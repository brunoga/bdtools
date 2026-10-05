package m2ts

import (
	"bufio"
	"errors"
	"io"
)

// Stream types of a Blu-ray program (stream_type in the PMT).
const (
	TypeMPEG2Video = 0x02
	TypeAVC        = 0x1b
	TypeMVC        = 0x20
	TypeHEVC       = 0x24
	TypeVC1        = 0xea
	TypeLPCM       = 0x80
	TypeAC3        = 0x81
	TypeDTS        = 0x82
	TypeTrueHD     = 0x83
	TypeEAC3       = 0x84
	TypeDTSHDHR    = 0x85
	TypeDTSHDMA    = 0x86
	TypeEAC3Sec    = 0xa1
	TypeDTSSec     = 0xa2
	TypePGS        = 0x90
	TypeIGS        = 0x91
	TypeText       = 0x92
)

// ProgramStream is one elementary stream the PMT announces.
type ProgramStream struct {
	PID  uint16
	Type byte
	// Lang is the ISO 639-2 code from the language descriptor, if any.
	Lang string
	// Descriptors is the stream's raw descriptor loop, for rewriting a PMT.
	Descriptors []byte
}

// Program is the content of the PMT.
type Program struct {
	Number  uint16
	PCRPID  uint16
	Version byte
	// Info is the raw program_info descriptor loop.
	Info    []byte
	Streams []ProgramStream
}

// Stream returns the program's entry for a PID, if any.
func (p *Program) Stream(pid uint16) (ProgramStream, bool) {
	for _, s := range p.Streams {
		if s.PID == pid {
			return s, true
		}
	}
	return ProgramStream{}, false
}

// PES is one packetised elementary stream packet, with its header removed.
type PES struct {
	PID      uint16
	StreamID byte
	PTS, DTS int64 // 90 kHz, -1 when absent
	Payload  []byte
}

// Reader splits a transport stream into PES packets, for every PID or a
// chosen set, and reads the program tables on the way.
type Reader struct {
	r        *bufio.Reader
	pktSize  int
	pkt      [192]byte
	want     map[uint16]bool // nil: every PID
	cur      map[uint16]*pesBuf
	ready    []PES
	sections map[uint16][]byte // PSI section assembly by PID
	pmtPIDs  map[uint16]bool
	program  *Program
	eof      bool
	// Packets counts the transport packets read.
	Packets int64
}

type pesBuf struct {
	data   []byte
	length int // PES_packet_length, 0 when unbounded
}

// NewReader returns a reader over a 188- or 192-byte-packet stream.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 1<<20), cur: map[uint16]*pesBuf{}, sections: map[uint16][]byte{}, pmtPIDs: map[uint16]bool{}}
}

// Select limits the PES packets returned to these PIDs. The program tables
// are read regardless.
func (r *Reader) Select(pids ...uint16) {
	r.want = map[uint16]bool{}
	for _, p := range pids {
		r.want[p] = true
	}
}

// PMTPIDs returns the PIDs the PAT announced PMTs on.
func (r *Reader) PMTPIDs() []uint16 {
	var out []uint16
	for pid := range r.pmtPIDs {
		out = append(out, pid)
	}
	return out
}

// Program returns the PMT once it has been read, else nil.
func (r *Reader) Program() *Program { return r.program }

// ReadProgram reads packets until a PMT has been seen, keeping any PES
// packets completed meanwhile for Next. A file interleaving two clips (an
// SSIF) carries a PMT per clip; the program grows as later ones are read,
// so a caller wanting every stream reads on and asks Program again.
func (r *Reader) ReadProgram() (*Program, error) {
	for r.program == nil {
		if err := r.step(); err != nil {
			return nil, err
		}
	}
	return r.program, nil
}

var errNoSync = errors.New("m2ts: not a transport stream")

func (r *Reader) detect() error {
	b, err := r.r.Peek(192 * 3)
	if err != nil && len(b) < 192*2 {
		if err == io.EOF {
			return errNoSync
		}
		return err
	}
	switch {
	case b[4] == 0x47 && b[196] == 0x47:
		r.pktSize = 192
	case b[0] == 0x47 && b[188] == 0x47:
		r.pktSize = 188
	default:
		return errNoSync
	}
	return nil
}

