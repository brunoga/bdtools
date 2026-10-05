package mvc

import "errors"

// SPSInfo is what a stream listing needs to know about a video stream.
type SPSInfo struct {
	ProfileIdc, LevelIdc int
	Width, Height        int // cropped luma size
	FrameMbsOnly         bool
	FPSNum, FPSDen       int // 0 when the VUI carries no timing
}

// Profile names the profile_idc the way a listing does.
func (s SPSInfo) Profile() string {
	switch s.ProfileIdc {
	case 66:
		return "Baseline"
	case 77:
		return "Main"
	case 88:
		return "Extended"
	case 100:
		return "High"
	case 110:
		return "High 10"
	case 118:
		return "Multiview High"
	case 122:
		return "High 4:2:2"
	case 128:
		return "Stereo High"
	case 244:
		return "High 4:4:4"
	}
	return "Unknown"
}

// ParseSPSInfo reads a sequence parameter set NAL unit (with or without
// its start code; the first byte after the start code is the NAL header).
func ParseSPSInfo(nal []byte) (SPSInfo, error) {
	for len(nal) > 0 && nal[0] == 0 {
		nal = nal[1:]
	}
	if len(nal) < 2 || nal[0] != 1 {
		return SPSInfo{}, errors.New("mvc: not a NAL unit")
	}
	nal = nal[1:] // past the start code's 01
	typ := nal[0] & 0x1f
	if typ != nalSPS && typ != nalSubsetSPS {
		return SPSInfo{}, errors.New("mvc: not a sequence parameter set")
	}
	rbsp := unescapeRBSP(nil, nal[1:])
	var s *sps
	var err error
	if typ == nalSPS {
		s, err = parseSPS(rbsp)
	} else {
		s, err = parseSubsetSPS(rbsp)
	}
	if err != nil {
		return SPSInfo{}, err
	}
	h := s.heightMapUnits * 16
	if !s.frameMbsOnly {
		h *= 2
	}
	info := SPSInfo{
		ProfileIdc:   s.profileIdc,
		LevelIdc:     s.levelIdc,
		Width:        s.widthMbs*16 - s.cropLeft - s.cropRight,
		Height:       h - s.cropTop - s.cropBottom,
		FrameMbsOnly: s.frameMbsOnly,
	}
	if s.numUnitsInTick > 0 && s.timeScale > 0 {
		info.FPSNum, info.FPSDen = int(s.timeScale), 2*int(s.numUnitsInTick)
		for a, b := info.FPSNum, info.FPSDen; b != 0; {
			a, b = b, a%b
			if b == 0 {
				info.FPSNum, info.FPSDen = info.FPSNum/a, info.FPSDen/a
			}
		}
	}
	return info, nil
}
