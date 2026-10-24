package nn

import "github.com/brunoga/bdtools/internal/cuda"

// SGMDisparities is the most a match searches each way (half resolution
// pixels): stereo's matchRange, which kernels.cu's SGM_R must equal.
const SGMDisparities = 64

// MatchBoth is stereo.MatchBoth on the GPU (kernels.cu's sgm_*: the same
// integers and doubles, so the same disparities) for two w x h pictures:
// at half their size, the left eye's disparity and the right's (full
// resolution pixels), and which of each are sure (tex the texture they
// need: stereo's matchTexture).
func (g *GPU) MatchBoth(l, r *Picture, tex float64) (dl, dr []float32, vl, vr []byte, err error) {
	w, h := l.w/2, l.h/2
	n := w * h
	const nd = 2*SGMDisparities + 1
	bytes := func(b int) *Tensor { return g.tensor((b + 3) / 4) }
	grid, block := each(n)
	lh, rh := bytes(n), bytes(n)
	g.launch("sgm_half", grid, block, 0, w, h, ptr(l.buf), l.strideY, ptr(lh))
	g.launch("sgm_half", grid, block, 0, w, h, ptr(r.buf), r.strideY, ptr(rh))
	lc, rc := g.tensor(n), g.tensor(n)
	g.launch("sgm_census", grid, block, 0, w, h, ptr(lh), ptr(lc))
	g.launch("sgm_census", grid, block, 0, w, h, ptr(rh), ptr(rc))
	cost := bytes(n * nd)
	cg, cb := each(n * nd)
	g.launch("sgm_cost", cg, cb, 0, w, h, ptr(lc), ptr(rc), ptr(cost))
	sum := bytes(2 * n * nd)
	fg, fb := each((2*n*nd + 3) / 4)
	g.launch("fill", fg, fb, 0, int64((2*n*nd+3)/4), ptr(sum), float32(0))
	const threads = 160 // kernels.cu's SGM_THREADS
	g.launch("sgm_rows", cuda.Dim{X: h}, cuda.Dim{X: threads}, 0, w, h, ptr(cost), ptr(sum))
	for _, down := range []bool{true, false} {
		prev, cur := bytes(2*3*w*nd), bytes(2*3*w*nd)
		for k := range h {
			y := k
			if !down {
				y = h - 1 - k
			}
			first := 0
			if k == 0 {
				first = 1
			}
			g.launch("sgm_down", cuda.Dim{X: w}, cuda.Dim{X: threads}, 0, w, h, y, first, ptr(cost), ptr(prev), ptr(cur), ptr(sum))
			prev, cur = cur, prev
		}
	}
	pick := func(right int) (disp, best, unique *Tensor) {
		disp, best, unique = g.tensor(n), g.tensor(n), bytes(n)
		g.launch("sgm_pick", grid, block, 0, w, h, right, ptr(sum), ptr(disp), ptr(best), ptr(unique))
		return disp, best, unique
	}
	tdl, bl, ul := pick(0)
	tdr, br, ur := pick(1)
	tvl, tvr := bytes(n), bytes(n)
	g.launch("sgm_valid", grid, block, 0, w, h, -1, ptr(bl), ptr(ul), ptr(br), ptr(lh), tex, ptr(tvl))
	g.launch("sgm_valid", grid, block, 0, w, h, 1, ptr(br), ptr(ur), ptr(bl), ptr(rh), tex, ptr(tvr))
	vl, vr = make([]byte, n), make([]byte, n)
	if err = g.DownloadBytes(vl, tvl); err != nil {
		return nil, nil, nil, nil, err
	}
	if err = g.DownloadBytes(vr, tvr); err != nil {
		return nil, nil, nil, nil, err
	}
	a, err := g.Download(tdl)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	b, err := g.Download(tdr)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return a.Data, b.Data, vl, vr, nil
}
