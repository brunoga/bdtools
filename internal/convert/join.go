package convert

import (
	"context"
	"fmt"

	"github.com/brunoga/bdtools/m2ts"
)

// Joining the clips of a title for a remux: each clip is cut to its
// PlayItem's window and moved onto one continuous timeline, so the result
// is a single transport stream that plays the title through, as one clip
// would. Nothing is decoded or re-encoded.
//
//   - The timeline is made continuous rather than marked discontinuous:
//     every later clip's PTS and DTS, its clock references (PCR) and its
//     arrival timestamps are moved by the clip's offset. A discontinuity
//     indicator is what a broadcast uses, but many players and tools (and
//     every seek index) cope badly with time running backwards; a single
//     clock plays and seeks everywhere.
//   - A clip starts at the last random access point at or before its IN
//     time: the picture that opens the GOP, which a remux cannot move. The
//     pictures between it and IN are kept (a fraction of a second at most),
//     since dropping them would leave the pictures after them undecodable.
//   - A picture is kept while its decoding time is before OUT, so no kept
//     picture loses one it is predicted from; a picture or two past OUT can
//     show as a result.
//   - Audio and subtitle packets are kept when their presentation time is
//     within IN to OUT, at PES granularity: a frame of audio either side.
//   - Continuity counters are renumbered per PID, since the cuts and the
//     joins break the sequence a decoder checks.

// clipWindow is the cut and the move applied to one clip.
type clipWindow struct {
	in, out int64 // 90 kHz, the clip's own timeline
	// shift is added to PTS and DTS (90 kHz). It starts as the clip's
	// nominal place after the clips before it, and is settled when the
	// clip's start is found (see begin).
	shift int64
	first bool // the title's first clip, which keeps its own times

	// atsShift moves arrival timestamps, so the arrival clock keeps the
	// relation to the program clock the first clip had.
	atsShift int64
	atsKnown bool
	pcr0     int64 // the clip's first clock reference, and its arrival
	pcr0ATS  int64
	havePCR0 bool

	started bool
	haveRAP bool
	rapDTS  int64 // the random access point the clip starts at
	rapPTS  int64
	rapATS  int64
	ended   bool // a picture decoded at or past OUT has been seen

	tags map[uint16]pesTag // the PES each PID's packets belong to
	buf  []heldPacket      // packets before the start, while it is found
	kept int64             // packets written from this clip
}

// pesTag is a PES's timestamps, which its later packets share.
type pesTag struct {
	pts, dts int64 // -1 when unknown
}

type heldPacket struct {
	p   *packet
	pid uint16
	tag pesTag
}

// joinState is what carries from one clip to the next, on the output's
// timeline (unwrapped).
type joinState struct {
	atsOrigin  int64 // arrival time minus PCR (27 MHz) of the first clip
	haveOrigin bool
	lastATS    int64
	lastPCR    int64
	havePCR    bool
	lastPTS    int64 // the base view's latest presentation and decoding times
	lastDTS    int64
	haveVideo  bool
	frameDur   int64 // the smallest step between pictures seen (90 kHz)
	cc         map[uint16]byte
	started    bool
}

// pcrWrap is the period of the PCR in 27 MHz units (2^33 × 300).
const pcrWrap = int64(1) << 33 * 300

// defaultFrameDur is a picture at 23.976 fps, for a step not yet measured.
const defaultFrameDur = 3754

// runClips remuxes a title of several clips into one continuous stream.
func (m *remuxer) runClips(ctx context.Context, clips []clipRef) error {
	var offset int64 // the output time (90 kHz, from the first IN) of the next clip
	m.join = &joinState{cc: map[uint16]byte{}}
	for k, c := range clips {
		if c.inTime < 0 || c.outTime <= c.inTime {
			return fmt.Errorf("%s has no play window, so it cannot be joined", c.path)
		}
		nominal := clips[0].inTime + offset - c.inTime
		m.win = &clipWindow{in: c.inTime, out: c.outTime, shift: nominal,
			first: k == 0, atsKnown: k == 0, tags: map[uint16]pesTag{}}
		m.report.Report("remuxing %s (clip %d of %d)", c.path, k+1, len(clips))
		if err := m.run(ctx, c); err != nil {
			return err
		}
		if !m.win.started {
			m.report.Report("warning: %s has no picture in its window; it adds nothing", c.path)
		}
		// A clip moved later than its nominal place (see begin) moves the
		// clips after it by as much.
		offset += c.outTime - c.inTime + m.win.shift - nominal
	}
	m.win = nil
	return nil
}

