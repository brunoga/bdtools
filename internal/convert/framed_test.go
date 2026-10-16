package convert

import (
	"testing"

	"github.com/brunoga/bdtools/internal/gpu"
)

// The eyes come out of a framed picture as they went in: copied from a
// full frame; from a half one, every other sample as it was and the ones
// between filtered; from NV12 and P010 alike.
func TestSplitFramed(t *testing.T) {
	const fw, fh = 32, 16
	for _, depth := range []int{8, 10} {
		bps := 1
		if depth > 8 {
			bps = 2
		}
		for _, l := range []Layout{LayoutFullSBS, LayoutHalfSBS, LayoutFullOU, LayoutHalfOU} {
			// Luma: 10*x + y, plus 100 in the second eye's part.
			g := &gpu.DecodedPicture{Width: fw, Height: fh, Depth: depth, Pitch: fw * bps,
				Y: make([]byte, fw*fh*bps), UV: make([]byte, fw*fh/2*bps)}
			second := func(x, y int) bool {
				if l.overUnder() {
					return y >= fh/2
				}
				return x >= fw/2
			}
			val := func(x, y int) int {
				ex, ey := x, y
				if l.overUnder() {
					ey %= fh / 2
				} else {
					ex %= fw / 2
				}
				v := (3*ex + 2*ey) % 100
				if second(x, y) {
					v += 100
				}
				return v
			}
			for y := range fh {
				for x := range fw {
					g.Y[y*g.Pitch+x*bps+bps-1] = byte(val(x, y))
				}
			}
			a, b := splitFramed(picture{gpu: g}, l)
			for e, f := range []*struct {
				w, h int
				y    []byte
			}{{a.Width, a.Height, a.Y}, {b.Width, b.Height, b.Y}} {
				ew, eh := fw/2, fh
				if l.overUnder() {
					ew, eh = fw, fh/2
				}
				wantW, wantH := ew, eh
				if l.halved() {
					if l.overUnder() {
						wantH *= 2
					} else {
						wantW *= 2
					}
				}
				if f.w != wantW || f.h != wantH {
					t.Fatalf("%s %d-bit: eye %d is %dx%d, want %dx%d", l, depth, e, f.w, f.h, wantW, wantH)
				}
				for y := range f.h {
					for x := range f.w {
						sx, sy, exact := x, y, true
						if l.halved() {
							if l.overUnder() {
								sy, exact = y/2, y%2 == 0
							} else {
								sx, exact = x/2, x%2 == 0
							}
						}
						if !exact {
							continue
						}
						if l.overUnder() {
							sy += e * eh
						} else {
							sx += e * ew
						}
						if got, want := int(f.y[y*f.w+x]), val(sx, sy); got != want {
							t.Fatalf("%s %d-bit: eye %d (%d,%d) = %d, want %d", l, depth, e, x, y, got, want)
						}
					}
				}
			}
		}
	}
}
