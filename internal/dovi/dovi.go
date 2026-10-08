// Package dovi handles Dolby Vision's carriage: the configuration record a
// container gives a Dolby Vision track (dvcC or dvvC), and the single-track
// form of a dual-layer stream.
//
// A UHD Blu-ray carries profile 7: an HEVC base layer (PID 0x1011, HDR10 on
// its own), and an enhancement layer (PID 0x1015) whose access units hold
// the enhancement picture (the full enhancement layer, FEL, or the minimal
// one, MEL) and the reference processing unit (RPU, NAL unit type 62) that
// says how to combine the two. Matroska and MP4 carry them in one HEVC
// track: each access unit is the base layer's NAL units, then the
// enhancement layer's, each behind a NAL unit header of type 63, then the
// RPU. Decoders that do not know Dolby Vision skip types 62 and 63 and show
// the base layer.
package dovi

import (
	"errors"
)

// NAL unit types (HEVC's unspecified range) Dolby Vision uses.
const (
	NALRPU = 62 // a reference processing unit
	NALEL  = 63 // an enhancement layer NAL unit, behind this header
)

// Config is the Dolby Vision decoder configuration record.
type Config struct {
	VersionMajor, VersionMinor int
	Profile                    int
	Level                      int
	RPU, EL, BL                bool // which parts the stream has
	// Compatibility is bl_signal_compatibility_id: what the base layer is on
	// its own (6 for a UHD Blu-ray's profile 7: HDR10; 1 for profile 8.1).
	Compatibility int
}

// Bytes is the 24-byte record (the payload of an MP4 dvcC or dvvC box, and
// of Matroska's BlockAddIDExtraData).
func (c Config) Bytes() []byte {
	b := make([]byte, 24)
	b[0], b[1] = byte(c.VersionMajor), byte(c.VersionMinor)  //nolint:gosec // small
	v := uint16(c.Profile&0x7f)<<9 | uint16(c.Level&0x3f)<<3 //nolint:gosec // masked
	if c.RPU {
		v |= 4
	}
	if c.EL {
		v |= 2
	}
	if c.BL {
		v |= 1
	}
	b[2], b[3] = byte(v>>8), byte(v)
	b[4] = byte(c.Compatibility&0xf) << 4
	return b
}

// ParseConfig reads a configuration record.
func ParseConfig(b []byte) (Config, error) {
	if len(b) < 5 {
		return Config{}, errors.New("dovi: configuration record too short")
	}
	v := uint16(b[2])<<8 | uint16(b[3])
	return Config{VersionMajor: int(b[0]), VersionMinor: int(b[1]),
		Profile: int(v >> 9), Level: int(v >> 3 & 0x3f),
		RPU: v&4 != 0, EL: v&2 != 0, BL: v&1 != 0, Compatibility: int(b[4] >> 4)}, nil
}

// FourCC is the record's box type: dvcC up to profile 7, dvvC above.
func (c Config) FourCC() string {
	if c.Profile > 7 {
		return "dvvC"
	}
	return "dvcC"
}

// levels are each Dolby Vision level's limits: the pixel rate, and the
// width.
var levels = []struct {
	pps   int64
	width int
}{
	{22118400, 1280}, {27648000, 1280}, {49766400, 1920}, {62208000, 2560},
	{124416000, 3840}, {199065600, 3840}, {248832000, 3840}, {398131200, 3840},
	{497664000, 3840}, {995328000, 3840}, {995328000, 7680}, {1990656000, 7680},
	{3981312000, 7680},
}

// Level is the lowest level that takes a picture size at a frame rate, 0
// when none does.
func Level(width, height, fpsNum, fpsDen int) int {
	if fpsDen <= 0 {
		return 0
	}
	pps := (int64(width)*int64(height)*int64(fpsNum) + int64(fpsDen) - 1) / int64(fpsDen)
	for i, l := range levels {
		if pps <= l.pps && width <= l.width {
			return i + 1
		}
	}
	return 0
}

// UHDBluRay is the configuration of a UHD Blu-ray's profile 7 stream.
func UHDBluRay(width, height, fpsNum, fpsDen int) Config {
	return Config{VersionMajor: 1, Profile: 7, Level: Level(width, height, fpsNum, fpsDen),
		RPU: true, EL: true, BL: true, Compatibility: 6}
}

// Merge appends an enhancement layer access unit (Annex B, as a disc's
// PID 0x1015 carries it) to a base layer one, in the single-track form:
// each enhancement layer NAL unit behind a type 63 header, the RPU last as
// it is. The enhancement layer's access unit delimiter is dropped: the
// base layer's starts the access unit.
func Merge(bl, el []byte) []byte {
	out := make([]byte, 0, len(bl)+len(el)+64)
	out = append(out, bl...)
	var rpu []byte
	for _, n := range nalUnits(el) {
		if len(n) < 2 {
			continue
		}
		switch n[0] >> 1 & 0x3f {
		case 35: // access unit delimiter
		case NALRPU:
			rpu = n
		default:
			out = append(out, 0, 0, 0, 1, NALEL<<1, 1)
			out = append(out, n...)
		}
	}
	if rpu != nil {
		out = append(out, 0, 0, 0, 1)
		out = append(out, rpu...)
	}
	return out
}

// HasRPU reports whether an access unit (Annex B) carries an RPU.
func HasRPU(au []byte) bool { return FindRPU(au) != nil }

// FindRPU returns an access unit's (Annex B) RPU NAL unit, without start
// code, or nil.
func FindRPU(au []byte) []byte {
	var rpu []byte
	for _, n := range nalUnits(au) {
		if len(n) > 0 && n[0]>>1&0x3f == NALRPU {
			rpu = n
		}
	}
	return rpu
}

// Profile81 is the configuration of a profile 8.1 stream: an HDR10 base
// layer and an RPU. Its level, when 0, is to be worked out.
func Profile81() Config {
	return Config{VersionMajor: 1, Profile: 8, RPU: true, BL: true, Compatibility: 1}
}

// nalUnits splits an Annex B stream into NAL units, without start codes or
// the zero bytes trailing them.
func nalUnits(b []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i+2 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		if start >= 0 {
			nals = append(nals, trimZeros(b[start:i]))
		}
		start = i + 3
		i += 2
	}
	if start >= 0 && start < len(b) {
		nals = append(nals, trimZeros(b[start:]))
	}
	return nals
}

func trimZeros(n []byte) []byte {
	for len(n) > 0 && n[len(n)-1] == 0 {
		n = n[:len(n)-1]
	}
	return n
}
