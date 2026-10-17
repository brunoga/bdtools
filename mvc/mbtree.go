package mvc

import (
	"math"
	"runtime"
	"sync"
)

// The macroblock tree (x264's): a lookahead over the frames to come, at
// half resolution, estimates how much of each macroblock later pictures
// predict from, directly or through other pictures; the quantiser of the
// macroblocks much predicted from goes down (their bits are reused), that
// of those nothing predicts from up. Each frame's intra cost and its
// motion costs against the references it will have give, per macroblock,
// the fraction of its information the motion carries from its
// references; walking the pictures in reverse coding order, each passes
// that fraction of what it holds (its own cost and what the pictures
// predicting from it took) to the blocks its vectors point at.

const (
	treeWindow = 40 // frames of lookahead
	lowPad     = 32 // lowres border, for vectors past the edges
	// treeStrength scales the offsets: x264's is 2 (5 * (1 - qcomp));
	// here, with the B pictures' fixed offsets kept, 0.9 measures best
	// (2 overdoes the reference pictures of slow dark scenes)
	treeStrength = 0.9
)

// lowFrame is a frame at half resolution with its estimates.
type lowFrame struct {
	pix    []byte
	stride int
	w, h   int // lowres size, 8 per macroblock
	intra  []int32
}

// lowMotion is a frame's motion against one reference: per macroblock
// the vector (lowres pixels) and its SATD.
type lowMotion struct {
	mv   []mv
	cost []int32
}

func newLowFrame(src *DepSource, w, h int) *lowFrame {
	lw, lh := w/2, h/2
	f := &lowFrame{stride: lw + 2*lowPad, w: lw, h: lh}
	f.pix = make([]byte, f.stride*(lh+2*lowPad))
	for y := range lh {
		s0 := src.Y[2*y*src.StrideY:]
		s1 := src.Y[(2*y+1)*src.StrideY:]
		row := f.pix[(y+lowPad)*f.stride+lowPad:]
		for x := range lw {
			row[x] = byte((int(s0[2*x]) + int(s0[2*x+1]) + int(s1[2*x]) + int(s1[2*x+1]) + 2) >> 2)
		}
	}
	// the border: the edge pixels repeated
	for y := lowPad; y < lowPad+lh; y++ {
		row := f.pix[y*f.stride:]
		l, r := row[lowPad], row[lowPad+lw-1]
		for x := range lowPad {
			row[x] = l
			row[lowPad+lw+x] = r
		}
	}
	for y := range lowPad {
		copy(f.pix[y*f.stride:(y+1)*f.stride], f.pix[lowPad*f.stride:(lowPad+1)*f.stride])
		copy(f.pix[(lowPad+lh+y)*f.stride:(lowPad+lh+y+1)*f.stride], f.pix[(lowPad+lh-1)*f.stride:(lowPad+lh)*f.stride])
	}
	f.intra = make([]int32, (lw/8)*(lh/8))
	return f
}

// off is the offset of lowres pixel (x,y).
func (f *lowFrame) off(x, y int) int { return (y+lowPad)*f.stride + x + lowPad }

// intraCosts estimates each block's intra cost: the least SATD of the DC,
// vertical and horizontal predictions from its (source) neighbours.
func (f *lowFrame) intraCosts() {
	bw := f.w / 8
	for i := range f.intra {
		bx, by := i%bw*8, i/bw*8
		o := f.off(bx, by)
		best := math.MaxInt
		var pred [64]byte
		top, left := by > 0, bx > 0
		sum, n := 0, 0
		if top {
			for x := range 8 {
				sum += int(f.pix[o-f.stride+x])
			}
			n += 8
		}
		if left {
			for y := range 8 {
				sum += int(f.pix[o+y*f.stride-1])
			}
			n += 8
		}
		dc := byte(128)
		if n > 0 {
			dc = byte((sum + n/2) / n)
		}
		for k := range pred {
			pred[k] = dc
		}
		best = min(best, satdBlock(f.pix, o, f.stride, pred[:], 0, 8, 8, 8))
		if top {
			for y := range 8 {
				copy(pred[y*8:y*8+8], f.pix[o-f.stride:o-f.stride+8])
			}
			best = min(best, satdBlock(f.pix, o, f.stride, pred[:], 0, 8, 8, 8))
		}
		if left {
			for y := range 8 {
				v := f.pix[o+y*f.stride-1]
				for x := range 8 {
					pred[y*8+x] = v
				}
			}
			best = min(best, satdBlock(f.pix, o, f.stride, pred[:], 0, 8, 8, 8))
		}
		f.intra[i] = int32(best)
	}
}

