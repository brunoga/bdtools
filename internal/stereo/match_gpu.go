package stereo

import (
	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/mvc"
)

// MatchBothGPU is MatchBoth on the GPU, the same disparities.
func MatchBothGPU(g *nn.GPU, left, right *mvc.Frame) (l, r *Disparity, err error) {
	defer g.Release()
	lp := g.UploadPicture(left.Y, left.Cb, left.Cr, left.Width, left.Height, left.StrideY, left.StrideC)
	rp := g.UploadPicture(right.Y, right.Cb, right.Cr, right.Width, right.Height, right.StrideY, right.StrideC)
	dl, dr, vl, vr, err := g.MatchBoth(lp, rp, matchTexture)
	if err != nil {
		return nil, nil, err
	}
	w, h := left.Width/2, left.Height/2
	valid := func(v []byte) []bool {
		out := make([]bool, len(v))
		for i, b := range v {
			out[i] = b != 0
		}
		return out
	}
	return &Disparity{W: w, H: h, D: dl, Valid: valid(vl)}, &Disparity{W: w, H: h, D: dr, Valid: valid(vr)}, nil
}
