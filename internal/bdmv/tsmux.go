package bdmv

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"github.com/brunoga/bdtools/m2ts"
)

// The transport streams of a disc's clips: each stream's frames in PES
// packets, cut into transport packets, each given the 4-byte header of a
// Blu-ray source packet (its arrival time on the clip's 27 MHz clock).
// A 3D film's two clips (its base view with the audio and subtitles, its
// dependent view alone) run on one clock, as a player reading the SSIF
// that interleaves them needs.
//
// Each clip's packets are sent earliest deadline first: the next packet
// is the one of the frame decoded soonest, among the streams whose
// decoder buffer has room for it (and no more than a second ahead), at
// no more than the clip's rate; a PCR
// goes every 50 ms and the PAT, PMT and SIT every 100 ms, as tsMuxeR's do.

// MuxFrame is one frame of a stream: a video access unit, an audio frame,
// a subtitle segment.
type MuxFrame struct {
	PTS, DTS int64 // 90 kHz
	Data     []byte
	// Key is a random access point: an IDR picture, an entry point.
	Key bool
}

// MuxStream is a stream of a clip.
type MuxStream struct {
	PID    uint16
	Coding byte
	// Lang, Format, Rate and Aspect are its attributes (ClipStream's).
	Lang                 string
	Format, Rate, Aspect byte
	// Descriptors are its PMT descriptors after the registration one
	// (an AC-3 stream's audio descriptor, say).
	Descriptors []byte
	// Buffer is its decoder buffer, in bytes: how much of it may arrive
	// ahead of its decoding.
	Buffer int
	// Next gives its frames in decode order; io.EOF ends them.
	Next func() (MuxFrame, error)
}

// ClipMux is a clip being muxed: its streams in (the video first, whose
// IDR pictures are its entry points), its source packets out to W, and
// what its clip info needs once done.
type ClipMux struct {
	W       io.Writer
	Streams []*MuxStream
	// Rate is the clip's highest rate, in bits a second.
	Rate int

	// Packets is how many source packets were written (a multiple of 32:
	// the file is whole aligned units).
	Packets uint32
	// EP are the entry points of its first stream (the video).
	EP []EPEntry
	// Units are the arrival times of its aligned units' first packets (32
	// packets each), 27 MHz: where an SSIF may cut it.
	Units []int64
	// First and Last are its first and last frames' presentation times,
	// 90 kHz.
	First, Last int64
}

const (
	pcrPID      = 0x1001
	pmtPID      = 0x100
	sitPID      = 0x1f
	pcrInterval = 27_000_000 / 20 // 50 ms
	psiInterval = 27_000_000 / 10 // 100 ms
	// Preload is how far ahead of the first frame's decoding a clip's
	// first packet arrives.
	Preload = 27_000_000 / 2
	// maxLead is how far ahead of its decoding a frame may arrive, whatever
	// room its buffer has: a low-rate stream (a dependent view) would
	// otherwise run tens of seconds ahead, which a player reading the two
	// clips interleaved does not expect.
	maxLead = 27_000_000
)

// sitSection is the clip's SIT: a partial transport stream descriptor
// (its peak rate, 48 Mbit/s) and one service, tsMuxeR's.
var sitSection = []byte{0x7f, 0xf0, 0x19, 0xff, 0xff, 0xc1, 0x00, 0x00, 0xf0, 0x0a, 0x63, 0x08, 0xc1, 0xd4, 0xc0,
	0xff, 0xff, 0xff, 0xff, 0xff, 0x00, 0x01, 0x80, 0x00, 0x03, 0x00, 0x38, 0x6d}

// Mux writes the clips, their timestamps moved by shift (90 kHz) and
// arriving from start (27 MHz) on.
func Mux(clips []*ClipMux, start, shift int64) error {
	for _, c := range clips {
		if err := c.mux(start, shift); err != nil {
			return err
		}
	}
	return nil
}

// stream is a MuxStream being sent: its frame in packets, the frames in
// its buffer.
type stream struct {
	*MuxStream
	cc    byte
	pkts  [][]byte // the current frame's transport packets, unsent
	dts   int64    // its decoding, 27 MHz
	pts   int64    // its presentation, 90 kHz
	key   bool
	sent  int    // of its packets
	held  []held // what has arrived and not yet been decoded
	inBuf int    // their bytes
	done  bool   // no more frames
}

type held struct {
	dts   int64
	bytes int
}

