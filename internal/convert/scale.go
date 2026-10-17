package convert

import (
	"encoding/binary"
	"math"
	"runtime"
	"sync"

	"github.com/brunoga/bdtools/internal/gpu"
)

// Scaling a 2D conversion's pictures (--scale): a separable Lanczos-3
// filter, widened by the ratio when it shrinks so that it low-passes as
// it should, over NV12 or P010 pictures as the decoders give them.
//
// Chroma keeps 4:2:0's MPEG-2 siting (BT.709 and BT.2020 sources alike):
// a chroma sample sits on the even luma column, and halfway between two
// luma rows. Each plane is mapped by its samples' positions in luma
// coordinates, so the chroma stays where the luma is.

// lanczosA is the Lanczos kernel's lobes.
const lanczosA = 3

// tapBits is the fixed-point precision of the filter weights.
const tapBits = 14

// filterTaps is one dimension's filter: for each output sample, the first
// input sample it reads and the weights of the n it reads from there.
type filterTaps struct {
	n     int
	first []int
	w     []int32 // n per output sample, summing to 1<<tapBits
}

// newFilterTaps maps in samples onto out, output sample i centred on input
// position (i+0.5+shift)*in/out - 0.5 - shift: shift 0 for samples centred
// in their cells, -0.25 for 4:2:0 chroma sited on the even luma column.
func newFilterTaps(in, out int, shift float64) filterTaps {
	ratio := float64(in) / float64(out)
	support := lanczosA * max(ratio, 1)
	n := int(math.Ceil(2 * support))
	t := filterTaps{n: n, first: make([]int, out), w: make([]int32, out*n)}
	for i := range out {
		centre := (float64(i)+0.5+shift)*ratio - 0.5 - shift
		first := int(math.Floor(centre - support + 1))
		ws := make([]float64, n)
		sum := 0.0
		for k := range n {
			x := (float64(first+k) - centre) / max(ratio, 1)
			ws[k] = lanczos(x)
			sum += ws[k]
		}
		// The weights in fixed point, rounded so that they add up exactly:
		// a flat picture stays flat.
		acc, total := 0.0, int32(0)
		for k := range n {
			acc += ws[k] / sum
			v := int32(math.Round(acc*(1<<tapBits))) - total
			t.w[i*n+k] = v
			total += v
		}
		t.first[i] = first
	}
	return t
}

func lanczos(x float64) float64 {
	if x == 0 {
		return 1
	}
	if x <= -lanczosA || x >= lanczosA {
		return 0
	}
	px := math.Pi * x
	return lanczosA * math.Sin(px) * math.Sin(px/lanczosA) / (px * px)
}

// scaler scales pictures of one size to another.
type scaler struct {
	inW, inH, outW, outH int
	// The luma's filters, and the chroma's (on chroma samples).
	lx, ly, cx, cy filterTaps
	tmp            []int32 // the horizontal pass, inH x outW luma then chroma
	out            gpu.DecodedPicture
}

func newScaler(inW, inH, outW, outH int) *scaler {
	s := &scaler{inW: inW, inH: inH, outW: outW, outH: outH}
	s.lx = newFilterTaps(inW, outW, 0)
	s.ly = newFilterTaps(inH, outH, 0)
	// A chroma column sits on the even luma column, a quarter of a chroma
	// sample left of where the centred grid would put it; a chroma row
	// sits between its two luma rows, on the centred grid.
	s.cx = newFilterTaps(inW/2, outW/2, -0.25)
	s.cy = newFilterTaps(inH/2, outH/2, 0)
	return s
}

// scale scales src (NV12 or P010) into the scaler's own picture, valid
// until the next call.
func (s *scaler) scale(src *gpu.DecodedPicture) *gpu.DecodedPicture {
	bps := 1
	if src.Depth > 8 {
		bps = 2
	}
	o := &s.out
	if o.Y == nil || o.Depth != src.Depth {
		o.Pitch = s.outW * bps
		o.Y = make([]byte, o.Pitch*s.outH)
		o.UV = make([]byte, o.Pitch*s.outH/2)
	}
	o.Width, o.Height, o.Depth = s.outW, s.outH, src.Depth
	o.PTS, o.Color, o.FrameRateNum, o.FrameRateDen = src.PTS, src.Color, src.FrameRateNum, src.FrameRateDen
	if n := s.inH*s.outW + s.inH/2*s.outW; len(s.tmp) < n {
		s.tmp = make([]int32, n)
	}
	ty, tc := s.tmp[:s.inH*s.outW], s.tmp[s.inH*s.outW:]
	maxV := int32(1)<<src.Depth - 1
	parallelRows(s.inH, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			hPass(ty[y*s.outW:(y+1)*s.outW], src.Y[y*src.Pitch:], bps, 1, s.inW, &s.lx)
		}
	})
	parallelRows(s.inH/2, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			hPass(tc[y*s.outW:(y+1)*s.outW], src.UV[y*src.Pitch:], bps, 2, s.inW/2, &s.cx)
		}
	})
	parallelRows(s.outH, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			vPass(o.Y[y*o.Pitch:], bps, ty, s.outW, s.inH, y, &s.ly, maxV)
		}
	})
	parallelRows(s.outH/2, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			vPass(o.UV[y*o.Pitch:], bps, tc, s.outW, s.inH/2, y, &s.cy, maxV)
		}
	})
	return o
}

// sample reads sample i of a row of n-byte samples (P010's value in the
// top ten bits).
func sample(row []byte, bps, i int) int32 {
	if bps == 1 {
		return int32(row[i])
	}
	return int32(binary.LittleEndian.Uint16(row[2*i:]) >> 6)
}

// hPass filters one row horizontally into dst (outW values, at tapBits
// more precision): ch interleaved channels of w samples each.
func hPass(dst []int32, row []byte, bps, ch, w int, t *filterTaps) {
	outN := len(t.first)
	for i := range outN {
		ws := t.w[i*t.n : (i+1)*t.n]
		first := t.first[i]
		for c := range ch {
			var acc int32
			if first >= 0 && first+t.n <= w {
				for k, wt := range ws {
					acc += wt * sample(row, bps, (first+k)*ch+c)
				}
			} else {
				for k, wt := range ws {
					acc += wt * sample(row, bps, min(max(first+k, 0), w-1)*ch+c)
				}
			}
			dst[i*ch+c] = acc
		}
	}
}

// vPass filters output row y vertically from the horizontal pass's rows
// (each n values) and stores it.
func vPass(dst []byte, bps int, rows []int32, n, h, y int, t *filterTaps, maxV int32) {
	ws := t.w[y*t.n : (y+1)*t.n]
	first := t.first[y]
	const shift = 2 * tapBits
	const round = 1 << (shift - 1)
	for x := range n {
		var acc int64
		for k, wt := range ws {
			r := min(max(first+k, 0), h-1)
			acc += int64(wt) * int64(rows[r*n+x])
		}
		v := int32((acc + round) >> shift)
		v = min(max(v, 0), maxV)
		if bps == 1 {
			dst[x] = byte(v)
		} else {
			binary.LittleEndian.PutUint16(dst[2*x:], uint16(v<<6))
		}
	}
}

// parallelRows runs fn over [0, n) in bands, one per CPU.
func parallelRows(n int, fn func(y0, y1 int)) {
	workers := min(runtime.GOMAXPROCS(0), max(1, n/16))
	if workers <= 1 {
		fn(0, n)
		return
	}
	var wg sync.WaitGroup
	for i := range workers {
		y0, y1 := n*i/workers, n*(i+1)/workers
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(y0, y1)
		}()
	}
	wg.Wait()
}
