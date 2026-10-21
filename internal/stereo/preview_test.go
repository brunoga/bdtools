package stereo

import (
	"bufio"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/brunoga/bdtools/internal/depth"
	"github.com/brunoga/bdtools/mvc"
)

// readPPM reads a binary PPM as an 8-bit 4:2:0 frame (BT.709, limited).
func readPPM(t *testing.T, file string) *mvc.Frame {
	fh, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	r := bufio.NewReader(fh)
	var w, h, mx int
	if _, err := fmt.Fscanf(r, "P6\n%d %d\n%d\n", &w, &h, &mx); err != nil {
		t.Fatal(err)
	}
	px := make([]byte, w*h*3)
	if _, err := io.ReadFull(r, px); err != nil {
		t.Fatal(err)
	}
	f := newFrame(w, h)
	for y := range h {
		for x := range w {
			p := px[(y*w+x)*3:]
			rr, gg, bb := float64(p[0]), float64(p[1]), float64(p[2])
			yy := 16 + (0.2126*rr+0.7152*gg+0.0722*bb)*219/255
			f.Y[y*w+x] = byte(yy + 0.5)
			if y%2 == 0 && x%2 == 0 {
				cb := 128 + (-0.1146*rr-0.3854*gg+0.5*bb)*224/255
				cr := 128 + (0.5*rr-0.4542*gg-0.0458*bb)*224/255
				f.Cb[(y/2)*f.StrideC+x/2], f.Cr[(y/2)*f.StrideC+x/2] = byte(cb+0.5), byte(cr+0.5)
			}
		}
	}
	return f
}

func toRGB(f *mvc.Frame, x, y int) (byte, byte, byte) {
	yy := 1.164 * (float64(f.Y[y*f.StrideY+x]) - 16)
	cb := float64(f.Cb[(y/2)*f.StrideC+x/2]) - 128
	cr := float64(f.Cr[(y/2)*f.StrideC+x/2]) - 128
	c := func(v float64) byte { return byte(min(max(v, 0), 255)) }
	return c(yy + 1.793*cr), c(yy - 0.213*cb - 0.533*cr), c(yy + 2.112*cb)
}

// Film frames ($STEREO_FRAMES, PPM files) converted with the depth model
// ($VDA_WEIGHTS): the last pair as side by side and anaglyph PNGs in
// $STEREO_OUT, to look at.
func TestPreview(t *testing.T) {
	wf, dir, out := os.Getenv("VDA_WEIGHTS"), os.Getenv("STEREO_FRAMES"), os.Getenv("STEREO_OUT")
	if wf == "" || dir == "" || out == "" {
		t.Skip("VDA_WEIGHTS, STEREO_FRAMES or STEREO_OUT unset")
	}
	m, err := depth.Load(wf)
	if err != nil {
		t.Fatal(err)
	}
	c := NewConverter(m, DefaultShape)
	files, _ := filepath.Glob(filepath.Join(dir, "*.ppm"))
	sort.Strings(files)
	var l, r *mvc.Frame
	for _, f := range files {
		var err error
		if l, r, err = c.Convert(readPPM(t, f)); err != nil {
			t.Fatal(err)
		}
	}
	w, h := l.Width, l.Height
	sbs := image.NewRGBA(image.Rect(0, 0, w, h/2))
	ana := image.NewRGBA(image.Rect(0, 0, w/2, h/2))
	for y := 0; y < h; y += 2 {
		for x := 0; x < w; x += 2 {
			lr, lg, lb := toRGB(l, x, y)
			rr, rg, rb := toRGB(r, x, y)
			sbs.Set(x/2, y/2, color.RGBA{lr, lg, lb, 255})
			sbs.Set(w/2+x/2, y/2, color.RGBA{rr, rg, rb, 255})
			ana.Set(x/2, y/2, color.RGBA{lr, rg, rb, 255})
		}
	}
	for name, img := range map[string]image.Image{"sbs.png": sbs, "anaglyph.png": ana} {
		fh, _ := os.Create(filepath.Join(out, name))
		_ = png.Encode(fh, img)
		_ = fh.Close()
	}
	t.Logf("%d frames, %d cuts, active rows %d..%d, network %dx%d", len(files), c.Cuts(), c.top, c.bottom, c.netW, c.netH)
}