// motionRow searches row by of f's blocks in ref: a pattern search
// (steps of 4, 2, 1) from the best of the zero vector and the left
// block's, by SAD, then the SATD of where it ends.
func motionRow(f, ref *lowFrame, m *lowMotion, by int) {
	bw := f.w / 8
	const lim = lowPad - 8
	var left mv
	for bx := range bw {
		x0, y0 := bx*8, by*8
		o := f.off(x0, y0)
		sad := func(v mv) int {
			return sadBlock(f.pix, o, f.stride, ref.pix, ref.off(x0+int(v.x), y0+int(v.y)), ref.stride, 8, 8)
		}
		ok := func(v mv) bool {
			x, y := x0+int(v.x), y0+int(v.y)
			return x >= -lim && y >= -lim && x+8 <= ref.w+lim && y+8 <= ref.h+lim
		}
		best, bc := mv{}, sad(mv{})
		if left != (mv{}) && ok(left) {
			if c := sad(left); c < bc {
				best, bc = left, c
			}
		}
		for step := int16(4); step >= 1; step >>= 1 {
			for range 8 {
				moved := false
				for _, d := range [4]mv{{step, 0}, {-step, 0}, {0, step}, {0, -step}} {
					v := mv{best.x + d.x, best.y + d.y}
					if !ok(v) {
						continue
					}
					if c := sad(v); c < bc {
						best, bc, moved = v, c, true
					}
				}
				if !moved {
					break
				}
			}
		}
		i := by*bw + bx
		m.mv[i] = best
		m.cost[i] = int32(satdBlock(f.pix, o, f.stride, ref.pix, ref.off(x0+int(best.x), y0+int(best.y)), ref.stride, 8, 8))
		left = best
	}
}

// treePic is a picture of the lookahead window as it will be coded: its
// references (window positions; -1 none or already coded).
type treePic struct {
	pos        int
	ref0, ref1 int
}

// planWindow lays the window's n frames out as queueGroup will code
// them, in coding order.
func (e *BaseEncoder) planWindow(n int) []treePic {
	var out []treePic
	since, pos, anchor := e.sinceIDR, 0, -1
	win := e.queue[:n]
	for pos < n {
		if since == 0 || since >= e.o.Keyint || win[pos].cut {
			out = append(out, treePic{pos, -1, -1})
			anchor, since = pos, 1
			pos++
			continue
		}
		g := e.groupSize(win[pos:], since)
		p := pos + g - 1
		out = append(out, treePic{p, anchor, -1})
		bs := g - 1
		if e.pyramid() && bs >= 2 {
			mid := pos + bs/2
			out = append(out, treePic{mid, anchor, p})
			for b := pos; b < p; b++ {
				switch {
				case b < mid:
					out = append(out, treePic{b, anchor, mid})
				case b > mid:
					out = append(out, treePic{b, mid, p})
				}
			}
		} else {
			for b := pos; b < p; b++ {
				out = append(out, treePic{b, anchor, p})
			}
		}
		since += g
		anchor, pos = p, pos+g
	}
	return out
}

