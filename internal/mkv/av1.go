package mkv

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// AV1 in Matroska (the AV1-in-Matroska codec mapping): CodecID V_AV1, the
// codec private data an av1C box (four bytes, then the sequence header OBU),
// and each block one temporal unit with its temporal delimiter removed. A
// temporal unit holds exactly one shown frame, and AV1 has no reordering
// visible to the container, so block n is shown at n frame durations.
//
// The input is what the encoders write: a low-overhead bitstream, OBU after
// OBU, each with its size, every temporal unit starting with a temporal
// delimiter.

// OBU types.
const (
	obuSequenceHeader    = 1
	obuTemporalDelimiter = 2
	obuFrameHeader       = 3
	obuFrame             = 6
)

// av1Seq is what the sequence header says that the container needs.
type av1Seq struct {
	profile, level, tier          int
	highBitDepth, twelveBit, mono bool
	subX, subY                    bool
	chromaPosition                int
	width, height                 int
	reduced                       bool // reduced_still_picture_header
	frameIDs                      bool // frame_id_numbers_present_flag
	raw                           []byte
}

// av1State reads temporal units.
type av1State struct {
	r     *bufio.Reader
	seq   *av1Seq
	held  []byte // the next temporal unit's first OBU, read ahead
	eof   bool
	first *Frame // the temporal unit read while priming
}

// readOBU reads one OBU whole (header, size field, payload) and returns its
// type and payload.
func readOBU(r *bufio.Reader) (typ int, whole, payload []byte, err error) {
	h, err := r.ReadByte()
	if err != nil {
		return 0, nil, nil, err
	}
	if h&0x80 != 0 {
		return 0, nil, nil, errors.New("mkv: AV1 OBU with the forbidden bit set")
	}
	typ = int(h >> 3 & 0xf)
	whole = append(whole, h)
	if h&0x04 != 0 { // extension header
		e, err := r.ReadByte()
		if err != nil {
			return 0, nil, nil, io.ErrUnexpectedEOF
		}
		whole = append(whole, e)
	}
	if h&0x02 == 0 {
		return 0, nil, nil, errors.New("mkv: AV1 OBU without a size field (not a low-overhead stream)")
	}
	var size uint64
	for i := 0; ; i++ {
		c, err := r.ReadByte()
		if err != nil {
			return 0, nil, nil, io.ErrUnexpectedEOF
		}
		whole = append(whole, c)
		size |= uint64(c&0x7f) << (7 * i)
		if c&0x80 == 0 {
			break
		}
		if i == 7 {
			return 0, nil, nil, errors.New("mkv: AV1 OBU size too long")
		}
	}
	if size > 64<<20 {
		return 0, nil, nil, fmt.Errorf("mkv: AV1 OBU of %d bytes", size)
	}
	start := len(whole)
	whole = append(whole, make([]byte, size)...)
	if _, err := io.ReadFull(r, whole[start:]); err != nil {
		return 0, nil, nil, io.ErrUnexpectedEOF
	}
	return typ, whole, whole[start:], nil
}

// nextTU returns the next temporal unit, without its temporal delimiter,
// and whether it is a keyframe (a key frame that is shown).
func (a *av1State) nextTU() (tu []byte, key bool, err error) {
	first := true
	frameSeen := false
	for {
		var typ int
		var whole, payload []byte
		if a.held != nil {
			whole, a.held = a.held, nil
			typ = int(whole[0] >> 3 & 0xf)
			payload = obuPayload(whole)
		} else {
			if a.eof {
				if len(tu) == 0 {
					return nil, false, io.EOF
				}
				return tu, key, nil
			}
			typ, whole, payload, err = readOBU(a.r)
			if errors.Is(err, io.EOF) {
				a.eof = true
				continue
			}
			if err != nil {
				return nil, false, err
			}
		}
		if typ == obuTemporalDelimiter {
			if !first && len(tu) > 0 {
				a.held = whole
				return tu, key, nil
			}
			first = false
			continue
		}
		first = false
		switch typ {
		case obuSequenceHeader:
			s, err := parseAV1Seq(payload)
			if err != nil {
				return nil, false, err
			}
			s.raw = append([]byte(nil), whole...)
			if a.seq == nil {
				a.seq = s
			}
		case obuFrame, obuFrameHeader:
			if !frameSeen && a.seq != nil {
				key = a.seq.shownKeyFrame(payload)
				frameSeen = true
			}
		}
		tu = append(tu, whole...)
	}
}

// obuPayload is the payload of a whole OBU.
func obuPayload(whole []byte) []byte {
	i := 1
	if whole[0]&0x04 != 0 {
		i++
	}
	for i < len(whole) && whole[i]&0x80 != 0 {
		i++
	}
	return whole[min(i+1, len(whole)):]
}

// shownKeyFrame reads the start of an uncompressed frame header.
func (s *av1Seq) shownKeyFrame(b []byte) bool {
	if s.reduced {
		return true // a still picture: always a shown key frame
	}
	r := &bitReader{b: b}
	if r.flag() { // show_existing_frame
		return false
	}
	frameType := r.u(2)
	show := r.flag()
	return !r.bad && frameType == 0 && show
}

