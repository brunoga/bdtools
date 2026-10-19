package av1

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// directional matches the spec's loops (directionalGo, as it was): every
// directional mode and angle delta, block size, edge filter and upsampling
// state, on random edges.
func TestDirectionalMatches(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 4))
	for _, depth := range []int{8, 10} {
		maxV := int32(1)<<depth - 1
		for lw := 2; lw <= 6; lw++ {
			for lh := 2; lh <= 6; lh++ {
				if lw-lh > 2 || lh-lw > 2 {
					continue
				}
				w, h := 1<<lw, 1<<lh
				for mode := V_PRED; mode <= D67_PRED; mode++ {
					for delta := -3; delta <= 3; delta++ {
						for _, ef := range []bool{false, true} {
							var e intraEdges
							for i := range e.above {
								e.above[i], e.left[i] = rng.Int32N(maxV+1), rng.Int32N(maxV+1)
							}
							b := &intraBlock{log2W: lw, log2H: lh, mode: mode, angleDelta: delta, edgeFilter: ef,
								haveAbove: true, haveLeft: true, maxX: 1 << 12, maxY: 1 << 12, smoothNeighbour: rng.IntN(2) == 0, bitDepth: depth}
							e2 := e
							got, want := make([]uint16, w*h), make([]uint16, w*h)
							e.directional(b, w, h, maxV, got)
							e2.directionalGo(b, w, h, maxV, want)
							if !slices.Equal(got, want) {
								t.Fatalf("%d bits %dx%d mode %d delta %d filter %v: differs", depth, w, h, mode, delta, ef)
							}
						}
					}
				}
			}
		}
	}
}

func (e *intraEdges) directionalGo(b *intraBlock, w, h int, maxV int32, pred []uint16) {
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
			for j := range w {
				base := (idx >> uint(6-upA)) + (j << uint(upA))
				v := above[maxBase]
				if base < maxBase {
					v = (above[base]*(32-shift) + above[base+1]*shift + 16) >> 5
				}
				pred[i*w+j] = uint16(v) //nolint:gosec // a sample
			}
		}
	case pAngle > 90 && pAngle < 180:
		for i := range h {
			for j := range w {
				idx := (j << 6) - (i+1)*dx
				base := idx >> uint(6-upA)
				var v int32
				if base >= -(1 << uint(upA)) {
					shift := int32((idx<<uint(upA))>>1) & 0x1f
					v = (ab(base)*(32-shift) + ab(base+1)*shift + 16) >> 5
				} else {
					idx = (i << 6) - (j+1)*dy
					base = idx >> uint(6-upL)
					shift := int32((idx<<uint(upL))>>1) & 0x1f
					v = (lf(base)*(32-shift) + lf(base+1)*shift + 16) >> 5
				}
				pred[i*w+j] = uint16(v) //nolint:gosec // a sample
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