// handle cuts and moves one packet of the clip being read.
func (w *clipWindow) handle(m *remuxer, p *packet) error {
	ts := p.b[4:]
	pid := uint16(ts[1]&0x1f)<<8 | uint16(ts[2])
	pusi := ts[1]&0x40 != 0
	payload := tsPayload(ts)
	es := pid == m.base || pid == m.dep || m.keep[pid]
	if es && pusi {
		t := pesTag{pts: -1, dts: -1}
		if pts, dts, ok := pesTimes(payload); ok {
			t = pesTag{pts: pts, dts: dts}
		}
		w.tags[pid] = t
		if pid == m.base && !w.started && t.dts >= 0 && isRandomAccess(payload) {
			w.prune(m, t, p.ats)
		}
	}
	t, tagged := w.tags[pid]
	if !tagged {
		t = pesTag{pts: -1, dts: -1}
	}
	if pcr, ok := tsPCR(ts); ok && !w.havePCR0 {
		w.pcr0, w.pcr0ATS, w.havePCR0 = pcr, p.ats, true
	}
	if !w.started {
		w.buf = append(w.buf, heldPacket{p: p, pid: pid, tag: t})
		if pid == m.base && pusi && w.haveRAP && t.pts >= w.in {
			w.begin(m)
			held := w.buf
			w.buf = nil
			for _, h := range held {
				if w.keepPacket(m, h.pid, h.tag, h.p) {
					if err := w.move(m, h.p); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if !w.keepPacket(m, pid, t, p) {
		return nil
	}
	return w.move(m, p)
}

// begin settles the clip's shift once its first picture is known. A clip's
// opening pictures decode a little before its IN time and the clip before
// it decodes up to its OUT time, so on their nominal places the two would
// overlap; the clip moves just far enough for its first picture to decode
// and show after the last one before it. Then the arrival clock follows.
func (w *clipWindow) begin(m *remuxer) {
	w.started = true
	js := m.join
	if !w.first && js.haveVideo {
		step := js.frameDur
		if step <= 0 {
			step = defaultFrameDur
		}
		w.shift = max(w.shift, js.lastDTS+step-w.rapDTS, js.lastPTS+step-w.rapPTS)
	}
	if !w.atsKnown && w.havePCR0 && js.haveOrigin {
		w.atsShift = js.atsOrigin + w.pcr0 + w.shift*300 - w.pcr0ATS
		w.atsKnown = true
	}
}

// prune, at a random access point before the window starts, drops what
// can no longer be needed: pictures before it, audio before IN, and the
// clock references before it.
func (w *clipWindow) prune(m *remuxer, rap pesTag, ats int64) {
	w.haveRAP, w.rapDTS, w.rapPTS, w.rapATS = true, rap.dts, rap.pts, ats
	kept := w.buf[:0]
	for _, h := range w.buf {
		if w.keepPacket(m, h.pid, h.tag, h.p) {
			kept = append(kept, h)
		}
	}
	clear(w.buf[len(kept):])
	w.buf = kept
}

// keepPacket decides one packet by the PES it belongs to.
func (w *clipWindow) keepPacket(m *remuxer, pid uint16, t pesTag, p *packet) bool {
	switch {
	case pid == m.base || pid == m.dep:
		if t.dts < 0 || !w.haveRAP || t.dts < w.rapDTS {
			return false
		}
		if t.dts >= w.out {
			if pid == m.base {
				w.ended = true
			}
			return false
		}
		return true
	case m.keep[pid]:
		return t.pts >= w.in && t.pts < w.out
	default: // clock references
		return w.haveRAP && p.ats >= w.rapATS && !w.ended
	}
}

// move retimes a kept packet onto the output's timeline and writes it.
func (w *clipWindow) move(m *remuxer, p *packet) error {
	ts := p.b[4:]
	js := m.join
	pid := uint16(ts[1]&0x1f)<<8 | uint16(ts[2])
	pusi := ts[1]&0x40 != 0
	if pcr, ok := tsPCR(ts); ok {
		out := pcr + w.shift*300
		switch {
		case js.havePCR && out <= js.lastPCR:
			// A clock reference may not run backwards. The next clip's first
			// packets arrive while the clip before it plays out, so a few
			// of its references can fall behind; they are left out.
			if ts[3]&0x10 == 0 {
				return nil // a reference and nothing else
			}
			stripPCR(ts, js.lastPCR+1)
		default:
			setPCR(ts, (out%pcrWrap+pcrWrap)%pcrWrap)
			js.lastPCR, js.havePCR = out, true
			if !js.haveOrigin && w.first {
				js.atsOrigin, js.haveOrigin = p.ats-out, true
			}
		}
	}
	if pusi {
		payload := tsPayload(ts)
		if pid == m.base {
			if pts, dts, ok := pesTimes(payload); ok {
				pts, dts = pts+w.shift, dts+w.shift
				if js.haveVideo {
					if d := dts - js.lastDTS; d > 0 && (js.frameDur == 0 || d < js.frameDur) {
						js.frameDur = d
					}
				}
				js.lastPTS = max(js.lastPTS, pts)
				js.lastDTS = max(js.lastDTS, dts)
				js.haveVideo = true
			}
		}
		if w.shift != 0 {
			shiftPESTimes(payload, w.shift)
		}
	}
	if !w.atsKnown {
		// No clock reference in this clip: carry on from the last packet.
		w.atsShift, w.atsKnown = js.lastATS+1-p.ats, true
	}
	ats := p.ats + w.atsShift
	if js.started && ats < js.lastATS {
		ats = js.lastATS // arrival time never runs backwards
	}
	js.lastATS, js.started = ats, true
	p.ats = ats
	v := uint32(ats) & (atsWrap - 1) //nolint:gosec // the 30-bit field
	p.b[0] = p.b[0]&0xc0 | byte(v>>24)
	p.b[1], p.b[2], p.b[3] = byte(v>>16), byte(v>>8), byte(v)
	// Continuity counters run on across the cuts.
	if ts[3]&0x10 != 0 { // carries a payload
		cc, seen := js.cc[pid]
		if seen {
			cc = (cc + 1) & 0x0f
		} else {
			cc = ts[3] & 0x0f
		}
		js.cc[pid] = cc
		ts[3] = ts[3]&0xf0 | cc
	} else if cc, seen := js.cc[pid]; seen {
		ts[3] = ts[3]&0xf0 | cc
	}
	w.kept++
	return m.emit(p)
}

// stripPCR takes a clock reference out of a packet that carries a payload
// too: its adaptation field's PCR becomes stuffing when nothing follows it,
// and otherwise the reference is held at the last one.
func stripPCR(ts []byte, at int64) {
	if ts[5]&0x0f == 0 {
		ts[5] &^= 0x10
		for i := 6; i < 12; i++ {
			ts[i] = 0xff
		}
		return
	}
	setPCR(ts, (at%pcrWrap+pcrWrap)%pcrWrap)
}

// --- transport and PES fields ------------------------------------------------------

// tsPayload returns a transport packet's payload, nil if it has none.
func tsPayload(ts []byte) []byte {
	afc := ts[3] >> 4 & 3
	if afc&1 == 0 {
		return nil
	}
	off := 4
	if afc&2 != 0 {
		off += 1 + int(ts[4])
	}
	if off >= len(ts) {
		return nil
	}
	return ts[off:188]
}

// tsPCR returns a packet's clock reference in 27 MHz units.
func tsPCR(ts []byte) (int64, bool) {
	if ts[3]&0x20 == 0 || ts[4] < 7 || ts[5]&0x10 == 0 {
		return 0, false
	}
	b := ts[6:12]
	base := int64(b[0])<<25 | int64(b[1])<<17 | int64(b[2])<<9 | int64(b[3])<<1 | int64(b[4])>>7
	ext := int64(b[4]&1)<<8 | int64(b[5])
	return base*300 + ext, true
}

func setPCR(ts []byte, pcr int64) {
	base, ext := pcr/300, pcr%300
	b := ts[6:12]
	b[0], b[1], b[2], b[3] = byte(base>>25), byte(base>>17), byte(base>>9), byte(base>>1)
	b[4] = byte(base<<7) | 0x7e | byte(ext>>8)
	b[5] = byte(ext)
}

// hasPESHeader reports whether a stream ID's PES carries the optional
// header (and so may carry timestamps).
func hasPESHeader(sid byte) bool {
	switch sid {
	case 0xbc, 0xbe, 0xbf, 0xf0, 0xf1, 0xff, 0xf2, 0xf8:
		return false
	}
	return true
}

// pesTimes reads the PTS and DTS of a PES starting in b (DTS = PTS when
// absent).
func pesTimes(b []byte) (pts, dts int64, ok bool) {
	if len(b) < 14 || b[0] != 0 || b[1] != 0 || b[2] != 1 || !hasPESHeader(b[3]) {
		return 0, 0, false
	}
	flags := b[7] >> 6
	if flags&2 == 0 {
		return 0, 0, false
	}
	pts = readTS(b[9:14])
	dts = pts
	if flags == 3 && len(b) >= 19 {
		dts = readTS(b[14:19])
	}
	return pts, dts, true
}

// shiftPESTimes moves the PTS and DTS of a PES starting in b.
func shiftPESTimes(b []byte, by int64) {
	if len(b) < 14 || b[0] != 0 || b[1] != 0 || b[2] != 1 || !hasPESHeader(b[3]) {
		return
	}
	flags := b[7] >> 6
	if flags&2 == 0 {
		return
	}
	writeTS(b[9:14], readTS(b[9:14])+by)
	if flags == 3 && len(b) >= 19 {
		writeTS(b[14:19], readTS(b[14:19])+by)
	}
}

const ts33 = int64(1) << 33

func readTS(b []byte) int64 {
	return int64(b[0]>>1&7)<<30 | int64(b[1])<<22 | int64(b[2]>>1)<<15 | int64(b[3])<<7 | int64(b[4]>>1)
}

// writeTS stores a 33-bit timestamp, wrapping it, and keeps the field's
// prefix and marker bits.
func writeTS(b []byte, v int64) {
	v = (v%ts33 + ts33) % ts33
	b[0] = b[0]&0xf0 | byte(v>>29)&0x0e | 1
	b[1] = byte(v >> 22)
	b[2] = byte(v>>14)&0xfe | 1
	b[3] = byte(v >> 7)
	b[4] = byte(v<<1) | 1
}

// isRandomAccess reports whether a video PES opens a GOP: its access unit
// carries a sequence parameter set or an IDR slice, as a Blu-ray's random
// access points do.
func isRandomAccess(b []byte) bool {
	if len(b) < 9 {
		return false
	}
	start := 9 + int(b[8])
	for i := start; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			switch b[i+3] & 0x1f {
			case 5, 7:
				return true
			case 1:
				return false // a non-IDR slice first: not a GOP start
			}
		}
	}
	return false
}

// sameStreams checks a later clip carries every kept track on the same PID
// with the same type as the first: the joined stream has one program table.
func sameStreams(first, later *m2ts.Program, keep map[uint16]bool, firstPath, laterPath string) error {
	for pid := range keep {
		a, _ := first.Stream(pid)
		b, ok := later.Stream(pid)
		if !ok || b.Type != a.Type {
			return fmt.Errorf("%s does not carry track %d as %s does: the clips of this title do not "+
				"share their streams, so they cannot be joined into one", laterPath, pid, firstPath)
		}
	}
	return nil
}