// parseAV1Seq reads a sequence header OBU's payload (AV1 spec 5.5).
func parseAV1Seq(b []byte) (*av1Seq, error) {
	r := &bitReader{b: b}
	s := &av1Seq{}
	s.profile = int(r.u(3))
	r.u(1) // still_picture
	s.reduced = r.flag()
	if s.reduced {
		s.level = int(r.u(5))
	} else {
		timing := r.flag()
		decoderModel := false
		bufferDelayLen := 0
		if timing {
			r.u(32)       // num_units_in_display_tick
			r.u(32)       // time_scale
			if r.flag() { // equal_picture_interval
				r.ue() // num_ticks_per_picture_minus_1 (uvlc)
			}
			decoderModel = r.flag()
			if decoderModel {
				bufferDelayLen = int(r.u(5)) + 1
				r.u(32) // num_units_in_decoding_tick
				r.u(5)  // buffer_removal_time_length_minus_1
				r.u(5)  // frame_presentation_time_length_minus_1
			}
		}
		initialDelay := r.flag()
		ops := int(r.u(5)) + 1
		for i := 0; i < ops; i++ {
			r.u(12) // operating_point_idc
			level := int(r.u(5))
			tier := 0
			if level > 7 {
				tier = int(r.u(1))
			}
			if i == 0 {
				s.level, s.tier = level, tier
			}
			if decoderModel && r.flag() {
				r.u(bufferDelayLen) // decoder_buffer_delay
				r.u(bufferDelayLen) // encoder_buffer_delay
				r.u(1)              // low_delay_mode_flag
			}
			if initialDelay && r.flag() {
				r.u(4)
			}
		}
	}
	wBits, hBits := int(r.u(4))+1, int(r.u(4))+1
	s.width = int(r.u(wBits)) + 1
	s.height = int(r.u(hBits)) + 1
	if !s.reduced {
		s.frameIDs = r.flag()
		if s.frameIDs {
			r.u(4)
			r.u(3)
		}
	}
	r.u(1) // use_128x128_superblock
	r.u(1) // enable_filter_intra
	r.u(1) // enable_intra_edge_filter
	if !s.reduced {
		r.u(1) // enable_interintra_compound
		r.u(1) // enable_masked_compound
		r.u(1) // enable_warped_motion
		r.u(1) // enable_dual_filter
		orderHint := r.flag()
		if orderHint {
			r.u(1) // enable_jnt_comp
			r.u(1) // enable_ref_frame_mvs
		}
		forceScreen := 2
		if !r.flag() { // seq_choose_screen_content_tools
			forceScreen = int(r.u(1))
		}
		if forceScreen > 0 && !r.flag() { // seq_choose_integer_mv
			r.u(1)
		}
		if orderHint {
			r.u(3)
		}
	}
	r.u(1) // enable_superres
	r.u(1) // enable_cdef
	r.u(1) // enable_restoration
	// color_config
	s.highBitDepth = r.flag()
	if s.profile == 2 && s.highBitDepth {
		s.twelveBit = r.flag()
	}
	if s.profile != 1 {
		s.mono = r.flag()
	}
	cp, tc, mc := 2, 2, 2
	if r.flag() { // color_description_present_flag
		cp, tc, mc = int(r.u(8)), int(r.u(8)), int(r.u(8))
	}
	switch {
	case s.mono:
		r.u(1)
		s.subX, s.subY = true, true
	case cp == 1 && tc == 13 && mc == 0:
		// sRGB: full range 4:4:4, nothing more coded.
	default:
		r.u(1) // color_range
		switch s.profile {
		case 0:
			s.subX, s.subY = true, true
		case 1:
		default:
			if s.twelveBit {
				s.subX = r.flag()
				if s.subX {
					s.subY = r.flag()
				}
			} else {
				s.subX = true
			}
		}
		if s.subX && s.subY {
			s.chromaPosition = int(r.u(2))
		}
	}
	if r.bad {
		return nil, errors.New("mkv: truncated AV1 sequence header")
	}
	return s, nil
}

// av1C is the codec private data: the AV1CodecConfigurationRecord and the
// sequence header OBU.
func (s *av1Seq) av1C() []byte {
	b2 := func(v bool) byte {
		if v {
			return 1
		}
		return 0
	}
	c := []byte{
		0x81, // marker, version 1
		byte(s.profile)<<5 | byte(s.level&0x1f),
		byte(s.tier)<<7 | b2(s.highBitDepth)<<6 | b2(s.twelveBit)<<5 | b2(s.mono)<<4 |
			b2(s.subX)<<3 | b2(s.subY)<<2 | byte(s.chromaPosition&3),
		0, // no initial presentation delay
	}
	return append(c, s.raw...)
}

// primeAV1 reads the first temporal unit, which must carry the sequence
// header the track's codec private data is made from.
func (v *VideoSource) primeAV1(stereoMode int) error {
	v.av1 = &av1State{r: v.r}
	tu, key, err := v.av1.nextTU()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("mkv: no AV1 sequence header in the video stream")
		}
		return err
	}
	s := v.av1.seq
	if s == nil {
		return errors.New("mkv: the AV1 stream does not start with a sequence header")
	}
	v.av1.first = &Frame{Keyframe: key, Data: tu}
	v.track = Track{Type: TypeVideo, CodecID: "V_AV1", CodecPrivate: s.av1C(), Width: s.width, Height: s.height,
		StereoMode: stereoMode, DefaultDuration: v.frameDur, Default: true}
	return nil
}

// nextAV1 returns the next temporal unit as a frame.
func (v *VideoSource) nextAV1() (Frame, error) {
	var f Frame
	if v.av1.first != nil {
		f, v.av1.first = *v.av1.first, nil
	} else {
		tu, key, err := v.av1.nextTU()
		if err != nil {
			return Frame{}, err
		}
		f = Frame{Keyframe: key, Data: tu}
	}
	f.PTS = v.at(v.decoded)
	f.Order = f.PTS
	v.decoded++
	return f, nil
}
