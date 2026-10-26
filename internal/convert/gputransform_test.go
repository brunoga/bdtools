package convert

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/hdr"
)

// The GPU's scaling and tone mapping give the CPU's pictures: cropped,
// scaled up and down, PQ and HLG, 8 and 10 bits, full and limited range.
func TestGPUTransform(t *testing.T) {
	for _, c := range []struct {
		name     string
		depth    int
		crop     Crop
		scale    Scale
		transfer int
		full     bool
		peak     float64
	}{
		{name: "down 10-bit PQ", depth: 10, scale: Scale{W: 640, H: 360}, transfer: 16, peak: 4000},
		{name: "cropped down 10-bit", depth: 10, crop: Crop{Top: 40, Bottom: 40, Left: 16, Right: 16}, scale: Scale{W: 600}},
		{name: "up 8-bit", depth: 8, scale: Scale{W: 1600}},
		{name: "PQ only", depth: 10, transfer: 16, peak: 1000},
		{name: "PQ full range", depth: 10, transfer: 16, full: true, peak: 600},
		{name: "HLG down", depth: 10, scale: Scale{W: 960}, transfer: 18},
	} {
		t.Run(c.name, func(t *testing.T) {
			const w, h = 1280, 720
			rng := rand.New(rand.NewPCG(1, uint64(c.depth)))
			top := 1<<c.depth - 1
			// gradients, noise and hard edges: every part of the curves
			src := testPicture(w, h, c.depth, func(x, y int) int {
				v := (x*top/w + y*top/h) / 2
				if (x/37+y/23)%5 == 0 {
					v = rng.IntN(top + 1)
				}
				return v
			}, func(x, y, ch int) int {
				mid := 1 << (c.depth - 1)
				return min(max(mid+(x-y+ch*50)*top/(2*w)+rng.IntN(top/16)-top/32, 0), top)
			})
			src.Color = gpu.ColorInfo{Primaries: 9, Transfer: c.transfer, Matrix: 9, FullRange: c.full}
			pt, err := newPicTransform(c.crop, c.scale, w, h)
			if err != nil {
				t.Fatal(err)
			}
			if c.transfer != 0 {
				pt.tm = newToneMapper(c.transfer, hdr.Static{})
				if c.peak > 0 {
					pt.tm.setPeak(c.peak)
				}
			}
			gx := newGPUTransform(pt)
			if gx == nil {
				t.Skip("no CUDA GPU")
			}
			defer gx.close()
			want, err := pt.apply(picture{gpu: src})
			if err != nil {
				t.Fatal(err)
			}
			pt.gx = gx
			got, err := pt.apply(picture{gpu: src})
			if err != nil || pt.gpuErr != nil {
				t.Fatal(err, pt.gpuErr)
			}
			a, b := want.gpu, got.gpu
			if a.Width != b.Width || a.Height != b.Height || a.Pitch != b.Pitch || a.Color != b.Color {
				t.Fatalf("GPU %dx%d pitch %d %+v, CPU %dx%d pitch %d %+v", b.Width, b.Height, b.Pitch, b.Color,
					a.Width, a.Height, a.Pitch, a.Color)
			}
			if !bytes.Equal(a.Y, b.Y) || !bytes.Equal(a.UV, b.UV) {
				diffs := 0
				for i := range a.Y {
					if a.Y[i] != b.Y[i] {
						diffs++
					}
				}
				for i := range a.UV {
					if a.UV[i] != b.UV[i] {
						diffs++
					}
				}
				t.Errorf("the GPU's picture differs from the CPU's in %d of %d bytes", diffs, len(a.Y)+len(a.UV))
			}
		})
	}
}