func (c *ClipMux) mux(start, shift int64) error {
	if c.Rate <= 0 {
		return errors.New("bdmv: a clip with no rate")
	}
	w := bufio.NewWriterSize(c.W, 1<<20)
	slot := int64(188*8*27_000_000) / int64(c.Rate)
	slot = max(slot, 1)
	var ss []*stream
	for _, m := range c.Streams {
		ss = append(ss, &stream{MuxStream: m})
	}
	c.First, c.Last = -1, -1
	t := start
	var pcrCC, patCC, pmtCC, sitCC byte
	nextPCR, nextPSI := t, t
	var n uint32
	put := func(pkt []byte) error {
		if n%32 == 0 {
			c.Units = append(c.Units, t)
		}
		var hdr [4]byte
		ats := uint32(t) & 0x3fffffff //nolint:gosec // 30 bits of the clock
		hdr[0], hdr[1], hdr[2], hdr[3] = byte(ats>>24), byte(ats>>16), byte(ats>>8), byte(ats)
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := w.Write(pkt); err != nil {
			return err
		}
		n++
		t += slot
		return nil
	}
	pmt := m2ts.Section(2, 1, 0, m2ts.PMTBody(c.program()))
	pat := m2ts.Section(0, 0, 0, append(m2ts.PATBody(0, sitPID), m2ts.PATBody(1, pmtPID)...))
	psi := func() error {
		for _, s := range []struct {
			pid uint16
			cc  *byte
			sec []byte
		}{{0, &patCC, pat}, {pmtPID, &pmtCC, pmt}, {sitPID, &sitCC, sitSection}} {
			if err := put(sectionPacket(s.pid, s.cc, s.sec)); err != nil {
				return err
			}
		}
		return nil
	}
	// next loads a stream's next frame as packets.
	next := func(s *stream) error {
		f, err := s.Next()
		if errors.Is(err, io.EOF) {
			s.done = true
			return nil
		}
		if err != nil {
			return err
		}
		f.PTS += shift
		f.DTS += shift
		if c.First < 0 || f.PTS < c.First {
			c.First = f.PTS
		}
		s.pkts = pesPackets(s.PID, &s.cc, streamID(s.Coding), f)
		s.dts, s.pts, s.key, s.sent = f.DTS*300, f.PTS, f.Key, 0
		if f.PTS > c.Last {
			c.Last = f.PTS
		}
		return nil
	}
	for _, s := range ss {
		if err := next(s); err != nil {
			return err
		}
	}
	for {
		// what has been decoded leaves its buffer
		for _, s := range ss {
			for len(s.held) > 0 && s.held[0].dts <= t {
				s.inBuf -= s.held[0].bytes
				s.held = s.held[1:]
			}
		}
		if t >= nextPCR {
			if err := put(pcrPacket(&pcrCC, t)); err != nil {
				return err
			}
			nextPCR += pcrInterval
			continue
		}
		if t >= nextPSI {
			if err := psi(); err != nil {
				return err
			}
			nextPSI += psiInterval
			continue
		}
		var pick *stream
		var wake int64 = -1 // when a full buffer empties some, or a frame may go
		for _, s := range ss {
			if s.done {
				continue
			}
			if early := s.dts - maxLead; t < early {
				if wake < 0 || early < wake {
					wake = early
				}
				continue
			}
			if s.inBuf+184 > s.Buffer {
				if len(s.held) > 0 && (wake < 0 || s.held[0].dts < wake) {
					wake = s.held[0].dts
				}
				continue
			}
			if pick == nil || s.dts < pick.dts {
				pick = s
			}
		}
		if pick == nil {
			if wake < 0 {
				break // every stream sent
			}
			t = min(max(wake, t), nextPCR, nextPSI)
			continue
		}
		if t+slot > pick.dts {
			return fmt.Errorf("bdmv: stream %#x's frame at %.3f s cannot arrive in time at %d Mbit/s",
				pick.PID, float64(pick.dts)/27e6, c.Rate/1_000_000)
		}
		if pick.sent == 0 && pick.key && pick == ss[0] {
			c.EP = append(c.EP, EPEntry{PTS: uint64(pick.pts), SPN: n, IEnd: iEnd(len(pick.pkts))})
		}
		if err := put(pick.pkts[pick.sent]); err != nil {
			return err
		}
		pick.sent++
		pick.inBuf += 184
		if len(pick.held) == 0 || pick.held[len(pick.held)-1].dts != pick.dts {
			pick.held = append(pick.held, held{dts: pick.dts})
		}
		pick.held[len(pick.held)-1].bytes += 184
		if pick.sent == len(pick.pkts) {
			if err := next(pick); err != nil {
				return err
			}
		}
	}
	// whole aligned units
	var nullCC byte
	for n%32 != 0 {
		if err := put(nullPacket(&nullCC)); err != nil {
			return err
		}
	}
	c.Packets = n
	return w.Flush()
}

// iEnd is an entry point's I_end_position_offset for an I picture of n
// transport packets: its size in steps of 128 KiB (1 to 7), as tsMuxeR
// codes it.
func iEnd(n int) byte {
	bytes := n * 192
	for i, limit := range []int{128 << 10, 256 << 10, 384 << 10, 576 << 10, 896 << 10, 1280 << 10} {
		if bytes < limit {
			return byte(i + 1) //nolint:gosec // 1 to 6
		}
	}
	return 7
}