// treeState is the lookahead's cache: each frame's lowres picture (on
// its baseFrame) and the motion between frames, by display indices.
type treeState struct {
	motion   map[[2]int]*lowMotion
	last     *baseFrame // the latest frame analysed
	sinceKey int        // frames from the last key frame (keyint's or a cut's) to it
}

// analyse makes a frame's lowres picture and intra costs as it comes
// in, its motion against the frame before, and from the two whether it
// starts a new scene (x264's test: the motion saves little over intra,
// by a margin that grows with the distance from the last key frame).
func (e *BaseEncoder) analyse(f *baseFrame) {
	if e.tree.motion == nil {
		e.tree.motion = map[[2]int]*lowMotion{}
	}
	f.low = newLowFrame(f.src, e.w, e.h)
	f.low.intraCosts()
	prev := e.tree.last
	e.tree.last = f
	e.tree.sinceKey++
	if prev == nil || e.tree.sinceKey >= e.o.Keyint {
		// the first frame, or one keyint brings: a key frame anyway
		e.tree.sinceKey = 0
		return
	}
	m := e.motionPairs([][2]*baseFrame{{f, prev}})[0]
	var icost, pcost int64
	for i, c := range f.low.intra {
		icost += int64(c)
		pcost += int64(min(c, m.cost[i]))
	}
	keyMin := min(e.o.Keyint/10, 24)
	const threshMax = 0.4
	threshMin := threshMax / 4
	d := e.tree.sinceKey
	var bias float64
	switch {
	case d <= keyMin/4:
		bias = threshMin / 4
	case d <= keyMin:
		bias = threshMin * float64(d) / float64(keyMin)
	default:
		bias = threshMin + (threshMax-threshMin)*float64(d-keyMin)/float64(max(e.o.Keyint-keyMin, 1))
	}
	if float64(pcost) >= (1-bias)*float64(icost) {
		f.cut = true
		e.tree.sinceKey = 0
	}
}

// motionPairs gives the motion of each pair's first frame against its
// second, searching (row by row on every core) the pairs not cached.
func (e *BaseEncoder) motionPairs(pairs [][2]*baseFrame) []*lowMotion {
	type task struct {
		f, r *lowFrame
		m    *lowMotion
		row  int
	}
	var tasks []task
	out := make([]*lowMotion, len(pairs))
	for k, p := range pairs {
		key := [2]int{p[0].index, p[1].index}
		if m := e.tree.motion[key]; m != nil {
			out[k] = m
			continue
		}
		n := len(p[0].low.intra)
		m := &lowMotion{mv: make([]mv, n), cost: make([]int32, n)}
		e.tree.motion[key] = m
		out[k] = m
		for row := range p[0].low.h / 8 {
			tasks = append(tasks, task{p[0].low, p[1].low, m, row})
		}
	}
	if len(tasks) == 0 {
		return out
	}
	var wg sync.WaitGroup
	ch := make(chan task)
	for range min(runtime.GOMAXPROCS(0), len(tasks)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range ch {
				motionRow(t.f, t.r, t.m, t.row)
			}
		}()
	}
	for _, t := range tasks {
		ch <- t
	}
	close(ch)
	wg.Wait()
	return out
}

