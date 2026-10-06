package convert

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/mvc/m2ts"
)

// remuxBuiltin copies the disc's own streams out with the unwanted tracks
// removed: transport packets are copied untouched, with their arrival
// timestamps, and only the program tables are rewritten to list what is
// kept. Nothing is decoded or re-timed, so the result plays exactly as the
// disc does.
//
// A 3D title's two views live in two clips. On a disc they are interleaved
// in an SSIF, the dependent view's extent ahead of the base view's for the
// same stretch of time, and both clips run on one arrival clock; a folder
// rip may have them as two .m2ts files instead. Either way the packets are
// merged back into arrival order, which is what a single transport stream
// carrying both views (as a 3D remux is) needs.
func (r *Runner) remuxBuiltin(ctx context.Context, src *goSource, sel Selection) error {
	c := src.clips[0]
	prog, pmtPIDs, err := readProgram(ctx, c)
	if err != nil {
		return err
	}
	keep := map[uint16]bool{}
	for _, t := range append(append([]Track{sel.Base, sel.Dependent}, sel.Audio...), sel.Subtitles...) {
		keep[uint16(t.ID)] = true //nolint:gosec // track ids are PIDs
	}
	// A title of several clips must carry the kept tracks on the same PIDs
	// in every clip, as discs do: one program table describes the result.
	for _, later := range src.clips[1:] {
		lp, lpmts, err := readProgram(ctx, later)
		if err != nil {
			return err
		}
		if err := sameStreams(prog, lp, keep, c.path, later.path); err != nil {
			return err
		}
		for pid := range lpmts {
			pmtPIDs[pid] = true
		}
	}
	out := &m2ts.Program{Number: prog.Number, PCRPID: prog.PCRPID, Version: prog.Version, Info: prog.Info}
	langs := map[uint16]string{}
	for _, t := range append(append([]Track(nil), sel.Audio...), sel.Subtitles...) {
		langs[uint16(t.ID)] = strings.TrimSpace(t.Lang) //nolint:gosec // track ids are PIDs
	}
	// Base view, dependent view, then the rest in the disc's order — the
	// order a 3D disc's own combined program uses. An SSIF's dependent clip
	// has its tables first, which would otherwise put that view first.
	for _, pass := range []func(uint16) bool{
		func(pid uint16) bool { return pid == uint16(sel.Base.ID) },                                    //nolint:gosec // a PID
		func(pid uint16) bool { return pid == uint16(sel.Dependent.ID) },                               //nolint:gosec // a PID
		func(pid uint16) bool { return pid != uint16(sel.Base.ID) && pid != uint16(sel.Dependent.ID) }, //nolint:gosec // PIDs
	} {
		for _, s := range prog.Streams {
			if keep[s.PID] && pass(s.PID) {
				out.Streams = append(out.Streams, withLanguage(s, langs[s.PID]))
			}
		}
	}
	r.Report.Report("remuxing %d audio and %d subtitle track(s) with the disc's own video",
		len(sel.Audio), len(sel.Subtitles))

	f, err := os.Create(r.Opts.Output)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 4<<20)
	rm := &remuxer{w: w, prog: out, pcr: prog.PCRPID, keep: keep, pmts: pmtPIDs,
		base: uint16(sel.Base.ID), dep: uint16(sel.Dependent.ID), //nolint:gosec // PIDs
		ts188: strings.EqualFold(filepath.Ext(r.Opts.Output), ".ts"), report: r.Report, started: time.Now()}
	if len(src.clips) == 1 {
		err = rm.run(ctx, c)
	} else {
		err = rm.runClips(ctx, src.clips)
	}
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(r.Opts.Output)
		return fmt.Errorf("remuxing: %w", err)
	}
	r.Report.Report("wrote %.1f GiB", float64(rm.written)/(1<<30))
	return nil
}

// withLanguage adds an ISO 639 language descriptor to a stream that has a
// language but no descriptor saying so. A disc keeps its languages in the
// playlist, not in the stream; a remux has no playlist, so without this a
// player would find every track undetermined.
func withLanguage(s m2ts.ProgramStream, lang string) m2ts.ProgramStream {
	if len(lang) != 3 || s.Lang != "" {
		return s
	}
	d := append([]byte(nil), s.Descriptors...)
	s.Descriptors = append(d, 0x0a, 4, lang[0], lang[1], lang[2], 0)
	s.Lang = lang
	return s
}

