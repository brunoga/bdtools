package hevc

import "sync"

// planeMargin is how far the fractional planes extend past the picture,
// in samples: the motion search's reach.
const planeMargin = 128

// planeBand is the plane rows built at a time.
const planeBand = 64

// refPlanes are a reference picture's luma interpolated at the sixteen
// quarter-sample phases, with a margin, as the uni-directional
// prediction would make them (8.5.3.3): the motion search's sub-sample
// positions become reads. Built a band of rows at a time, when a search
// first reaches it, from the reference's rows as they become final.
type refPlanes struct {
	once   sync.Once
	pic    *picture
	s      *sps
	planes *[16][]uint16 // by phase: (y&3)<<2 | x&3
	stride int
	bands  []sync.Once
}

// planePool keeps plane sets for reuse: they are large.
var planePool sync.Pool

// init readies the planes of pic; idempotent.
func (r *refPlanes) init(pic *picture, s *sps) *refPlanes {
	r.once.Do(func() {
		m := planeMargin
		w, h := s.width+2*m, s.height+2*m
		r.pic, r.s, r.stride = pic, s, w
		if p, ok := planePool.Get().(*[16][]uint16); ok && len(p[0]) == w*h {
			r.planes = p
		} else {
			r.planes = new([16][]uint16)
			for i := range r.planes {
				r.planes[i] = make([]uint16, w*h)
			}
		}
		r.bands = make([]sync.Once, (h+planeBand-1)/planeBand)
	})
	return r
}

// release gives the planes back for reuse.
func (r *refPlanes) release() {
	if r.planes != nil {
		planePool.Put(r.planes)
		r.planes = nil
	}
}

// band builds plane band b (rows [b*planeBand, (b+1)*planeBand) of every
// phase) once.
func (r *refPlanes) band(b int) {
	r.bands[b].Do(func() { r.buildBand(b) })
}

// bandScratch is a band build's working memory.
type bandScratch struct {
	src []uint16   // the source rows, edges repeated, 3 samples more around
	hor [3][]int16 // the source filtered horizontally at x phases 1-3
	tmp []int16    // one phase's 14-bit prediction
}

var bandScratchPool sync.Pool

// buildBand interpolates band b: the horizontal filter once per x phase,
// shared by the four y phases (as interpolate's two-dimensional case
// does it per block), every phase rounded as uni-directional prediction.
func (r *refPlanes) buildBand(b int) {
	s, m, w := r.s, planeMargin, r.stride
	h := s.height + 2*m
	ty := b * planeBand
	bh := min(planeBand, h-ty)
	depth := s.bitDepth
	maxV := 1<<depth - 1
	shift1 := 14 - depth
	py0 := ty - m // the band's first row in the picture
	r.pic.waitLines(py0-3, py0+bh+3)
	sw, sh := w+7, bh+7
	sc, _ := bandScratchPool.Get().(*bandScratch)
	if sc == nil || len(sc.src) < sw*sh {
		sc = &bandScratch{src: make([]uint16, sw*(planeBand+7))}
		for i := range sc.hor {
			sc.hor[i] = make([]int16, w*(planeBand+7))
		}
		sc.tmp = make([]int16, w*planeBand)
	}
	defer bandScratchPool.Put(sc)
	pic := r.pic
	for y := range sh {
		row := pic.y[clip3(0, s.height-1, py0-3+y)*pic.strideY:]
		d := sc.src[y*sw : y*sw+sw]
		x := 0
		for ; x < sw && -m-3+x < 0; x++ {
			d[x] = row[0]
		}
		for ; x < sw && -m-3+x < s.width; x++ {
			d[x] = row[-m-3+x]
		}
		for ; x < sw; x++ {
			d[x] = row[s.width-1]
		}
	}
	for xf := 1; xf <= 3; xf++ {
		hFilter(sc.hor[xf-1], w, w, sh, sc.src, sw, &lumaPairs[xf], 4, uint(depth-8))
	}
	put := func(phase int, a []int16) {
		putBlock(r.planes[phase][ty*w:], w, a, a, w, w, bh, 1, 0, 1<<(shift1-1), uint(shift1), 0, maxV)
	}
	for y := range bh {
		copy(r.planes[0][(ty+y)*w:(ty+y)*w+w], sc.src[(y+3)*sw+3:])
	}
	tmp := sc.tmp[:w*bh]
	for yf := range 4 {
		for xf := range 4 {
			phase := yf<<2 | xf
			switch {
			case phase == 0:
			case yf == 0:
				put(phase, sc.hor[xf-1][3*w:])
			case xf == 0:
				vFilter(tmp, w, w, bh, sc.src[3:], sw, &lumaPairs[yf], 4, uint(depth-8))
				put(phase, tmp)
			default:
				vFilter16(tmp, w, w, bh, sc.hor[xf-1], w, &lumaPairs[yf], 4)
				put(phase, tmp)
			}
		}
	}
}

// buildBandInterp is the band build by interpolate, block by block (the
// reference for buildBand).
func (r *refPlanes) buildBandInterp(b int) {
	s, m, w := r.s, planeMargin, r.stride
	h := s.height + 2*m
	depth := s.bitDepth
	maxV := 1<<depth - 1
	shift1 := 14 - depth
	sd := &sliceDec{s: s}
	tmp := sd.pred.l[0][:]
	ty := b * planeBand
	bh := min(planeBand, h-ty)
	for phase := range 16 {
		pl := r.planes[phase]
		v := mv{int16(phase & 3), int16(phase >> 2)}
		for tx := 0; tx < w; tx += 64 {
			bw := min(64, w-tx)
			sd.interpolate(r.pic, 0, tx-m, ty-m, bw, bh, v, tmp[:bw*bh], depth)
			putBlock(pl[ty*w+tx:], w, tmp[:bw*bh], tmp[:bw*bh], bw, bw, bh, 1, 0, 1<<(shift1-1), uint(shift1), 0, maxV)
		}
	}
}

// block returns the plane and the offset of the w x h block at (x, y)
// moved by v, building the bands it covers, or false when it reaches past
// the margin.
func (r *refPlanes) block(x, y, w, h int, v mv) ([]uint16, int, bool) {
	s, m := r.s, planeMargin
	px, py := x+int(v.x>>2)+m, y+int(v.y>>2)+m
	if px < 0 || py < 0 || px+w > s.width+2*m || py+h > s.height+2*m {
		return nil, 0, false
	}
	for b := py / planeBand; b <= (py+h-1)/planeBand; b++ {
		r.band(b)
	}
	return r.planes[int(v.y&3)<<2|int(v.x&3)], py*r.stride + px, true
}