// program is the clip's PMT: HDMV's registration and copy control
// descriptors, each stream with its own registration.
func (c *ClipMux) program() *m2ts.Program {
	p := &m2ts.Program{PCRPID: pcrPID, Info: []byte{0x05, 0x04, 'H', 'D', 'M', 'V', 0x88, 0x04, 0x0f, 0xff, 0xfc, 0xfc}}
	for _, s := range c.Streams {
		var d []byte
		switch {
		case isVideo(s.Coding):
			d = []byte{0x05, 0x08, 'H', 'D', 'M', 'V', 0xff, s.Coding, s.Format<<4 | s.Rate, 0x3f}
		case s.Coding == CodingAC3 || s.Coding == CodingEAC3:
			d = []byte{0x05, 0x04, 'A', 'C', '-', '3'}
		}
		p.Streams = append(p.Streams, m2ts.ProgramStream{Type: s.Coding, PID: s.PID, Descriptors: append(d, s.Descriptors...)})
	}
	return p
}

// streamID is the PES stream id of a coding: video's 0xE0, the rest
// private stream 1.
func streamID(coding byte) byte {
	if isVideo(coding) {
		return 0xe0
	}
	return 0xbd
}

// pesPackets is a frame as a PES packet in transport packets of pid.
func pesPackets(pid uint16, cc *byte, sid byte, f MuxFrame) [][]byte {
	hdr := []byte{0, 0, 1, sid, 0, 0, 0x84}
	if f.DTS != f.PTS {
		hdr = append(hdr, 0xc0, 10)
		hdr = append(hdr, timestamp(3, f.PTS)...)
		hdr = append(hdr, timestamp(1, f.DTS)...)
	} else {
		hdr = append(hdr, 0x80, 5)
		hdr = append(hdr, timestamp(2, f.PTS)...)
	}
	if l := len(hdr) - 6 + len(f.Data); l <= 0xffff {
		hdr[4], hdr[5] = byte(l>>8), byte(l)
	}
	return packetize(pid, cc, append(hdr, f.Data...))
}

// timestamp is a PES PTS or DTS field, its prefix 4 bits pre.
func timestamp(pre byte, ts int64) []byte {
	v := uint64(ts) & (1<<33 - 1) //nolint:gosec // 33 bits
	return []byte{pre<<4 | byte(v>>29)&0x0e | 1, byte(v >> 22), byte(v>>14) | 1, byte(v >> 7), byte(v<<1) | 1}
}

// packetize cuts a PES packet into transport packets, the last one padded
// by its adaptation field.
func packetize(pid uint16, cc *byte, pes []byte) [][]byte {
	var out [][]byte
	for first := true; len(pes) > 0; first = false {
		pkt := make([]byte, 188)
		pkt[0] = 0x47
		pkt[1] = byte(pid >> 8)
		if first {
			pkt[1] |= 0x40
		}
		pkt[2] = byte(pid)
		n := min(len(pes), 184)
		if n == 184 {
			pkt[3] = 0x10 | *cc
			copy(pkt[4:], pes)
		} else {
			// an adaptation field fills what the payload does not
			pkt[3] = 0x30 | *cc
			af := 183 - n
			pkt[4] = byte(af)
			if af > 0 {
				pkt[5] = 0 // no flags; the rest stuffing
				stuff := pkt[6 : 5+af]
				for i := range stuff {
					stuff[i] = 0xff
				}
			}
			copy(pkt[5+af:], pes[:n])
		}
		*cc = (*cc + 1) & 15
		pes = pes[n:]
		out = append(out, pkt)
	}
	return out
}

// sectionPacket is a PSI section in one transport packet.
func sectionPacket(pid uint16, cc *byte, sec []byte) []byte {
	pkt := make([]byte, 188)
	pkt[0], pkt[1], pkt[2], pkt[3] = 0x47, 0x40|byte(pid>>8), byte(pid), 0x10|*cc
	*cc = (*cc + 1) & 15
	pkt[4] = 0 // pointer_field
	copy(pkt[5:], sec)
	for i := 5 + len(sec); i < 188; i++ {
		pkt[i] = 0xff
	}
	return pkt
}

// pcrPacket is a packet of the PCR PID carrying only the clock, t.
func pcrPacket(cc *byte, t int64) []byte {
	pkt := make([]byte, 188)
	pkt[0], pkt[1], pkt[2], pkt[3] = 0x47, byte(pcrPID>>8), byte(pcrPID&0xff), 0x20|*cc
	pkt[4], pkt[5] = 183, 0x10
	base, ext := uint64(t/300)&(1<<33-1), uint64(t%300) //nolint:gosec // the PCR's fields
	pkt[6], pkt[7], pkt[8], pkt[9] = byte(base>>25), byte(base>>17), byte(base>>9), byte(base>>1)
	pkt[10] = byte(base<<7) | 0x7e | byte(ext>>8)
	pkt[11] = byte(ext)
	for i := 12; i < 188; i++ {
		pkt[i] = 0xff
	}
	return pkt
}

// nullPacket is a null packet, which fills a file to its aligned units.
func nullPacket(cc *byte) []byte {
	pkt := make([]byte, 188)
	pkt[0], pkt[1], pkt[2], pkt[3] = 0x47, 0x1f, 0xff, 0x10|*cc
	*cc = (*cc + 1) & 15
	for i := 4; i < 188; i++ {
		pkt[i] = 0xff
	}
	return pkt
}
