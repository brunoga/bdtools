//go:build !amd64 || purego

package av1

type cdefParams struct{}

func cdefSearch8(win []int32, w, vw, vh int, src []uint16, stride, dir int,
	priT *[16]int32, priShift *[16]uint, priTaps *[16][2]int32, secT *[4]int32, secShift *[4]uint, e *[64]int64, p *cdefParams) bool {
	return false
}
