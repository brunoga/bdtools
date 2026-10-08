// Package hdr reads and writes the metadata HDR10 and HDR10+ video carries
// beside its pictures: the mastering display colour volume and content
// light level (static, SMPTE ST 2086 and CTA-861.3), and HDR10+'s dynamic
// metadata (SMPTE ST 2094-40, as an ITU-T T.35 message). In HEVC they are
// SEI messages; in AV1, metadata OBUs.
package hdr

import (
	"bytes"
	"encoding/binary"
)

// Mastering is the mastering display's colour volume, in the units the
// SEI uses: chromaticities in 0.00002, luminance in 0.0001 cd/m².
type Mastering struct {
	// Primaries are green, blue and red (the SEI's order), each x then y.
	Primaries  [3][2]uint16
	WhitePoint [2]uint16
	MaxLuma    uint32
	MinLuma    uint32
}

// LightLevel is the content light level: the brightest pixel (MaxCLL) and
// the brightest frame average (MaxFALL), in cd/m².
type LightLevel struct {
	MaxCLL, MaxFALL uint16
}

// Static is a stream's static HDR metadata; a nil field is absent.
type Static struct {
	Mastering *Mastering
	Light     *LightLevel
}

// Empty reports whether there is none.
func (s Static) Empty() bool { return s.Mastering == nil && s.Light == nil }

// SEI payload types.
const (
	seiT35       = 4
	seiMastering = 137
	seiLight     = 144
)

// hdr10PlusPrefix starts an ITU-T T.35 message carrying HDR10+ metadata:
// the United States' country code, Samsung's provider code, the oriented
// code 1 and application identifier 4.
var hdr10PlusPrefix = []byte{0xb5, 0x00, 0x3c, 0x00, 0x01, 0x04}

// IsHDR10Plus reports whether a T.35 payload is HDR10+ metadata.
func IsHDR10Plus(t35 []byte) bool { return bytes.HasPrefix(t35, hdr10PlusPrefix) }

// HEVCMetadata is what an access unit's SEI carries.
type HEVCMetadata struct {
	Static Static
	// HDR10Plus is the T.35 payload of HDR10+ metadata for this picture,
	// nil when there is none.
	HDR10Plus []byte
}

// ParseHEVC reads the HDR metadata from an HEVC access unit's prefix SEI
// NAL units (Annex B).
func ParseHEVC(au []byte) HEVCMetadata {
	var md HEVCMetadata
	for _, nal := range annexB(au) {
		if len(nal) < 3 || nal[0]>>1&0x3f != 39 { // prefix SEI
			continue
		}
		messages(unescape(nal[2:]), func(typ int, p []byte) {
			switch typ {
			case seiMastering:
				if m, ok := parseMastering(p); ok {
					md.Static.Mastering = &m
				}
			case seiLight:
				if len(p) >= 4 {
					md.Static.Light = &LightLevel{binary.BigEndian.Uint16(p), binary.BigEndian.Uint16(p[2:])}
				}
			case seiT35:
				if IsHDR10Plus(p) {
					md.HDR10Plus = append([]byte(nil), p...)
				}
			}
		})
	}
	return md
}

func parseMastering(p []byte) (Mastering, bool) {
	if len(p) < 24 {
		return Mastering{}, false
	}
	var m Mastering
	for i := range 3 {
		m.Primaries[i] = [2]uint16{binary.BigEndian.Uint16(p[4*i:]), binary.BigEndian.Uint16(p[4*i+2:])}
	}
	m.WhitePoint = [2]uint16{binary.BigEndian.Uint16(p[12:]), binary.BigEndian.Uint16(p[14:])}
	m.MaxLuma, m.MinLuma = binary.BigEndian.Uint32(p[16:]), binary.BigEndian.Uint32(p[20:])
	return m, true
}

func (m Mastering) bytes() []byte {
	b := make([]byte, 24)
	for i, p := range m.Primaries {
		binary.BigEndian.PutUint16(b[4*i:], p[0])
		binary.BigEndian.PutUint16(b[4*i+2:], p[1])
	}
	binary.BigEndian.PutUint16(b[12:], m.WhitePoint[0])
	binary.BigEndian.PutUint16(b[14:], m.WhitePoint[1])
	binary.BigEndian.PutUint32(b[16:], m.MaxLuma)
	binary.BigEndian.PutUint32(b[20:], m.MinLuma)
	return b
}

// HEVCStaticSEI is a prefix SEI NAL unit (without start code) carrying the
// static metadata, or nil when there is none.
func HEVCStaticSEI(s Static) []byte {
	var msgs []byte
	if s.Mastering != nil {
		msgs = appendMessage(msgs, seiMastering, s.Mastering.bytes())
	}
	if s.Light != nil {
		l := make([]byte, 4)
		binary.BigEndian.PutUint16(l, s.Light.MaxCLL)
		binary.BigEndian.PutUint16(l[2:], s.Light.MaxFALL)
		msgs = appendMessage(msgs, seiLight, l)
	}
	if msgs == nil {
		return nil
	}
	return hevcSEINAL(msgs)
}

