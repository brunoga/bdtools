package convert

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/brunoga/bdtools/internal/dovi"
	"github.com/brunoga/bdtools/internal/gpu"
)

// Crop is how many rows and columns come off each edge of the picture
// (--crop), before any scaling.
type Crop struct {
	Top, Bottom, Left, Right int
	// Auto finds the black bars to crop before the conversion starts
	// (--crop auto: see cropdetect.go).
	Auto bool
}

// IsZero reports whether the crop takes nothing.
func (c Crop) IsZero() bool { return c == Crop{} }

func (c Crop) String() string {
	if c.Auto {
		return "auto"
	}
	return fmt.Sprintf("%d:%d:%d:%d", c.Top, c.Bottom, c.Left, c.Right)
}

// ParseCrop reads --crop: "none" (or ""), "auto", or TOP:BOTTOM:LEFT:RIGHT
// in samples, each even (4:2:0 chroma goes in pairs).
func ParseCrop(s string) (Crop, error) {
	switch s {
	case "", "none":
		return Crop{}, nil
	case "auto":
		return Crop{Auto: true}, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) != 4 {
		return Crop{}, fmt.Errorf("--crop %q: want none, auto or TOP:BOTTOM:LEFT:RIGHT", s)
	}
	var v [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n%2 != 0 {
			return Crop{}, fmt.Errorf("--crop %q: each edge is an even number of samples", s)
		}
		v[i] = n
	}
	return Crop{Top: v[0], Bottom: v[1], Left: v[2], Right: v[3]}, nil
}

// Scale is the box a picture is scaled to fit (--scale), keeping its
// shape; a picture that fits already stays as it is. Zero scales nothing.
type Scale struct{ W, H int }

// IsZero reports whether no scaling is asked for.
func (s Scale) IsZero() bool { return s == Scale{} }

func (s Scale) String() string { return fmt.Sprintf("%dx%d", s.W, s.H) }

// scaleNames are the boxes --scale knows by name.
var scaleNames = map[string]Scale{
	"2160p": {3840, 2160}, "4k": {3840, 2160}, "1440p": {2560, 1440},
	"1080p": {1920, 1080}, "720p": {1280, 720}, "576p": {1024, 576}, "480p": {854, 480},
}

// ParseScale reads --scale: "none" (or ""), a name (2160p, 1440p, 1080p,
// 720p, 576p, 480p) or WIDTHxHEIGHT.
func ParseScale(s string) (Scale, error) {
	s = strings.ToLower(s)
	if s == "" || s == "none" {
		return Scale{}, nil
	}
	if b, ok := scaleNames[s]; ok {
		return b, nil
	}
	w, h, ok := strings.Cut(s, "x")
	wi, err1 := strconv.Atoi(w)
	hi, err2 := strconv.Atoi(h)
	if !ok || err1 != nil || err2 != nil || wi < 16 || hi < 16 {
		return Scale{}, fmt.Errorf("--scale %q: want none, 2160p, 1440p, 1080p, 720p, 576p, 480p or WIDTHxHEIGHT", s)
	}
	return Scale{wi, hi}, nil
}

// fit is the size a w x h picture takes in the box: its shape kept, both
// sides even, never larger than it was.
func (s Scale) fit(w, h int) (int, int) {
	if s.IsZero() || (w <= s.W && h <= s.H) {
		return w, h
	}
	f := min(float64(s.W)/float64(w), float64(s.H)/float64(h))
	even := func(v float64) int { return max(2, 2*int(v/2+0.5)) }
	return min(even(float64(w)*f), s.W&^1), min(even(float64(h)*f), s.H&^1)
}

// picTransform crops and scales a 2D conversion's pictures.
type picTransform struct {
	crop       Crop
	box        Scale
	inW, inH   int // the source's pictures
	cw, ch     int // cropped
	outW, outH int // and scaled
	sc         *scaler
	tm         *toneMapper        // --tonemap, for an HDR source
	flat       gpu.DecodedPicture // a stereo frame's base view as NV12
}