// readProgram reads a clip's program tables: an SSIF's two PMTs merged,
// or the base and dependent files' when they are separate.
func readProgram(ctx context.Context, c clipRef) (*m2ts.Program, map[uint16]bool, error) {
	f, _, err := c.open()
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", c.path, err)
	}
	defer func() { _ = f.Close() }()
	rd := m2ts.NewReader(io.LimitReader(f, 32<<20))
	prog, err := rd.ReadProgram()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: no program table: %w", c.path, err)
	}
	// Read on so an SSIF's second PMT is seen.
	for rd.Packets*192 < 16<<20 {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if _, err := rd.Next(); err != nil {
			break
		}
	}
	pmts := map[uint16]bool{}
	for _, pid := range rd.PMTPIDs() {
		pmts[pid] = true
	}
	if c.dependent != nil {
		df, _, err := c.dependent.open()
		if err != nil {
			return nil, nil, err
		}
		dr := m2ts.NewReader(io.LimitReader(df, 4<<20))
		if dp, err := dr.ReadProgram(); err == nil {
			for _, s := range dp.Streams {
				if _, have := prog.Stream(s.PID); !have {
					prog.Streams = append(prog.Streams, s)
				}
			}
		}
		_ = df.Close()
	}
	return prog, pmts, nil
}

// packet is one 192-byte source packet: a 4-byte arrival timestamp, then
// the transport packet.
type packet struct {
	b   [192]byte
	ats int64 // unwrapped, 27 MHz
}

// remuxer filters and merges packets into the output.
type remuxer struct {
	w    io.Writer
	prog *m2ts.Program
	pcr  uint16
	keep map[uint16]bool
	pmts map[uint16]bool
	base uint16
	dep  uint16
	// win, for a title of several clips, cuts the clip being read to its
	// window and moves it onto one continuous timeline (see join.go); nil
	// copies a single clip untouched.
	win    *clipWindow
	join   *joinState
	ts188  bool
	report Reporter

	a, b             []*packet // base clip, dependent clip; each in arrival order
	lastA, lastB     int64
	unwrap           int64 // last unwrapped timestamp seen
	psiAt            int64 // timestamp of the last tables written
	psiWritten       bool
	ccPAT, ccPMT     byte
	written, packets int64
	started, lastRep time.Time
}

// atsWrap is the period of the 30-bit arrival timestamp.
const atsWrap = 1 << 30

// psiInterval is how often the rewritten tables are repeated: 100 ms at
// 27 MHz, the spacing a Blu-ray uses.
const psiInterval = 2_700_000

func (m *remuxer) run(ctx context.Context, c clipRef) error {
	f, size, err := c.open()
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	main := newPacketReader(f)
	interleaved := strings.EqualFold(filepath.Ext(c.path), ".ssif")
	var depReader *packetReader
	if c.dependent != nil {
		df, _, err := c.dependent.open()
		if err != nil {
			return err
		}
		defer func() { _ = df.Close() }()
		depReader = newPacketReader(df)
	}
	m.lastA, m.lastB = -1<<62, -1<<62
	m.unwrap = 0 // each clip runs on its own arrival clock
	if m.lastRep.IsZero() {
		m.lastRep = m.started
	}
	mainDone, depDone := false, depReader == nil
	for !mainDone || !depDone {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Read from whichever source the merge is waiting on.
		readDep := !depDone && (mainDone || (len(m.b) == 0 && len(m.a) > 0))
		if readDep {
			p, err := depReader.next()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					return err
				}
				depDone = true
			} else if p != nil {
				m.classify(p, false, true)
			}
		} else {
			p, err := main.next()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					return err
				}
				mainDone = true
			} else if p != nil {
				m.classify(p, interleaved, false)
			}
		}
		if err := m.drain(mainDone && depDone, depReader == nil && !interleaved); err != nil {
			return err
		}
		if now := time.Now(); now.Sub(m.lastRep) >= 30*time.Second {
			m.lastRep = now
			m.report.Report("remuxed %.1f of %.1f GiB", float64(main.read)/(1<<30), float64(size)/(1<<30))
		}
	}
	return m.drain(true, true)
}

// classify unwraps a packet's timestamp and queues it with its clip, or
// drops it: tables (rewritten), padding, and the streams not kept.
func (m *remuxer) classify(p *packet, interleaved, fromDep bool) {
	pid := uint16(p.b[5]&0x1f)<<8 | uint16(p.b[6])
	if pid == 0 || pid == 0x1f || pid == 0x1fff || m.isPMT(pid) {
		return
	}
	if pid != m.pcr && !m.keep[pid] {
		return
	}
	raw := int64(uint32(p.b[0])<<24|uint32(p.b[1])<<16|uint32(p.b[2])<<8|uint32(p.b[3])) & (atsWrap - 1)
	// Both clips run on one clock and are never seconds apart, far less
	// than the 40 s wrap, so the value nearest the last one seen is right.
	u := m.unwrap - m.unwrap%atsWrap + raw
	switch {
	case u-m.unwrap > atsWrap/2:
		u -= atsWrap
	case m.unwrap-u > atsWrap/2:
		u += atsWrap
	}
	m.unwrap = u
	p.ats = u
	switch {
	case fromDep:
		if pid == m.pcr {
			return // the base clip's clock references are the program's
		}
		m.push(&m.b, &m.lastB, p)
	case pid == m.dep:
		m.push(&m.b, &m.lastB, p)
	case pid == m.pcr && interleaved:
		// Both clips of an SSIF carry clock references on the same PID. The
		// dependent clip's fall in arrival order among its own packets and
		// are dropped; the base clip's fit after the base clip's last.
		if p.ats >= m.lastB {
			return
		}
		if p.ats >= m.lastA {
			m.push(&m.a, &m.lastA, p)
		}
	default:
		m.push(&m.a, &m.lastA, p)
	}
}

