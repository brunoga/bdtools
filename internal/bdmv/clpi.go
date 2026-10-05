package bdmv

import (
	"encoding/binary"
	"errors"
)

// ClipStream is one stream of a clip's program, from its clip info.
type ClipStream struct {
	PID    uint16
	Coding byte
	Lang   string
}

// ParseCLPI reads the streams of a clip info (.clpi) file: the program's
// PIDs, coding types and languages, which a bare stream file does not carry
// in its own tables.
func ParseCLPI(b []byte) ([]ClipStream, error) {
	if len(b) < 40 || string(b[:4]) != "HDMV" {
		return nil, errors.New("clpi: not a clip info file")
	}
	pos := int(binary.BigEndian.Uint32(b[12:]))
	c := &cursor{b: b, pos: pos}
	c.u32() // length
	c.skip(1)
	nProg := int(c.u8())
	var out []ClipStream
	for i := 0; i < nProg && c.err == nil; i++ {
		c.u32() // SPN_program_sequence_start
		c.u16() // program_map_PID
		nStreams := int(c.u8())
		c.skip(1) // num_groups
		for j := 0; j < nStreams && c.err == nil; j++ {
			s := ClipStream{PID: c.u16()}
			l := int(c.u8())
			start := c.pos
			s.Coding = c.u8()
			switch s.Coding {
			case CodingLPCM, CodingAC3, CodingDTS, CodingTrueHD, CodingEAC3, CodingDTSHDHR, CodingDTSHDMA,
				CodingEAC3Sec, CodingDTSSec, 0x03, 0x04:
				c.skip(1)
				s.Lang = c.str(3)
			case CodingPGS, CodingIGS:
				s.Lang = c.str(3)
			case CodingText:
				c.skip(1)
				s.Lang = c.str(3)
			}
			c.pos = start + l
			if c.err == nil {
				out = append(out, s)
			}
		}
	}
	return out, c.err
}
