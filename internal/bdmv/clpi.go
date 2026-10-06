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
	// Format and Rate are the stream's coding attributes as the clip info
	// gives them. Video: video_format (1 480i, 2 576i, 3 480p, 4 1080i,
	// 5 720p, 6 1080p, 7 576p, 8 2160p) and frame_rate (1 23.976, 2 24,
	// 3 25, 4 29.97, 6 50, 7 59.94). Audio: audio_presentation_type (1 mono,
	// 3 stereo, 6 multi-channel, 12 stereo + multi-channel) and
	// sampling_frequency (1 48 kHz, 4 96 kHz, 5 192 kHz, 12 192 kHz with a
	// 48 kHz core, 14 96 kHz with a 48 kHz core). Zero when not given.
	Format, Rate byte
}

// ParseCLPI reads the streams of a clip info (.clpi) file: the program's
// PIDs, coding types and languages, which a bare stream file does not carry
// in its own tables. A dependent view's clip lists its MVC stream in the
// file's 3D extension (ProgramInfo_SS) rather than its program, and that
// stream comes after the program's.
func ParseCLPI(b []byte) ([]ClipStream, error) {
	if len(b) < 40 || string(b[:4]) != "HDMV" {
		return nil, errors.New("clpi: not a clip info file")
	}
	out, err := parseProgramInfo(b, int(binary.BigEndian.Uint32(b[12:])))
	if err != nil {
		return out, err
	}
	if at, ok := clpiExtension(b, 2, 5); ok {
		ss, err := parseProgramInfo(b, at)
		if err != nil {
			return out, err
		}
		out = append(out, ss...)
	}
	return out, nil
}

// clpiExtension finds the ExtensionData entry id1/id2 and returns where its
// data starts, if the file has one.
func clpiExtension(b []byte, id1, id2 uint16) (int, bool) {
	ext := int(binary.BigEndian.Uint32(b[24:]))
	if ext == 0 || ext+12 > len(b) {
		return 0, false
	}
	n := int(b[ext+11])
	for i := range n {
		e := ext + 12 + 12*i
		if e+12 > len(b) {
			return 0, false
		}
		if binary.BigEndian.Uint16(b[e:]) == id1 && binary.BigEndian.Uint16(b[e+2:]) == id2 {
			at := ext + int(binary.BigEndian.Uint32(b[e+4:]))
			return at, at < len(b)
		}
	}
	return 0, false
}

// parseProgramInfo reads a ProgramInfo structure at pos.
func parseProgramInfo(b []byte, pos int) ([]ClipStream, error) {
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
			case 0x01, 0x02, CodingAVC, CodingMVC, CodingHEVC, 0xea:
				a := c.u8()
				s.Format, s.Rate = a>>4, a&0x0f
			case CodingLPCM, CodingAC3, CodingDTS, CodingTrueHD, CodingEAC3, CodingDTSHDHR, CodingDTSHDMA,
				CodingEAC3Sec, CodingDTSSec, 0x03, 0x04:
				a := c.u8()
				s.Format, s.Rate = a>>4, a&0x0f
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