// HEVCT35SEI is a prefix SEI NAL unit carrying a T.35 payload (HDR10+).
func HEVCT35SEI(t35 []byte) []byte {
	return hevcSEINAL(appendMessage(nil, seiT35, t35))
}

func hevcSEINAL(msgs []byte) []byte {
	rbsp := append(msgs, 0x80) // rbsp_trailing_bits
	return append([]byte{39 << 1, 1}, escape(rbsp)...)
}

func appendMessage(b []byte, typ int, p []byte) []byte {
	for ; typ >= 255; typ -= 255 {
		b = append(b, 0xff)
	}
	b = append(b, byte(typ))
	n := len(p)
	for ; n >= 255; n -= 255 {
		b = append(b, 0xff)
	}
	return append(append(b, byte(n)), p...)
}

// messages calls each with every SEI message of an RBSP.
func messages(b []byte, each func(typ int, payload []byte)) {
	for len(b) > 1 { // the last byte is rbsp_trailing_bits
		typ, size := 0, 0
		for len(b) > 0 && b[0] == 0xff {
			typ += 255
			b = b[1:]
		}
		if len(b) == 0 {
			return
		}
		typ += int(b[0])
		b = b[1:]
		for len(b) > 0 && b[0] == 0xff {
			size += 255
			b = b[1:]
		}
		if len(b) == 0 {
			return
		}
		size += int(b[0])
		b = b[1:]
		if size > len(b) {
			return
		}
		each(typ, b[:size])
		b = b[size:]
	}
}

// annexB splits an Annex B stream into NAL units (without start codes).
func annexB(b []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				end := i
				if end > start && b[end-1] == 0 {
					end--
				}
				nals = append(nals, b[start:end])
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 && start < len(b) {
		nals = append(nals, b[start:])
	}
	return nals
}

// unescape removes emulation prevention bytes.
func unescape(b []byte) []byte {
	if !bytes.Contains(b, []byte{0, 0, 3}) {
		return b
	}
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// escape inserts emulation prevention bytes.
func escape(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/64)
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// AV1 metadata OBU types.
const (
	av1MetaCLL  = 1
	av1MetaMDCV = 2
	av1MetaT35  = 4
)

// AV1StaticOBUs are metadata OBUs carrying the static metadata.
func AV1StaticOBUs(s Static) []byte {
	var out []byte
	if s.Light != nil {
		p := make([]byte, 4)
		binary.BigEndian.PutUint16(p, s.Light.MaxCLL)
		binary.BigEndian.PutUint16(p[2:], s.Light.MaxFALL)
		out = append(out, av1MetadataOBU(av1MetaCLL, p)...)
	}
	if s.Mastering != nil {
		// AV1 orders the primaries red, green, blue, gives chromaticities in
		// 0.16 fixed point (the SEI in 0.00002), and luminance in 24.8 (max)
		// and 18.14 (min) fixed point (the SEI in 0.0001 cd/m²).
		m := s.Mastering
		p := make([]byte, 24)
		xy := func(v uint16) uint16 { return uint16(min(65535, (uint32(v)*65536+25000)/50000)) } //nolint:gosec // clamped
		for i, src := range []int{2, 0, 1} {
			binary.BigEndian.PutUint16(p[4*i:], xy(m.Primaries[src][0]))
			binary.BigEndian.PutUint16(p[4*i+2:], xy(m.Primaries[src][1]))
		}
		binary.BigEndian.PutUint16(p[12:], xy(m.WhitePoint[0]))
		binary.BigEndian.PutUint16(p[14:], xy(m.WhitePoint[1]))
		binary.BigEndian.PutUint32(p[16:], uint32(uint64(m.MaxLuma)*256/10000))   //nolint:gosec // in range
		binary.BigEndian.PutUint32(p[20:], uint32(uint64(m.MinLuma)*16384/10000)) //nolint:gosec // in range
		out = append(out, av1MetadataOBU(av1MetaMDCV, p)...)
	}
	return out
}

// AV1T35OBU is a metadata OBU carrying a T.35 payload (HDR10+).
func AV1T35OBU(t35 []byte) []byte { return av1MetadataOBU(av1MetaT35, t35) }

func av1MetadataOBU(typ byte, payload []byte) []byte {
	body := append([]byte{typ}, payload...) // metadata_type, a leb128 below 128
	body = append(body, 0x80)               // trailing bits
	out := []byte{5<<3 | 2}                 // OBU_METADATA, has_size_field
	n := len(body)
	for {
		c := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			out = append(out, c)
			break
		}
		out = append(out, c|0x80)
	}
	return append(out, body...)
}