func (m *remuxer) push(q *[]*packet, last *int64, p *packet) {
	*q = append(*q, p)
	*last = p.ats
}

func (m *remuxer) isPMT(pid uint16) bool { return m.pmts[pid] || pid == 0x100 }

// drain writes packets in arrival order for as long as the order is known:
// while both clips have packets queued, or when one source has ended.
func (m *remuxer) drain(final, single bool) error {
	for {
		var p *packet
		switch {
		case len(m.a) > 0 && len(m.b) > 0:
			if m.b[0].ats < m.a[0].ats {
				p, m.b = m.b[0], m.b[1:]
			} else {
				p, m.a = m.a[0], m.a[1:]
			}
		case len(m.a) > 0 && (final || single):
			p, m.a = m.a[0], m.a[1:]
		case len(m.b) > 0 && final:
			p, m.b = m.b[0], m.b[1:]
		default:
			return nil
		}
		if err := m.write(p); err != nil {
			return err
		}
	}
}

func (m *remuxer) write(p *packet) error {
	if m.win != nil {
		return m.win.handle(m, p)
	}
	return m.emit(p)
}

// emit writes a packet, with the tables ahead of it when they are due.
func (m *remuxer) emit(p *packet) error {
	if !m.psiWritten || p.ats-m.psiAt >= psiInterval {
		if err := m.writeTables(p.b[:4]); err != nil {
			return err
		}
		m.psiAt, m.psiWritten = p.ats, true
	}
	return m.out(p.b[:])
}

func (m *remuxer) out(b []byte) error {
	if m.ts188 {
		b = b[4:]
	}
	n, err := m.w.Write(b)
	m.written += int64(n)
	m.packets++
	return err
}

// writeTables writes a PAT and the rewritten PMT, stamped with the arrival
// time of the packet they precede.
func (m *remuxer) writeTables(ats []byte) error {
	for _, t := range []struct {
		pid uint16
		sec []byte
		cc  *byte
	}{
		{0, m2ts.Section(0x00, 0, 0, m2ts.PATBody(m.prog.Number, 0x100)), &m.ccPAT},
		{0x100, m2ts.Section(0x02, m.prog.Number, m.prog.Version, m2ts.PMTBody(m.prog)), &m.ccPMT},
	} {
		sec := t.sec
		first := true
		for len(sec) > 0 || first {
			var pkt [192]byte
			copy(pkt[:4], ats)
			pkt[0] &= 0x3f // no copy permission bits on generated packets
			h := pkt[4:]
			h[0] = 0x47
			h[1] = byte(t.pid >> 8 & 0x1f)
			if first {
				h[1] |= 0x40
			}
			h[2] = byte(t.pid)
			h[3] = 0x10 | *t.cc&0x0f
			*t.cc++
			body := h[4:]
			if first {
				body[0] = 0 // pointer_field
				body = body[1:]
				first = false
			}
			n := copy(body, sec)
			for i := n; i < len(body); i++ {
				body[i] = 0xff
			}
			sec = sec[n:]
			if err := m.out(pkt[:]); err != nil {
				return err
			}
		}
	}
	return nil
}

// packetReader reads source packets, 192-byte or 188-byte (given a
// synthetic arrival time in order).
type packetReader struct {
	r    *bufio.Reader
	size int
	read int64
	seq  int64
}

func newPacketReader(r io.Reader) *packetReader {
	return &packetReader{r: bufio.NewReaderSize(r, 4<<20)}
}

func (pr *packetReader) next() (*packet, error) {
	if pr.size == 0 {
		b, err := pr.r.Peek(192 * 3)
		if err != nil && len(b) < 192*2 {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, err
		}
		switch {
		case b[4] == 0x47 && b[196] == 0x47:
			pr.size = 192
		case b[0] == 0x47 && b[188] == 0x47:
			pr.size = 188
		default:
			return nil, errors.New("not a transport stream")
		}
	}
	p := &packet{}
	buf := p.b[192-pr.size:]
	if _, err := io.ReadFull(pr.r, buf); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.EOF // a truncated last packet
		}
		return nil, err
	}
	pr.read += int64(pr.size)
	if p.b[4] != 0x47 {
		return nil, nil // damaged: skip it
	}
	if pr.size == 188 {
		pr.seq++
		v := uint32(pr.seq) & (atsWrap - 1) //nolint:gosec // wraps like an arrival time
		p.b[0], p.b[1], p.b[2], p.b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
	}
	return p, nil
}
