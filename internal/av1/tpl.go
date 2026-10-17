package av1

// A temporal dependency model (libaom's TPL, simplified): before a
// mini-GOP is coded, each of its frames' 16x16 blocks is costed on the
// source pictures (intra: its SAD around its mean; inter: the SAD of an
// integer motion search in each reference's source), then, from the last
// frame coded back to the first, each block hands the information later
// frames take from it ((intra + what depends on it) times the share inter
// prediction saves) to the blocks of its reference it predicts from. A
// superblock much depended on codes finer: its q index the one whose DC
// quantizer is the frame's over the square root of beta (the frame's
// intra share over the superblock's), as libaom's delta q does.

import (
	"math"
	"sync"
)

const tplBlock = 16

// tplFrame is a frame's block statistics.
type tplFrame struct {
	job        *frameJob
	cols, rows int
	intra, dep []float64
	inter      []float64
	refDisp    []int // the reference predicted from, -1 none
	mvx, mvy   []int // its integer vector, in samples
}

// tplMaxDelta bounds a superblock's q index offset.
const tplMaxDelta = 48

// tpl sets the coded frames of a mini-GOP's steps' superblock q indexes.
func (e *Encoder) tpl(steps []step) {
	if dbgNoTPL || e.o.QIndex == 0 {
		return
	}
	w, h := e.o.Width, e.o.Height
	cols, rows := (w+tplBlock-1)/tplBlock, (h+tplBlock-1)/tplBlock
	var frames []*tplFrame
	byDisp := map[int]*tplFrame{}
	for _, s := range steps {
		if s.job == nil || s.job.key {
			continue
		}
		n := cols * rows
		tf := &tplFrame{job: s.job, cols: cols, rows: rows, intra: make([]float64, n), dep: make([]float64, n),
			inter: make([]float64, n), refDisp: make([]int, n), mvx: make([]int, n), mvy: make([]int, n)}
		frames = append(frames, tf)
		if _, dup := byDisp[s.job.disp]; !dup {
			byDisp[s.job.disp] = tf
		}
	}
	src := func(disp int) *Source { return e.recent[disp] }
	var wg sync.WaitGroup
	for _, tf := range frames {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.tplCost(tf, src)
		}()
	}
	wg.Wait()
	// propagation, last coded first
	for k := len(frames) - 1; k >= 0; k-- {
		tf := frames[k]
		for i := range tf.intra {
			ref, ok := byDisp[tf.refDisp[i]]
			if tf.refDisp[i] < 0 || !ok || tf.intra[i] <= 0 {
				continue
			}
			frac := 1 - math.Min(tf.inter[i], tf.intra[i])/tf.intra[i]
			amount := (tf.intra[i] + tf.dep[i]) * frac
			bx, by := i%cols*tplBlock+tf.mvx[i], i/cols*tplBlock+tf.mvy[i]
			// spread over the four blocks the displaced block covers
			x0, y0 := floorDiv(bx, tplBlock), floorDiv(by, tplBlock)
			fx, fy := bx-x0*tplBlock, by-y0*tplBlock
			for _, o := range [4][3]int{{0, 0, (tplBlock - fx) * (tplBlock - fy)}, {1, 0, fx * (tplBlock - fy)}, {0, 1, (tplBlock - fx) * fy}, {1, 1, fx * fy}} {
				xx, yy := x0+o[0], y0+o[1]
				if o[2] == 0 || xx < 0 || yy < 0 || xx >= cols || yy >= rows {
					continue
				}
				ref.dep[yy*cols+xx] += amount * float64(o[2]) / (tplBlock * tplBlock)
			}
		}
	}
	// the superblocks' q
	bd := (e.o.BitDepth - 8) >> 1
	for _, tf := range frames {
		j := tf.job
		var sumIntra, sumAll float64
		for i := range tf.intra {
			sumIntra += tf.intra[i]
			sumAll += tf.intra[i] + tf.dep[i]
		}
		if sumIntra <= 0 || sumAll <= sumIntra {
			continue
		}
		r0 := sumIntra / sumAll
		sbCols, sbRows := (2*((w+7)>>3)+15)>>4, (2*((h+7)>>3)+15)>>4
		qs := make([]int, sbCols*sbRows)
		any := false
		for sy := range sbRows {
			for sx := range sbCols {
				var in, all float64
				for y := sy * 4; y < min(sy*4+4, rows); y++ {
					for x := sx * 4; x < min(sx*4+4, cols); x++ {
						in += tf.intra[y*cols+x]
						all += tf.intra[y*cols+x] + tf.dep[y*cols+x]
					}
				}
				q := j.q
				if in > 0 {
					beta := r0 / (in / all)
					q = deltaQFor(j.q, beta, bd)
				}
				qs[sy*sbCols+sx] = q
				any = any || q != j.q
			}
		}
		if any {
			j.sbQ = qs
		}
	}
}

