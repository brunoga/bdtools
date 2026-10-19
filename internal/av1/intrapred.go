package av1

// Intra prediction (spec 7.11.2), exactly as a decoder predicts: from the
// reconstructed samples above and to the left of a transform block (the
// edges, extended as the spec extends them where they are missing), by
// one of the modes: DC, the directional modes (with their angle deltas,
// the edge filtered and upsampled when enable_intra_edge_filter is on),
// the smooth modes, Paeth, and filter intra's recursive filters.

// edgeOff is where index 0 of an edge is in its buffer (the spec indexes
// edges from -2).
const edgeOff = 16

// intraEdges are a transform block's edges, the spec's AboveRow and
// LeftCol.
type intraEdges struct {
	above, left [edgeOff + 2*128 + 32]int32
}

// intraBlock is what a prediction needs to know of its transform block.
type intraBlock struct {
	plane                     int
	x, y                      int // its top left sample
	log2W, log2H              int
	haveLeft, haveAbove       bool
	haveAboveRight, haveBelow bool // above right, below left
	maxX, maxY                int  // the last sample of the plane's mi grid
	mode                      int
	angleDelta                int
	filterIntra               int  // its mode, or -1
	edgeFilter                bool // enable_intra_edge_filter
	smoothNeighbour           bool // the filter type: a neighbour is smooth
	bitDepth                  int
}

// isDirectional reports whether a mode is a directional one.
func isDirectional(mode int) bool { return mode >= V_PRED && mode <= D67_PRED }

