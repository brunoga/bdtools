package convert

import (
	"encoding/binary"
	"testing"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/mvc"
)

func testFrame(w, h int, seed byte) *mvc.Frame {
	f := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: w / 2,
		Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4)}
	for i := range f.Y {
		f.Y[i] = byte(i*7) + seed
	}
	for i := range f.Cb {
		f.Cb[i], f.Cr[i] = byte(i*3)+seed, byte(i*5)+seed
	}
	return f
}

// Over-under puts the left eye on top and the right below, sample for
// sample, at 8 and 10 bits; half over-under filters each pair of rows.
func TestDrawOverUnder(t *testing.T) {
	const w, h = 16, 8
	a, b := testFrame(w, h, 0), testFrame(w, h, 100)
	sf := &mvc.StereoFrame{Base: a, Dependent: b}
	for _, depth := range []int{8, 10} {
		bps := 1 + depth/10
		get := func(pl []byte, pitch, x, y int) int {
			if bps == 2 {
				return int(binary.LittleEndian.Uint16(pl[y*pitch+2*x:]) >> 6)
			}
			return int(pl[y*pitch+x])
		}
		scale := 1 << (depth - 8)
		for _, l := range []Layout{LayoutFullOU, LayoutHalfOU} {
			oh := 2 * h
			if l.halved() {
				oh = h
			}
			p := &gpu.Picture{Y: make([]byte, w*oh*bps), UV: make([]byte, w*oh/2*bps), Pitch: w * bps, Depth: depth}
			drawFramed(p, sf, false, l)
			for e, f := range []*mvc.Frame{a, b} {
				for y := range oh / 2 {
					for x := range w {
						want := int(f.Y[y*w+x]) * scale
						if l.halved() {
							at := func(k int) int { return int(f.Y[min(max(k, 0), h-1)*w+x]) }
							s := -at(2*y-1) + 5*at(2*y) + 5*at(2*y+1) - at(2*y+2)
							if depth == 8 {
								want = min(max((s+4)>>3, 0), 255)
							} else {
								want = min(max((s+1)>>1, 0), 1023)
							}
						}
						if got := get(p.Y, p.Pitch, x, e*oh/2+y); got != want {
							t.Fatalf("%s %d-bit: eye %d luma (%d,%d) = %d, want %d", l, depth, e, x, y, got, want)
						}
					}
				}
				if !l.halved() {
					if got, want := get(p.UV, p.Pitch, 2, e*h/2+1), int(f.Cb[1*w/2+1])*scale; got != want {
						t.Fatalf("%s %d-bit: eye %d Cb = %d, want %d", l, depth, e, got, want)
					}
				}
			}
		}
	}
}