// deltaQFor is the q index whose DC quantizer is base's over sqrt(beta)
// (av1_get_deltaq_offset), within tplMaxDelta of base.
func deltaQFor(base int, beta float64, bd int) int {
	target := float64(dcQlookup[bd][base]) / math.Sqrt(beta)
	best, bestD := base, math.Inf(1)
	for q := max(base-tplMaxDelta, 1); q <= min(base+tplMaxDelta, 255); q++ {
		if d := math.Abs(float64(dcQlookup[bd][q]) - target); d < bestD {
			best, bestD = q, d
		}
	}
	return best
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// tplCost costs a frame's blocks, both ways by SATD: intra the best of
// DC, vertical, horizontal and a bilinear smooth prediction from the
// source's neighbouring samples; inter a motion search (to a quarter
// sample) in each reference's source.
func (e *Encoder) tplCost(tf *tplFrame, src func(int) *Source) {
	j := tf.job
	cur := j.src
	if j.orig != nil {
		cur = j.orig
	}
	w, h := e.o.Width, e.o.Height
	var refs []*refFrameState
	var disps []int
	seen := map[int]bool{}
	for _, r := range j.refs {
		if r == nil || seen[r.disp] {
			continue
		}
		seen[r.disp] = true
		if s := src(r.disp); s != nil {
			refs = append(refs, &refFrameState{rec: [3][]uint16{s.Y, s.Cb, s.Cr}, stride: [3]int{s.StrideY, s.StrideC, s.StrideC}, width: w, height: h})
			disps = append(disps, r.disp)
		}
	}
	var ms motionSearch
	var pred [tplBlock * tplBlock]uint16
	var above, left [tplBlock]int32
	mid := int32(1) << (e.o.BitDepth - 1)
	for by := range tf.rows {
		for bx := range tf.cols {
			i := by*tf.cols + bx
			x0, y0 := bx*tplBlock, by*tplBlock
			bw, bh := min(tplBlock, w-x0)&^3, min(tplBlock, h-y0)&^3
			tf.inter[i], tf.refDisp[i] = 1, -1
			if bw == 0 || bh == 0 {
				tf.intra[i] = 1
				continue
			}
			at := func(x, y int) int32 { return int32(cur.Y[y*cur.StrideY+x]) }
			for k := range bw {
				above[k] = mid
				if y0 > 0 {
					above[k] = at(x0+k, y0-1)
				}
			}
			for k := range bh {
				left[k] = mid
				if x0 > 0 {
					left[k] = at(x0-1, y0+k)
				}
			}
			var dc int32
			switch {
			case x0 > 0 && y0 > 0:
				var s int32
				for k := range bw {
					s += above[k]
				}
				for k := range bh {
					s += left[k]
				}
				dc = s / int32(bw+bh)
			case y0 > 0:
				var s int32
				for k := range bw {
					s += above[k]
				}
				dc = s / int32(bw)
			case x0 > 0:
				var s int32
				for k := range bh {
					s += left[k]
				}
				dc = s / int32(bh)
			default:
				dc = mid
			}
			corner := mid
			if x0 > 0 && y0 > 0 {
				corner = at(x0-1, y0-1)
			}
			blk := cur.Y[y0*cur.StrideY+x0:]
			best := int64(math.MaxInt64)
			for mode := range 4 {
				for y := range bh {
					for x := range bw {
						var v int32
						switch mode {
						case 0:
							v = dc
						case 1:
							v = above[x]
						case 2:
							v = left[y]
						default: // the plane through the corner and both edges
							v = above[x] + left[y] - corner
							v = min(max(v, 0), 2*mid-1)
						}
						pred[y*tplBlock+x] = uint16(v)
					}
				}
				best = min(best, satdBlock(blk, cur.StrideY, pred[:], tplBlock, bw, bh))
			}
			tf.intra[i] = float64(best) + 1
			tf.inter[i] = tf.intra[i]
			for k, r := range refs {
				b := meBlock{ref: r, src: cur.Y, srcStride: cur.StrideY, x: x0, y: y0, w: bw, h: bh,
					bitDepth: e.o.BitDepth, searchRange: 32, cost: func(mv) float64 { return 0 }}
				m, c := ms.search(&b, []mv{{}})
				if c+1 < tf.inter[i] {
					tf.inter[i], tf.refDisp[i] = c+1, disps[k]
					tf.mvx[i], tf.mvy[i] = int(math.Round(float64(m.col)/8)), int(math.Round(float64(m.row)/8))
				}
			}
		}
	}
}