// buildTree sets the macroblock tree's quantiser offsets of the frames
// queued (the first of them about to be coded).
func (e *BaseEncoder) buildTree() {
	win := e.queue[:min(len(e.queue), treeWindow)]
	plan := e.planWindow(len(win))
	// the motion the plan needs
	var pairs [][2]*baseFrame
	for _, tp := range plan {
		if tp.ref0 >= 0 {
			pairs = append(pairs, [2]*baseFrame{win[tp.pos], win[tp.ref0]})
		}
		if tp.ref1 >= 0 {
			pairs = append(pairs, [2]*baseFrame{win[tp.pos], win[tp.ref1]})
		}
	}
	e.motionPairs(pairs)
	// propagation, in reverse coding order
	n := len(win[0].low.intra)
	bw := win[0].low.w / 8
	bh := n / bw
	prop := make([][]float32, len(win))
	for i := range prop {
		prop[i] = make([]float32, n)
	}
	give := func(dst []float32, i int, v mv, amount float32) {
		// the block the vector points at, spread over the (up to four)
		// blocks it overlaps by area
		x := (i%bw)*8 + int(v.x)
		y := (i/bw)*8 + int(v.y)
		bx, by := x>>3, y>>3
		fx, fy := float32(x&7), float32(y&7)
		for _, c := range [4]struct {
			dx, dy int
			w      float32
		}{{0, 0, (8 - fx) * (8 - fy)}, {1, 0, fx * (8 - fy)}, {0, 1, (8 - fx) * fy}, {1, 1, fx * fy}} {
			tx, ty := bx+c.dx, by+c.dy
			if c.w == 0 || tx < 0 || ty < 0 || tx >= bw || ty >= bh {
				continue
			}
			dst[ty*bw+tx] += amount * c.w / 64
		}
	}
	for k := len(plan) - 1; k >= 0; k-- {
		tp := plan[k]
		if tp.ref0 < 0 && tp.ref1 < 0 {
			continue
		}
		f := win[tp.pos].low
		var m0, m1 *lowMotion
		if tp.ref0 >= 0 {
			m0 = e.tree.motion[[2]int{win[tp.pos].index, win[tp.ref0].index}]
		}
		if tp.ref1 >= 0 {
			m1 = e.tree.motion[[2]int{win[tp.pos].index, win[tp.ref1].index}]
		}
		for i := range n {
			intra := float32(f.intra[i])
			if intra <= 0 {
				continue
			}
			inter, use := float32(math.MaxFloat32), 0
			if m0 != nil {
				inter, use = float32(m0.cost[i]), 1
			}
			if m1 != nil && float32(m1.cost[i]) < inter {
				inter, use = float32(m1.cost[i]), 2
			}
			if m0 != nil && m1 != nil {
				x0, y0 := (i%bw)*8, (i/bw)*8
				a, b := m0.mv[i], m1.mv[i]
				r0, r1 := win[tp.ref0].low, win[tp.ref1].low
				bi := satdAvgBlock(f.pix, f.off(x0, y0), f.stride, r0.pix, r0.off(x0+int(a.x), y0+int(a.y)), r0.stride, r1.pix, r1.off(x0+int(b.x), y0+int(b.y)), r1.stride, 8, 8)
				if float32(bi) < inter {
					inter, use = float32(bi), 3
				}
			}
			if inter >= intra {
				continue
			}
			amount := (intra + prop[tp.pos][i]) * (intra - inter) / intra
			switch use {
			case 1:
				give(prop[tp.ref0], i, m0.mv[i], amount)
			case 2:
				give(prop[tp.ref1], i, m1.mv[i], amount)
			case 3:
				give(prop[tp.ref0], i, m0.mv[i], amount/2)
				give(prop[tp.ref1], i, m1.mv[i], amount/2)
			}
		}
	}
	for p, f := range win {
		if f.treeQP == nil {
			f.treeQP = make([]float32, n)
		}
		for i := range n {
			intra := float32(f.low.intra[i])
			if intra <= 0 {
				f.treeQP[i] = 0
				continue
			}
			f.treeQP[i] = -treeStrength * float32(math.Log2(float64((intra+prop[p][i])/intra)))
		}
	}
	// the frames keep their motion, for the coders' searches
	for k, m := range e.tree.motion {
		for _, f := range win {
			if f.index == k[0] {
				if f.lowMV == nil {
					f.lowMV = map[int]*lowMotion{}
				}
				f.lowMV[k[1]] = m
			}
		}
	}
	// forget the motion of frames gone from the window
	first := win[0].index
	for k := range e.tree.motion {
		if k[0] < first || k[1] < first {
			delete(e.tree.motion, k)
		}
	}
}
