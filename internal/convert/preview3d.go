package convert

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// --3d-preview: --to-3d's pairs as stills, in seconds, before a
// conversion of hours. At previewPlaces places spread over the part
// converted (by its bytes, as --crop auto looks), the pictures are
// decoded from a keyframe on and given to the depth network as a
// conversion does, and the one previewWarm pictures in (the network's
// view of the pictures before it settled) is made a pair at each of
// previewDepths and --3d-depth: a full side-by-side JPEG to look at in a
// headset or on a 3D screen, and a red-cyan anaglyph to look at on any.

const (
	previewPlaces = 8
	previewWarm   = 16
	previewJPEG   = 90 // the JPEGs' quality
)

// previewDepths are the depths (--3d-depth) each still is made at.
var previewDepths = []float64{1, 1.5, 2, 2.5}

// preview3D writes the stills into the output directory; feed samples the
// video.
func (r *Runner) preview3D(video Track, feed func() (sampleFeed, func(), error)) error {
	dir := r.Opts.Output
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // the operator's directory
		return err
	}
	if r.Opts.Crop.Auto {
		f, done, err := feed()
		if err := r.resolveCrop(video, f, done, err); err != nil {
			return err
		}
	}
	sample, done, err := feed()
	if err != nil {
		return fmt.Errorf("--3d-preview: %w", err)
	}
	defer done()
	defer r.closeTransform()
	m, err := r.depthModel()
	if err != nil {
		return err
	}
	depths := previewDepths
	if d := r.Opts.depth3D(); !slices.Contains(depths, d) {
		depths = append(slices.Clone(depths), d)
		slices.Sort(depths)
	}
	shapes := make([]stereo.Shape, len(depths))
	for i, d := range depths {
		o := r.Opts
		o.Depth3D = d
		shapes[i] = o.shape3D(m.Size())
	}
	// the places within the part converted
	lo, hi := 0.0, 1.0
	if !r.Opts.Trim.IsZero() && r.length > 0 {
		end := r.Opts.Trim.End
		if end == 0 || end > r.length {
			end = r.length
		}
		lo, hi = r.Opts.Trim.Start.Seconds()/r.length.Seconds(), end.Seconds()/r.length.Seconds()
	}
	// still makes the stills of place i, frac of the way through; ok is
	// false when no picture decodes there.
	reported := false
	still := func(i int, frac float64) (ok bool, err error) {
		c, err := r.newConverter3D(m, !reported)
		if err != nil {
			return false, err
		}
		reported = true
		name := fmt.Sprintf("%02d", i+1)
		if r.length > 0 {
			name += "-" + previewClock(time.Duration(frac*float64(r.length)).Round(time.Second))
		}
		e, err := r.previewEstimate(video, sample, frac, c)
		if err != nil || e == nil {
			return false, err
		}
		for k, s := range shapes {
			left, right := c.Still(e, s)
			base := filepath.Join(dir, fmt.Sprintf("%s-depth%g", name, depths[k]))
			if err := writeJPEG(base+"-sbs.jpg", sideBySide(left, right)); err != nil {
				return false, err
			}
			if err := writeJPEG(base+"-anaglyph.jpg", anaglyph(left, right)); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	stills := 0
	for i := range previewPlaces {
		ok, err := still(i, lo+(hi-lo)*(float64(i)+0.5)/previewPlaces)
		if err != nil {
			return err
		}
		if ok {
			stills++
		}
	}
	if stills == 0 {
		// a short video, its only keyframes before every place: its start
		ok, err := still(0, lo)
		if err != nil {
			return err
		}
		if ok {
			stills++
		}
	}
	if stills == 0 {
		return errors.New("--3d-preview: no picture decoded at any place")
	}
	r.Report.Report("--3d-preview: %d places at depths %v in %s (NN-time-depthD: -sbs.jpg full side by side, -anaglyph.jpg red-cyan)",
		stills, depths, dir)
	return nil
}

// previewEstimate is the depth of the picture previewWarm in at a place
// (the last there, when fewer), nil when none decodes.
func (r *Runner) previewEstimate(video Track, feed sampleFeed, frac float64, c *stereo.Converter) (*stereo.Estimate, error) {
	next, done, err := feed(frac)
	if err != nil {
		return nil, nil //nolint:nilerr // a place that cannot be read adds no still
	}
	defer done()
	src, err := r.pictures(video, next)
	if err != nil {
		return nil, err
	}
	var e *stereo.Estimate
	n := 0
	err = src.run(func(p picture) error {
		p, err := r.transform(p, src)
		if err != nil {
			return err
		}
		if e, err = c.Depth(pictureFrame8(p)); err != nil {
			return err
		}
		if n++; n > previewWarm {
			return errEnoughPictures
		}
		return nil
	})
	if err != nil && !errors.Is(err, errEnoughPictures) {
		if e != nil {
			return e, nil // what decoded before a damaged stretch
		}
		return nil, err
	}
	return e, nil
}

// previewClock is a time for a file's name: 1h02m03s.
func previewClock(d time.Duration) string {
	h, m, s := d/time.Hour, d%time.Hour/time.Minute, d%time.Minute/time.Second
	return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
}

func writeJPEG(file string, img image.Image) error {
	f, err := os.Create(file) //nolint:gosec // the operator's directory
	if err != nil {
		return err
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: previewJPEG}); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// rgb is a picture's pixel (BT.709, limited range) in RGB.
func rgb(f *mvc.Frame, x, y int) (byte, byte, byte) {
	yy := 1.164 * (float64(f.Y[y*f.StrideY+x]) - 16)
	cb := float64(f.Cb[(y/2)*f.StrideC+x/2]) - 128
	cr := float64(f.Cr[(y/2)*f.StrideC+x/2]) - 128
	c := func(v float64) byte { return byte(min(max(v+0.5, 0), 255)) }
	return c(yy + 1.793*cr), c(yy - 0.213*cb - 0.533*cr), c(yy + 2.112*cb)
}

// sideBySide is a pair as full side by side.
func sideBySide(l, r *mvc.Frame) *image.RGBA {
	w, h := l.Width, l.Height
	img := image.NewRGBA(image.Rect(0, 0, 2*w, h))
	eachRow(h, func(y int) {
		for x := range w {
			for k, f := range [2]*mvc.Frame{l, r} {
				i := img.PixOffset(k*w+x, y)
				img.Pix[i], img.Pix[i+1], img.Pix[i+2] = rgb(f, x, y)
				img.Pix[i+3] = 255
			}
		}
	})
	return img
}

// anaglyph is a pair as red-cyan: the left eye's red, the right's green
// and blue.
func anaglyph(l, r *mvc.Frame) *image.RGBA {
	w, h := l.Width, l.Height
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	eachRow(h, func(y int) {
		for x := range w {
			i := img.PixOffset(x, y)
			img.Pix[i], _, _ = rgb(l, x, y)
			_, img.Pix[i+1], img.Pix[i+2] = rgb(r, x, y)
			img.Pix[i+3] = 255
		}
	})
	return img
}

// eachRow runs fn for rows [0, h), on all the CPUs.
func eachRow(h int, fn func(y int)) {
	n := min(runtime.GOMAXPROCS(0), h)
	var wg sync.WaitGroup
	for k := range n {
		wg.Go(func() {
			for y := k; y < h; y += n {
				fn(y)
			}
		})
	}
	wg.Wait()
}
