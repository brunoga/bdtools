package main

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// depthDir (-depth-dir) holds another network's depth of the clips'
// left eyes, iw3's export (--export --export-depth-only): DIR/CLIP/CLIP/
// depth/%08d.png, 16-bit grey at the network's size, larger nearer,
// each picture's span stretched to the whole range. Its depth stands in
// for ours in the depth's measures (the fits are a picture's own, so the
// stretching does not count); the made eyes stay ours.
var depthDir string

// otherDepth is picture k of a clip's depth from depthDir, upsampled
// (bilinear) to w x h, NaN outside the active rows [top, bottom).
func otherDepth(clip string, k, w, h, top, bottom int) ([]float32, error) {
	f, err := os.Open(filepath.Join(depthDir, clip, clip, "depth", fmt.Sprintf("%08d.png", k))) //nolint:gosec // the operator's directory
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	g, ok := img.(*image.Gray16)
	if !ok {
		return nil, fmt.Errorf("%s picture %d: %T, not 16-bit grey", clip, k, img)
	}
	sw, sh := g.Rect.Dx(), g.Rect.Dy()
	at := func(x, y int) float32 {
		i := g.PixOffset(g.Rect.Min.X+x, g.Rect.Min.Y+y)
		return float32(uint16(g.Pix[i])<<8|uint16(g.Pix[i+1])) / 65535
	}
	out := make([]float32, w*h)
	nan := float32(math.NaN())
	for y := range h {
		if y < top || y >= bottom {
			for x := range w {
				out[y*w+x] = nan
			}
			continue
		}
		fy := max((float32(y)+0.5)*float32(sh)/float32(h)-0.5, 0)
		y0 := min(int(fy), sh-1)
		y1, ty := min(y0+1, sh-1), fy-float32(y0)
		for x := range w {
			fx := max((float32(x)+0.5)*float32(sw)/float32(w)-0.5, 0)
			x0 := min(int(fx), sw-1)
			x1, tx := min(x0+1, sw-1), fx-float32(x0)
			a := at(x0, y0)*(1-tx) + at(x1, y0)*tx
			b := at(x0, y1)*(1-tx) + at(x1, y1)*tx
			out[y*w+x] = a*(1-ty) + b*ty
		}
	}
	return out, nil
}
