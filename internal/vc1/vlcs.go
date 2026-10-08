package vc1

import "sync"

// The variable length code tables, made once from tables.go.
var (
	vlcOnce sync.Once

	imodeVLC, norm2VLC, norm6VLC        *vlc
	ttmbVLC, ttblkVLC, subblkpatVLC     [3]*vlc
	fourMVBPVLC, cbpcyPVLC, mvDiffVLC   [4]*vlc
	intfr4MVModeVLC, intfrNon4MVModeVLC [4]*vlc
	mvdata1RefVLC, twoMVBPVLC           [4]*vlc
	acVLC, mvdata2RefVLC, icbpcyVLC     [8]*vlc
	ifMixedModeVLC, if1MVModeVLC        [8]*vlc
	dcVLC                               [2][2]*vlc // [table][chroma]
	mbIVLC                              *vlc
)

type integer interface {
	~uint8 | ~int16 | ~int32
}

func mkVLC[C integer](codes []C, lens []uint8) *vlc {
	c := make([]uint32, len(codes))
	for i, v := range codes {
		c[i] = uint32(v)
	}
	return newVLC(c, lens[:len(codes)])
}

// mkPairs makes a table of {code, length} pairs, n of them.
func mkPairs[C integer](pairs [][2]C, n int) *vlc {
	c := make([]uint32, n)
	l := make([]uint8, n)
	for i := range n {
		c[i], l[i] = uint32(pairs[i][0]), uint8(pairs[i][1])
	}
	return newVLC(c, l)
}

func initVLCs() {
	vlcOnce.Do(func() {
		imodeVLC = mkVLC(imodeCodes[:], imodeBits[:])
		norm2VLC = mkVLC(norm2Codes[:], norm2Bits[:])
		norm6VLC = mkVLC(norm6Codes[:], norm6Bits[:])
		for i := range 3 {
			ttmbVLC[i] = mkVLC(ttmbCodes[i][:], ttmbBits[i][:])
			ttblkVLC[i] = mkVLC(ttblkCodes[i][:], ttblkBits[i][:])
			subblkpatVLC[i] = mkVLC(subblkpatCodes[i][:], subblkpatBits[i][:])
		}
		for i := range 4 {
			fourMVBPVLC[i] = mkVLC(fourMVBPCodes[i][:], fourMVBPBits[i][:])
			cbpcyPVLC[i] = mkVLC(cbpcyPCodes[i][:], cbpcyPBits[i][:])
			mvDiffVLC[i] = mkVLC(mvDiffCodes[i][:], mvDiffBits[i][:])
			intfr4MVModeVLC[i] = mkVLC(intfr4MVModeCodes[i][:], intfr4MVModeBits[i][:])
			intfrNon4MVModeVLC[i] = mkVLC(intfrNon4MVModeCodes[i][:], intfrNon4MVModeBits[i][:])
			mvdata1RefVLC[i] = mkVLC(mvdata1RefCodes[i][:], mvdata1RefBits[i][:])
			twoMVBPVLC[i] = mkVLC(twoMVBPCodes[i][:], twoMVBPBits[i][:])
		}
		for i := range 8 {
			acVLC[i] = mkPairs(acTables[i][:], int(acSizes[i]))
			mvdata2RefVLC[i] = mkVLC(mvdata2RefCodes[i][:], mvdata2RefBits[i][:])
			icbpcyVLC[i] = mkVLC(icbpcyCodes[i][:], icbpcyBits[i][:])
			ifMixedModeVLC[i] = mkVLC(ifMixedModeCodes[i][:], ifMixedModeBits[i][:])
			if1MVModeVLC[i] = mkVLC(if1MVModeCodes[i][:], if1MVModeBits[i][:])
		}
		for i := range 2 {
			for j := range 2 {
				dcVLC[i][j] = mkPairs(dcTables[i][j][:], 120)
			}
		}
		mbIVLC = mkPairs(mbICBPTable[:], 64)
	})
}