// predictIntra predicts a transform block into pred (w by h, its stride
// w) from the plane's reconstructed samples (stride apart).
func (e *intraEdges) predictIntra(b *intraBlock, rec []uint16, stride int, pred []uint16) {
	w, h := 1<<uint(b.log2W), 1<<uint(b.log2H)
	above, left := e.above[edgeOff:], e.left[edgeOff:]
	mid := int32(1) << uint(b.bitDepth-1)
	at := func(x, y int) int32 { return int32(rec[y*stride+x]) }
	switch {
	case !b.haveAbove && b.haveLeft:
		v := at(b.x-1, b.y)
		for i := range w + h {
			above[i] = v
		}
	case !b.haveAbove:
		for i := range w + h {
			above[i] = mid - 1
		}
	default:
		lim := b.x + w - 1
		if b.haveAboveRight {
			lim = b.x + 2*w - 1
		}
		lim = min(lim, b.maxX)
		row := rec[(b.y-1)*stride:]
		for i := range w + h {
			above[i] = int32(row[min(lim, b.x+i)])
		}
	}
	switch {
	case !b.haveLeft && b.haveAbove:
		v := at(b.x, b.y-1)
		for i := range w + h {
			left[i] = v
		}
	case !b.haveLeft:
		for i := range w + h {
			left[i] = mid + 1
		}
	default:
		lim := b.y + h - 1
		if b.haveBelow {
			lim = b.y + 2*h - 1
		}
		lim = min(lim, b.maxY)
		for i := range w + h {
			left[i] = at(b.x-1, min(lim, b.y+i))
		}
	}
	var corner int32
	switch {
	case b.haveAbove && b.haveLeft:
		corner = at(b.x-1, b.y-1)
	case b.haveAbove:
		corner = at(b.x, b.y-1)
	case b.haveLeft:
		corner = at(b.x-1, b.y)
	default:
		corner = mid
	}
	e.above[edgeOff-1], e.left[edgeOff-1] = corner, corner
	maxV := int32(1)<<uint(b.bitDepth) - 1
	switch {
	case b.plane == 0 && b.filterIntra >= 0:
		e.recursive(b, w, h, maxV, pred)
	case isDirectional(b.mode):
		e.directional(b, w, h, maxV, pred)
	case b.mode == SMOOTH_PRED || b.mode == SMOOTH_V_PRED || b.mode == SMOOTH_H_PRED:
		e.smooth(b, w, h, pred)
	case b.mode == DC_PRED:
		e.dc(b, w, h, pred)
	default:
		e.paeth(w, h, pred)
	}
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func (e *intraEdges) paeth(w, h int, pred []uint16) {
	above, left := e.above[edgeOff-1:], e.left[edgeOff:]
	tl := above[0]
	above = above[1:]
	for i := range h {
		for j := range w {
			base := above[j] + left[i] - tl
			pl, pt, ptl := abs32(base-left[i]), abs32(base-above[j]), abs32(base-tl)
			v := tl
			switch {
			case pl <= pt && pl <= ptl:
				v = left[i]
			case pt <= ptl:
				v = above[j]
			}
			pred[i*w+j] = uint16(v) //nolint:gosec // a sample
		}
	}
}

func (e *intraEdges) dc(b *intraBlock, w, h int, pred []uint16) {
	above, left := e.above[edgeOff:], e.left[edgeOff:]
	var avg int32
	switch {
	case b.haveLeft && b.haveAbove:
		var sum int32
		for k := range h {
			sum += left[k]
		}
		for k := range w {
			sum += above[k]
		}
		sum += int32(w+h) >> 1
		avg = sum / int32(w+h)
	case b.haveLeft:
		var sum int32
		for k := range h {
			sum += left[k]
		}
		avg = (sum + int32(h>>1)) >> uint(b.log2H)
	case b.haveAbove:
		var sum int32
		for k := range w {
			sum += above[k]
		}
		avg = (sum + int32(w>>1)) >> uint(b.log2W)
	default:
		avg = 1 << uint(b.bitDepth-1)
	}
	v := uint16(avg) //nolint:gosec // a sample
	for i := range w * h {
		pred[i] = v
	}
}

// smWeights are the smooth modes' weights by log2 of the size.
var smWeights = [7][]uint8{nil, nil, smWeightsTx4x4[:], smWeightsTx8x8[:], smWeightsTx16x16[:], smWeightsTx32x32[:], smWeightsTx64x64[:]}

func (e *intraEdges) smooth(b *intraBlock, w, h int, pred []uint16) {
	above, left := e.above[edgeOff:], e.left[edgeOff:]
	wx, wy := smWeights[b.log2W], smWeights[b.log2H]
	bl, tr := left[h-1], above[w-1]
	for i := range h {
		for j := range w {
			var v int32
			switch b.mode {
			case SMOOTH_PRED:
				s := int32(wy[i])*above[j] + (256-int32(wy[i]))*bl + int32(wx[j])*left[i] + (256-int32(wx[j]))*tr
				v = (s + 256) >> 9
			case SMOOTH_V_PRED:
				s := int32(wy[i])*above[j] + (256-int32(wy[i]))*bl
				v = (s + 128) >> 8
			default:
				s := int32(wx[j])*left[i] + (256-int32(wx[j]))*tr
				v = (s + 128) >> 8
			}
			pred[i*w+j] = uint16(v) //nolint:gosec // a sample
		}
	}
}

func clip(v, maxV int32) int32 { return min(max(v, 0), maxV) }

func (e *intraEdges) recursive(b *intraBlock, w, h int, maxV int32, pred []uint16) {
	left := e.left[edgeOff:]
	taps := &intraFilterTaps[b.filterIntra]
	var p [7]int32
	for i2 := range h >> 1 {
		for j4 := range w >> 2 {
			for i := range 7 {
				if i < 5 {
					switch {
					case i2 == 0:
						p[i] = e.above[edgeOff+(j4<<2)+i-1]
					case j4 == 0 && i == 0:
						p[i] = left[(i2<<1)-1]
					default:
						p[i] = int32(pred[((i2<<1)-1)*w+(j4<<2)+i-1])
					}
				} else {
					if j4 == 0 {
						p[i] = left[(i2<<1)+i-5]
					} else {
						p[i] = int32(pred[((i2<<1)+i-5)*w+(j4<<2)-1])
					}
				}
			}
			for i1 := range 2 {
				for j1 := range 4 {
					var pr int32
					for i, t := range taps[(i1<<2)+j1] {
						pr += int32(t) * p[i]
					}
					pred[((i2<<1)+i1)*w+(j4<<2)+j1] = uint16(clip(round2Signed(pr, INTRA_FILTER_SCALE_BITS), maxV)) //nolint:gosec // clipped
				}
			}
		}
	}
}

func round2Signed(v int32, n int) int32 {
	if v < 0 {
		return -((-v + 1<<uint(n-1)) >> uint(n))
	}
	return (v + 1<<uint(n-1)) >> uint(n)
}

// edgeStrength is the intra edge filter's strength (7.11.2.9).
func edgeStrength(w, h int, smooth bool, delta int) int {
	d := delta
	if d < 0 {
		d = -d
	}
	wh := w + h
	s := 0
	if !smooth {
		switch {
		case wh <= 8:
			if d >= 56 {
				s = 1
			}
		case wh <= 16:
			if d >= 40 {
				s = 1
			}
		case wh <= 24:
			if d >= 8 {
				s = 1
			}
			if d >= 16 {
				s = 2
			}
			if d >= 32 {
				s = 3
			}
		case wh <= 32:
			s = 1
			if d >= 4 {
				s = 2
			}
			if d >= 32 {
				s = 3
			}
		default:
			s = 3
		}
		return s
	}
	switch {
	case wh <= 8:
		if d >= 40 {
			s = 1
		}
		if d >= 64 {
			s = 2
		}
	case wh <= 16:
		if d >= 20 {
			s = 1
		}
		if d >= 48 {
			s = 2
		}
	case wh <= 24:
		if d >= 4 {
			s = 3
		}
	default:
		s = 3
	}
	return s
}

// useUpsample is the intra edge upsample selection (7.11.2.10).
func useUpsample(w, h int, smooth bool, delta int) bool {
	d := delta
	if d < 0 {
		d = -d
	}
	if d <= 0 || d >= 40 {
		return false
	}
	if !smooth {
		return w+h <= 16
	}
	return w+h <= 8
}

// filterEdge filters an edge's first sz samples from -1 (7.11.2.12).
func filterEdge(buf []int32, sz, strength int) {
	if strength == 0 {
		return
	}
	var edge [129 + 1]int32
	for i := range sz {
		edge[i] = buf[edgeOff+i-1]
	}
	k := &intraEdgeKernel[strength-1]
	for i := 1; i < sz; i++ {
		var s int32
		for j := range 5 {
			s += int32(k[j]) * edge[min(max(i-2+j, 0), sz-1)]
		}
		buf[edgeOff+i-1] = (s + 8) >> 4
	}
}

// upsampleEdge doubles an edge's first numPx samples (7.11.2.11).
func upsampleEdge(buf []int32, numPx int, maxV int32) {
	var dup [64 + 3]int32
	dup[0] = buf[edgeOff-1]
	for i := -1; i < numPx; i++ {
		dup[i+2] = buf[edgeOff+i] //nolint:gosec // numPx is at most 24 (upsampling is for w+h <= 16)
	}
	dup[numPx+2] = buf[edgeOff+numPx-1]
	buf[edgeOff-2] = dup[0]
	for i := range numPx {
		s := -dup[i] + 9*dup[i+1] + 9*dup[i+2] - dup[i+3] //nolint:gosec // as above
		buf[edgeOff+2*i-1] = clip((s+8)>>4, maxV)
		buf[edgeOff+2*i] = dup[i+2] //nolint:gosec // as above
	}
}

func (e *intraEdges) directional(b *intraBlock, w, h int, maxV int32, pred []uint16) {
	pAngle := int(modeToAngle[b.mode]) + b.angleDelta*ANGLE_STEP
	upA, upL := 0, 0
	if b.edgeFilter {
		if pAngle != 90 && pAngle != 180 {
			if pAngle > 90 && pAngle < 180 && w+h >= 24 {
				s := e.left[edgeOff]*5 + e.above[edgeOff-1]*6 + e.above[edgeOff]*5
				c := (s + 8) >> 4
				e.left[edgeOff-1], e.above[edgeOff-1] = c, c
			}
			if b.haveAbove {
				st := edgeStrength(w, h, b.smoothNeighbour, pAngle-90)
				n := min(w, b.maxX-b.x+1) + 1
				if pAngle < 90 {
					n += h
				}
				filterEdge(e.above[:], n, st)
			}
			if b.haveLeft {
				st := edgeStrength(w, h, b.smoothNeighbour, pAngle-180)
				n := min(h, b.maxY-b.y+1) + 1
				if pAngle > 180 {
					n += w
				}
				filterEdge(e.left[:], n, st)
			}
		}
		if useUpsample(w, h, b.smoothNeighbour, pAngle-90) {
			upA = 1
			n := w
			if pAngle < 90 {
				n += h
			}
			upsampleEdge(e.above[:], n, maxV)
		}
		if useUpsample(w, h, b.smoothNeighbour, pAngle-180) {
			upL = 1
			n := h
			if pAngle > 180 {
				n += w
			}
			upsampleEdge(e.left[:], n, maxV)
		}
	}
	above, left := e.above[edgeOff:], e.left[edgeOff:]
	// edge index i (from -2) of above and left
	ab := func(i int) int32 { return e.above[edgeOff+i] }
	lf := func(i int) int32 { return e.left[edgeOff+i] }
	var dx, dy int
	if pAngle < 90 {
		dx = int(drIntraDerivative[pAngle])
	} else if pAngle > 90 && pAngle < 180 {
		dx = int(drIntraDerivative[180-pAngle])
	}
	if pAngle > 90 && pAngle < 180 {
		dy = int(drIntraDerivative[pAngle-90])
	} else if pAngle > 180 {
		dy = int(drIntraDerivative[270-pAngle])
	}
	switch {
	case pAngle < 90:
		maxBase := (w + h - 1) << uint(upA)
		for i := range h {
			idx := (i + 1) * dx
			shift := int32((idx<<uint(upA))>>1) & 0x1f
			b0 := idx >> uint(6-upA)
			// the samples whose base is before maxBase, then its sample
			n := min(max((maxBase-b0+(1<<uint(upA))-1)>>uint(upA), 0), w)
			out := pred[i*w : i*w+w]
			if upA == 0 {
				a := above[b0 : b0+n+1]
				for j := range out[:n] {
					out[j] = uint16((a[j]*(32-shift) + a[j+1]*shift + 16) >> 5) //nolint:gosec // a sample
				}
			} else {
				for j := range out[:n] {
					base := b0 + 2*j
					out[j] = uint16((above[base]*(32-shift) + above[base+1]*shift + 16) >> 5) //nolint:gosec // a sample
				}
			}
			for j := n; j < w; j++ {
				out[j] = uint16(above[maxBase]) //nolint:gosec // a sample
			}
		}
	case pAngle > 90 && pAngle < 180:
		for i := range h {
			// from the left edge while the above one's base would be
			// before -(1 << upA), that is idx < -64: to j0
			j0 := min(max(((i+1)*dx-64+63)>>6, 0), w)
			out := pred[i*w : i*w+w]
			for j := range out[:j0] {
				idx := (i << 6) - (j+1)*dy
				base := idx >> uint(6-upL)
				shift := int32((idx<<uint(upL))>>1) & 0x1f
				out[j] = uint16((lf(base)*(32-shift) + lf(base+1)*shift + 16) >> 5) //nolint:gosec // a sample
			}
			for j := j0; j < w; j++ {
				idx := (j << 6) - (i+1)*dx
				base := idx >> uint(6-upA)
				shift := int32((idx<<uint(upA))>>1) & 0x1f
				out[j] = uint16((ab(base)*(32-shift) + ab(base+1)*shift + 16) >> 5) //nolint:gosec // a sample
			}
		}
	case pAngle > 180:
		for j := range w {
			idx := (j + 1) * dy
			shift := int32((idx<<uint(upL))>>1) & 0x1f
			for i := range h {
				base := (idx >> uint(6-upL)) + (i << uint(upL))
				v := (left[base]*(32-shift) + left[base+1]*shift + 16) >> 5
				pred[i*w+j] = uint16(v) //nolint:gosec // a sample
			}
		}
	case pAngle == 90:
		for i := range h {
			for j := range w {
				pred[i*w+j] = uint16(above[j]) //nolint:gosec // a sample
			}
		}
	default:
		for i := range h {
			for j := range w {
				pred[i*w+j] = uint16(left[i]) //nolint:gosec // a sample
			}
		}
	}
}
