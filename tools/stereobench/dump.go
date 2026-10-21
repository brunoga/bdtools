package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"slices"

	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// dump writes a measured picture's maps, each at half the picture's size,
// in two rows of three:
//
//	the left eye     the disc's disparity        the network's, fitted
//	the right eye    the made right eye's error  the disparity's error
//
// Disparity is grey, lighter nearer (both on the disc's 1st to 99th
// percentile); unsure matches are blue. The made right eye's error (the
// fitted depth's) and the disparity's are black for none, white for 32
// luma levels and 8 pixels.
func dump(path string, left, right, made *mvc.Frame, dm *stereo.Disparity, hr []float32, a, b float64) error {
	w, h := dm.W, dm.H
	img := image.NewRGBA(image.Rect(0, 0, 3*w, 2*h))
	var valid []float32
	for i, ok := range dm.Valid {
		if ok {
			valid = append(valid, dm.D[i])
		}
	}
	slices.Sort(valid)
	lo, hi := float32(-1), float32(1)
	if len(valid) > 0 {
		lo, hi = valid[len(valid)/100], valid[len(valid)-1-len(valid)/100]
	}
	grey := func(d float32) color.RGBA {
		v := uint8(math.Round(float64(255 * min(max((d-lo)/max(hi-lo, 1e-3), 0), 1))))
		return color.RGBA{v, v, v, 255}
	}
	heat := func(e, full float64) color.RGBA {
		v := uint8(math.Round(255 * min(e/full, 1)))
		return color.RGBA{v, v, v, 255}
	}
	lum := func(f *mvc.Frame, x, y int) float64 {
		return (float64(f.Y[2*y*f.StrideY+2*x]) + float64(f.Y[2*y*f.StrideY+2*x+1]) +
			float64(f.Y[(2*y+1)*f.StrideY+2*x]) + float64(f.Y[(2*y+1)*f.StrideY+2*x+1])) / 4
	}
	blue := color.RGBA{0, 0, 160, 255}
	for y := range h {
		for x := range w {
			i := y*w + x
			l, r := uint8(lum(left, x, y)), uint8(lum(right, x, y))
			img.SetRGBA(x, y, color.RGBA{l, l, l, 255})
			img.SetRGBA(x, h+y, color.RGBA{r, r, r, 255})
			fd := float32(a*float64(hr[i]) + b)
			if math.IsNaN(float64(hr[i])) {
				fd = lo
			}
			img.SetRGBA(2*w+x, y, grey(fd))
			img.SetRGBA(w+x, h+y, heat(math.Abs(lum(made, x, y)-lum(right, x, y)), 32))
			if dm.Valid[i] {
				img.SetRGBA(w+x, y, grey(dm.D[i]))
				img.SetRGBA(2*w+x, h+y, heat(math.Abs(float64(fd-dm.D[i])), 8))
			} else {
				img.SetRGBA(w+x, y, blue)
				img.SetRGBA(2*w+x, h+y, blue)
			}
		}
	}
	f, err := os.Create(path) //nolint:gosec // the operator's directory
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