// newPicTransform sets up the transform for pictures of w x h.
func newPicTransform(crop Crop, box Scale, w, h int) (*picTransform, error) {
	t := &picTransform{crop: crop, box: box, inW: w, inH: h}
	t.cw, t.ch = w-crop.Left-crop.Right, h-crop.Top-crop.Bottom
	if t.cw < 16 || t.ch < 16 {
		return nil, fmt.Errorf("--crop %s leaves nothing of a %dx%d picture", crop, w, h)
	}
	t.outW, t.outH = box.fit(t.cw, t.ch)
	if t.outW != t.cw || t.outH != t.ch {
		t.sc = newScaler(t.cw, t.ch, t.outW, t.outH)
	}
	return t, nil
}

// describe says what the transform does, for the report.
func (t *picTransform) describe() string {
	var parts []string
	if !t.crop.IsZero() {
		parts = append(parts, fmt.Sprintf("cropping %dx%d to %dx%d (top %d, bottom %d, left %d, right %d)",
			t.inW, t.inH, t.cw, t.ch, t.crop.Top, t.crop.Bottom, t.crop.Left, t.crop.Right))
	}
	if t.sc != nil {
		parts = append(parts, fmt.Sprintf("scaling %dx%d to %dx%d (Lanczos)", t.cw, t.ch, t.outW, t.outH))
	}
	if len(parts) == 0 && !t.box.IsZero() {
		return "the picture fits --scale already: not scaled"
	}
	return strings.Join(parts, ", ")
}

// apply transforms a picture; what it gives is valid until the next call.
func (t *picTransform) apply(p picture) (picture, error) {
	w, h, _ := p.size()
	if w != t.inW || h != t.inH {
		return p, fmt.Errorf("the picture size changed from %dx%d to %dx%d: --crop and --scale need one size", t.inW, t.inH, w, h)
	}
	src := p.gpu
	if src == nil {
		src = t.flatten(p)
	}
	bps := 1
	if src.Depth > 8 {
		bps = 2
	}
	c := t.crop
	cropped := *src
	cropped.Width, cropped.Height = t.cw, t.ch
	cropped.Y = src.Y[c.Top*src.Pitch+c.Left*bps:]
	cropped.UV = src.UV[c.Top/2*src.Pitch+c.Left*bps:]
	out := &cropped
	if t.sc != nil {
		out = t.sc.scale(&cropped)
	}
	q := p
	if t.tm != nil {
		// SDR: no HDR metadata goes on.
		t.tm.pictureMeta(p)
		out = t.tm.apply(out)
		q.rpu, q.hdr10Plus = nil, nil
	} else if p.rpu != nil {
		q.rpu = t.reframeRPU(p.rpu)
	}
	q.gpu, q.stereo = out, nil
	return q, nil
}

// flatten gives a stereo frame's base view (the MVC decoder's 8-bit
// planar picture) as NV12.
func (t *picTransform) flatten(p picture) *gpu.DecodedPicture {
	f := p.stereo.Base
	d := &t.flat
	if d.Y == nil {
		d.Width, d.Height, d.Depth, d.Pitch = f.Width, f.Height, 8, f.Width
		d.Y = make([]byte, f.Width*f.Height)
		d.UV = make([]byte, f.Width*f.Height/2)
	}
	d.PTS = p.pts
	pr, tr, m, full := p.stereo.ColorInfo()
	d.Color = gpu.ColorInfo{Primaries: pr, Transfer: tr, Matrix: m, FullRange: full}
	for y := range f.Height {
		copy(d.Y[y*d.Pitch:y*d.Pitch+f.Width], f.Y[y*f.StrideY:])
	}
	for y := range f.Height / 2 {
		row := d.UV[y*d.Pitch:]
		u, v := f.Cb[y*f.StrideC:], f.Cr[y*f.StrideC:]
		for x := range f.Width / 2 {
			row[2*x], row[2*x+1] = u[x], v[x]
		}
	}
	return d
}

// reframeRPU moves a Dolby Vision RPU's active area (its level 5 bars)
// onto the transformed picture: less what was cropped, then scaled.
func (t *picTransform) reframeRPU(nal []byte) []byte {
	u, err := dovi.ParseNAL(nal)
	if err != nil {
		return nal
	}
	l, r, top, b, ok := u.ActiveArea()
	if !ok {
		return nal
	}
	c := t.crop
	sx := func(v int) int { return (max(v, 0)*t.outW + t.cw/2) / t.cw }
	sy := func(v int) int { return (max(v, 0)*t.outH + t.ch/2) / t.ch }
	u.SetActiveArea(sx(l-c.Left), sx(r-c.Right), sy(top-c.Top), sy(b-c.Bottom))
	return u.NAL()
}