// readPacket returns the 188-byte body of the next packet, resynchronising
// on the sync byte after damage.
func (r *Reader) readPacket() ([]byte, error) {
	if r.pktSize == 0 {
		if err := r.detect(); err != nil {
			return nil, err
		}
	}
	buf := r.pkt[:r.pktSize]
	if _, err := io.ReadFull(r.r, buf); err != nil {
		return nil, err
	}
	p := buf[r.pktSize-188:]
	if p[0] == 0x47 {
		r.Packets++
		return p, nil
	}
	for {
		c, err := r.r.ReadByte()
		if err != nil {
			return nil, err
		}
		if c != 0x47 {
			continue
		}
		n := r.pktSize - 188
		r.pkt[n] = 0x47
		if _, err := io.ReadFull(r.r, r.pkt[n+1:r.pktSize]); err != nil {
			return nil, err
		}
		r.Packets++
		return r.pkt[n:r.pktSize], nil
	}
}

func parseTimestamp(b []byte) int64 {
	return int64(b[0]>>1&7)<<30 | int64(b[1])<<22 | int64(b[2]>>1)<<15 | int64(b[3])<<7 | int64(b[4]>>1)
}

// finish completes a PES and queues it.
func (r *Reader) finish(pid uint16) {
	c := r.cur[pid]
	if c == nil {
		return
	}
	delete(r.cur, pid)
	b := c.data
	if len(b) < 9 || b[0] != 0 || b[1] != 0 || b[2] != 1 {
		return
	}
	sid := b[3]
	// Streams without the MPEG-2 PES header extension (none on a Blu-ray,
	// but a stray padding stream must not be misread).
	switch sid {
	case 0xbc, 0xbe, 0xbf, 0xf0, 0xf1, 0xff, 0xf2, 0xf8:
		return
	}
	flags := b[7]
	hl := int(b[8])
	if 9+hl > len(b) {
		return
	}
	p := PES{PID: pid, StreamID: sid, PTS: -1, DTS: -1}
	if flags&0x80 != 0 && hl >= 5 {
		p.PTS = parseTimestamp(b[9:])
		p.DTS = p.PTS
	}
	if flags&0xc0 == 0xc0 && hl >= 10 {
		p.DTS = parseTimestamp(b[14:])
	}
	p.Payload = b[9+hl:]
	if c.length > 0 && 6+c.length < len(b) {
		p.Payload = b[9+hl : 6+c.length]
	}
	r.ready = append(r.ready, p)
}

// step reads one packet.
func (r *Reader) step() error {
	if r.eof {
		return io.EOF
	}
	p, err := r.readPacket()
	if err != nil {
		r.eof = true
		for pid := range r.cur {
			r.finish(pid)
		}
		return err
	}
	if p[1]&0x80 != 0 { // transport_error_indicator
		return nil
	}
	pid := uint16(p[1]&0x1f)<<8 | uint16(p[2])
	pusi := p[1]&0x40 != 0
	afc := p[3] >> 4 & 3
	payload := p[4:]
	if afc&2 != 0 {
		al := int(p[4])
		if 5+al > len(p) {
			return nil
		}
		payload = p[5+al:]
	}
	if afc&1 == 0 {
		return nil
	}
	if pid == 0 || r.pmtPIDs[pid] {
		r.section(pid, pusi, payload)
		return nil
	}
	if pid == 0x1fff {
		return nil
	}
	if r.want != nil && !r.want[pid] {
		return nil
	}
	if pusi {
		r.finish(pid)
		c := &pesBuf{data: make([]byte, 0, 4096)}
		if len(payload) >= 6 && payload[0] == 0 && payload[1] == 0 && payload[2] == 1 {
			c.length = int(payload[4])<<8 | int(payload[5])
		}
		r.cur[pid] = c
	}
	if c := r.cur[pid]; c != nil {
		c.data = append(c.data, payload...)
		if c.length > 0 && len(c.data) >= 6+c.length {
			r.finish(pid)
		}
	}
	return nil
}

// Next returns the next PES packet. Packets of the same PID come in stream
// order; across PIDs in the order they complete. The payload is owned by
// the caller.
func (r *Reader) Next() (PES, error) {
	for len(r.ready) == 0 {
		if err := r.step(); err != nil && len(r.ready) == 0 {
			return PES{}, err
		}
	}
	p := r.ready[0]
	r.ready[0] = PES{}
	r.ready = r.ready[1:]
	return p, nil
}
