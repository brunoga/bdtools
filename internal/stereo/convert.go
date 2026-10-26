package stereo

import (
	"math"

	"github.com/brunoga/bdtools/internal/depth"
	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/mvc"
)

// Converter makes the stereo pairs of a 2D film's pictures, one picture
// after the other: the depth of the picture's active area (letterbox bars
// left out, at the screen) by the depth network, shaped into disparity,
// the eyes rendered from it. A cut, or bars that change, starts the
// network's and the shaping's state afresh.
type Converter struct {
	Model *depth.Model
	Shaper
	// Guide snaps the depth's edges to the picture's (DefaultGuide; the
	// zero Guide leaves the depth as the network gives it, upsampled).
	Guide Guide
	// NoWindow leaves out the floating window (window.go).
	NoWindow bool
	// KeepCredits gives the credits (credits.go) their depth as any
	// picture's, not flat.
	KeepCredits bool
	// Refine, when not nil, corrects the depth's shape (refinenet.go), on
	// the depth model's backend.
	Refine          *RefineWeights
	refineOn        *RefineWeights // Refine's weights on that backend
	win             window
	credits         creditState
	cuts            cutDetector
	top, bottom     int // the active rows (Estimate's)
	shapeTop        int // the active rows of the picture Shape did last
	shapeBottom     int
	netW, netH      int // the network's input size
	frames, cutsHit int
}

// NewConverter is a converter for a depth model.
func NewConverter(m *depth.Model, shape Shape) *Converter {
	return &Converter{Model: m, Shaper: Shaper{Shape: shape}, Guide: DefaultGuide, top: -1}
}

// Cuts is how many cuts the converter has seen.
func (c *Converter) Cuts() int { return c.cutsHit }

// Convert gives the pair for the next picture (8-bit 4:2:0).
func (c *Converter) Convert(f *mvc.Frame) (left, right *mvc.Frame, err error) {
	_, d, err := c.Next(f)
	if err != nil {
		return nil, nil, err
	}
	left, right = Pair(f, d)
	c.Window(left, right, SamplesOf(d, f.Width, f.Height, c.shapeTop, c.shapeBottom))
	return left, right, nil
}

// Window puts the floating window on the pair made of the picture Next,
// Shape or Eyes did last, for its disparity's samples s (unless NoWindow).
func (c *Converter) Window(left, right *mvc.Frame, s *Samples) {
	if !c.NoWindow {
		c.win.apply(left, right, s)
	}
}

// Next gives the next picture's depth and disparity: depth, the network's
// (larger nearer, in its own units), NaN in the letterbox bars; disparity,
// shaped from it (zero in the bars), in pixels, as Pair takes it. The
// error is the depth model's backend's (a GPU's). It is Estimate then
// Shape.
func (c *Converter) Next(f *mvc.Frame) (depth, disparity []float32, err error) {
	e, err := c.estimate(f, false)
	if err != nil {
		return nil, nil, err
	}
	depth, disparity = c.Shape(e)
	return depth, disparity, nil
}

// An Estimate is a picture's depth as the network (and the refinement)
// give it, at their size: Estimate's work, on the depth model's backend,
// the pictures one after the other; Shape's, on the CPU, may run beside
// the next picture's Estimate.
type Estimate struct {
	f           *mvc.Frame
	top, bottom int
	d           *nn.Tensor // [1, h, w] of the active rows (nil: none)
	fresh       bool       // a new shot: the shaping starts afresh
	factor      float32    // of the disparity: 0 flat (the credits), 1 as shaped
	left, right *mvc.Frame // the eyes, when the GPU made them
	samples     *Samples   // of the disparity they were made with
}

// Estimate is the first half of Next: the picture's depth, by the
// network and the refinement; on a GPU, the eyes too when it can make
// them (Eyes gives them).
func (c *Converter) Estimate(f *mvc.Frame) (*Estimate, error) { return c.Start(f).Wait() }

// Pending is an Estimate under way (Start's): Wait gives it.
type Pending struct {
	e    *Estimate
	err  error
	rest func() error // the GPU's part still to wait for
}

// Wait gives the Estimate once made.
func (p *Pending) Wait() (*Estimate, error) {
	if p.rest != nil {
		p.err, p.rest = p.rest(), nil
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.e, nil
}

// Start is Estimate begun: on a GPU, its work queued and not waited for,
// so that the caller's own (the next picture's decoding) runs beside it.
// One at a time: the next Start after this one's Wait.
func (c *Converter) Start(f *mvc.Frame) *Pending { return c.start(f, true) }

// estimate is Estimate, the eyes made on a GPU only if asked.
func (c *Converter) estimate(f *mvc.Frame, eyes bool) (*Estimate, error) {
	return c.start(f, eyes).Wait()
}

// start is Start, the eyes made on a GPU only if asked.
func (c *Converter) start(f *mvc.Frame, eyes bool) *Pending {
	top, bottom := bars(f)
	cut := c.cuts.next(f)
	e := &Estimate{f: f, top: top, bottom: bottom}
	if c.frames == 0 || cut || top != c.top || bottom != c.bottom {
		if c.frames > 0 && cut {
			c.cutsHit++
		}
		c.Model.Reset()
		e.fresh = true
		c.top, c.bottom = top, bottom
		c.netW, c.netH = netSize(f.Width, bottom-top)
	}
	c.frames++
	e.factor = 1
	if !c.KeepCredits {
		e.factor = c.credits.next(Credits(f, top, bottom), e.fresh)
	}
	if ah := bottom - top; ah >= 28 {
		if g, ok := c.Model.Backend().(*nn.GPU); ok {
			return &Pending{e: e, rest: c.startGPU(g, e, eyes && c.gpuEyes())}
		}
		x := netInput(f, top, bottom, c.netW, c.netH)
		dm, err := c.Model.FrameErr(x)
		if err != nil {
			return &Pending{err: err}
		}
		e.d = dm.Reshape(1, c.netH, c.netW)
		if c.Refine != nil {
			if e.d, err = c.refine(c.Model.Backend(), e.d, f, top, bottom); err != nil {
				return &Pending{err: err}
			}
		}
	}
	return &Pending{e: e}
}

// Shape is the second half of Next: an Estimate's depth upsampled to the
// picture and shaped into disparity. The Estimates must come in order;
// one whose eyes the GPU made has no depth to shape and goes to Eyes.
func (c *Converter) Shape(e *Estimate) (depth, disparity []float32) {
	if e.fresh {
		c.Reset()
		c.win.reset()
	}
	f, top, bottom := e.f, e.top, e.bottom
	c.shapeTop, c.shapeBottom = top, bottom
	w, h := f.Width, f.Height
	depth = make([]float32, w*h)
	disparity = make([]float32, w*h)
	nan := float32(math.NaN())
	rows(h, func(y int) {
		if y < top || y >= bottom || e.d == nil {
			row := depth[y*w : (y+1)*w]
			for i := range row {
				row[i] = nan
			}
		}
	})
	if e.d != nil {
		ah := bottom - top
		up := nn.Bilinear(e.d, ah, w)
		c.Guide.guided(up.Data, f.Y[top*f.StrideY:], f.StrideY, w, ah)
		copy(depth[top*w:bottom*w], up.Data)
		d := c.Disparity(up.Data, w)
		if e.factor != 1 {
			for i := range d {
				d[i] *= e.factor
			}
		}
		copy(disparity[top*w:bottom*w], d)
	}
	return depth, disparity
}

// refine corrects the depth dm (the network's, [1, h, w]) of the active
// rows [top, bottom) of f by the refinement on be, at half the picture's
// size: the corrected depth, [1, ah/2, w/2].
func (c *Converter) refine(be nn.Backend, dm *nn.Tensor, f *mvc.Frame, top, bottom int) (*nn.Tensor, error) {
	c.uploadRefine(be)
	w, ah := f.Width, bottom-top
	hw, hh := w/2, ah/2
	d := nn.Bilinear(dm, hh, hw).Data
	sc := f.StrideC // the half-size luma at the chroma's stride
	y := make([]byte, hh*sc)
	rows(hh, func(r int) {
		p := f.Y[(top+2*r)*f.StrideY:]
		q := f.Y[(top+2*r+1)*f.StrideY:]
		for x := range hw {
			y[r*sc+x] = byte((int(p[2*x]) + int(p[2*x+1]) + int(q[2*x]) + int(q[2*x+1]) + 2) / 4)
		}
	})
	co := top / 2 * sc
	x, mean, sd := RefineInput(y, f.Cb[co:], f.Cr[co:], sc, d, hw, hh)
	out, err := be.Download(c.refineOn.Refine(be, be.Upload(x)))
	be.Release()
	if err != nil {
		return nil, err
	}
	// the refined depth put back to the depth's mean and deviation
	pw := x.Dim(2)
	var s, ss float64
	for r := range hh {
		for _, v := range out.Data[r*pw : r*pw+hw] {
			s += float64(v)
			ss += float64(v) * float64(v)
		}
	}
	n := float64(hw * hh)
	m := s / n
	k := sd / math.Sqrt(max(ss/n-m*m, 1e-12))
	rows(hh, func(r int) {
		for x, v := range out.Data[r*pw : r*pw+hw] {
			d[r*hw+x] = float32(mean + (float64(v)-m)*k)
		}
	})
	return &nn.Tensor{Shape: []int{1, hh, hw}, Data: d}, nil
}

// uploadRefine puts the refinement's weights on the backend, once.
func (c *Converter) uploadRefine(be nn.Backend) {
	if c.refineOn != nil {
		return
	}
	c.refineOn = &RefineWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
	for _, l := range RefineLayers {
		for _, p := range []struct{ to, from map[string]*nn.Tensor }{{c.refineOn.W, c.Refine.W}, {c.refineOn.B, c.Refine.B}} {
			t := be.Upload(p.from[l.Name])
			be.Keep(t)
			p.to[l.Name] = t
		}
	}
}

// startGPU is Estimate's depth on a GPU, refined if the converter
// refines: the picture goes to the GPU, the depth comes back, and what
// is between (the network's input, the refinement's, its output put back
// to the depth's mean and deviation) is the GPU's, as netInput, refine
// and RefineInput do it on the CPU. When the eyes' making needs nothing
// of the pictures before (gpuEyes), the GPU makes them too, as Shape and
// Pair would, when asked (makeEyes). The work is queued; the function it
// gives waits for it and takes its results.
func (c *Converter) startGPU(g *nn.GPU, e *Estimate, makeEyes bool) func() error {
	f, top, bottom := e.f, e.top, e.bottom
	p := g.UploadPicture(f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
	d := c.Model.Infer(g.NetInput(p, top, bottom-top, c.netW, c.netH))
	if c.Refine != nil {
		d = c.refineGPU(g, p, d, top, bottom)
	}
	var eyes, disp *nn.Tensor
	if makeEyes {
		w := float64(f.Width)
		a, b, lim := float32(c.Shaper.Shape.Scale*w), float32(c.Shaper.Shape.Offset*w), float32(0.04*w)
		a, b = a*e.factor, b*e.factor
		disp = g.Disparity(g.Bilinear(d, bottom-top, f.Width), f.Width, f.Height, top, bottom, a, b, lim)
		eyes = g.Eyes(p, disp, gapPixels)
		disp = g.Samples(disp, f.Width, f.Height, top, bottom, sampleEdge(f.Width))
	}
	return func() error {
		var out *nn.Tensor
		var err error
		if eyes == nil {
			out, err = g.Download(d)
		} else {
			buf := make([]byte, nn.EyesSize(f.Width, f.Height))
			var dt *nn.Tensor
			if err = g.DownloadBytes(buf, eyes); err == nil {
				dt, err = g.Download(disp)
			}
			if err == nil {
				e.left, e.right = eyeFrames(buf, f)
				e.samples = samplesSplit(dt.Data, f.Width, top, bottom)
			}
		}
		g.Release()
		e.d = out
		return err
	}
}

// gpuEyes reports whether Estimate makes the eyes on a GPU: when the
// disparity is the depth's as it is (Shape.Scale), the depth unguided.
func (c *Converter) gpuEyes() bool { return c.Shaper.Shape.Scale != 0 && c.Guide.Radius <= 0 }

// eyeFrames is the two eyes of f in buf (nn.GPU.Eyes').
func eyeFrames(buf []byte, f *mvc.Frame) (left, right *mvc.Frame) {
	w, h := f.Width, f.Height
	cw, ch := (w+1)/2, (h+1)/2
	eye := func(b []byte) *mvc.Frame {
		return &mvc.Frame{Y: b[: w*h : w*h], Cb: b[w*h : w*h+cw*ch : w*h+cw*ch], Cr: b[w*h+cw*ch : w*h+2*cw*ch],
			StrideY: w, StrideC: cw, Width: w, Height: h, PTS: f.PTS}
	}
	n := len(buf) / 2
	return eye(buf[:n:n]), eye(buf[n:])
}

// Eyes is Shape and Pair of an Estimate (the GPU's, when it made them):
// the picture's two eyes and their disparity's samples. The Estimates
// must come in order.
func (c *Converter) Eyes(e *Estimate) (left, right *mvc.Frame, s *Samples) {
	if e.left == nil {
		_, d := c.Shape(e)
		left, right = Pair(e.f, d)
		return left, right, SamplesOf(d, e.f.Width, e.f.Height, e.top, e.bottom)
	}
	if e.fresh {
		c.Reset()
		c.win.reset()
	}
	c.shapeTop, c.shapeBottom = e.top, e.bottom
	return e.left, e.right, e.samples
}

// refineGPU is refine on a GPU, the picture p and the depth dm there.
func (c *Converter) refineGPU(g *nn.GPU, p *nn.Picture, dm *nn.Tensor, top, bottom int) *nn.Tensor {
	c.uploadRefine(g)
	hw, hh := p.Width()/2, (bottom-top)/2
	pw, ph := pad8(hw), pad8(hh)
	d := g.Bilinear(dm, hh, hw)
	want := g.Moments(d, hh, hw, hw)
	out := c.refineOn.Refine(g, g.RefineInput(p, top, d, want, hw, hh, pw, ph))
	return g.Renorm(out, hw, hh, pw, want, g.Moments(out, hh, hw, pw))
}

// Bars finds a picture's letterbox bars: its active rows are [top,
// bottom).
func Bars(f *mvc.Frame) (top, bottom int) { return bars(f) }

// netSize is the network's input for an active area: 518 on the shorter
// side (less for an area wider than 16:9, as the reference does to bound
// its memory), the longer side kept in proportion, both multiples of 14
// at least that large.
func netSize(w, h int) (int, int) {
	size := 518
	ratio := float64(max(w, h)) / float64(max(min(w, h), 1))
	if ratio > 1.78 {
		size = int(float64(size) * 1.777 / ratio)
		size = int(math.Round(float64(size)/14)) * 14
	}
	scale := math.Max(float64(size)/float64(w), float64(size)/float64(h))
	up := func(v float64) int { return int(math.Ceil(v/14)) * 14 }
	return max(up(float64(w)*scale), 14), max(up(float64(h)*scale), 14)
}

// netInput is the active rows of f as the network takes them: RGB (BT.709,
// limited range), resized bilinearly to nw x nh, normalised by ImageNet's
// mean and deviation.
func netInput(f *mvc.Frame, top, bottom, nw, nh int) *nn.Tensor {
	x := nn.New(3, nh, nw)
	w, ah := f.Width, bottom-top
	mean := [3]float32{0.485, 0.456, 0.406}
	std := [3]float32{0.229, 0.224, 0.225}
	sample := func(plane []byte, stride, pw, ph int, fx, fy float64) float32 {
		x0, y0 := int(fx), int(fy)
		x1, y1 := min(x0+1, pw-1), min(y0+1, ph-1)
		ax, ay := float32(fx-float64(x0)), float32(fy-float64(y0))
		p := func(x, y int) float32 { return float32(plane[y*stride+x]) }
		t := p(x0, y0)*(1-ax) + p(x1, y0)*ax
		b := p(x0, y1)*(1-ax) + p(x1, y1)*ax
		return t*(1-ay) + b*ay
	}
	rows(nh, func(oy int) {
		fy := math.Max((float64(oy)+0.5)*float64(ah)/float64(nh)-0.5, 0) + float64(top)
		fyc := math.Max(((fy+0.5)/2)-0.5, 0)
		for ox := range nw {
			fx := math.Max((float64(ox)+0.5)*float64(w)/float64(nw)-0.5, 0)
			fxc := math.Max(((fx+0.5)/2)-0.5, 0)
			yv := sample(f.Y, f.StrideY, w, f.Height, fx, fy) - 16
			cb := sample(f.Cb, f.StrideC, (w+1)/2, (f.Height+1)/2, fxc, fyc) - 128
			cr := sample(f.Cr, f.StrideC, (w+1)/2, (f.Height+1)/2, fxc, fyc) - 128
			rgb := [3]float32{1.164*yv + 1.793*cr, 1.164*yv - 0.213*cb - 0.533*cr, 1.164*yv + 2.112*cb}
			for c, v := range rgb {
				v = min(max(v/255, 0), 1)
				x.Data[(c*nh+oy)*nw+ox] = (v - mean[c]) / std[c]
			}
		}
	})
	return x
}

// bars finds the letterbox bars: the rows at the top and bottom that are
// black (none of their samples above 30), giving the active rows [top,
// bottom); a picture black all over is active all over.
func bars(f *mvc.Frame) (int, int) {
	black := func(y int) bool {
		row := f.Y[y*f.StrideY : y*f.StrideY+f.Width]
		for x := 0; x < len(row); x += 4 {
			if row[x] > 30 {
				return false
			}
		}
		return true
	}
	top, bottom := 0, f.Height
	for top < bottom && black(top) {
		top++
	}
	for bottom > top && black(bottom-1) {
		bottom--
	}
	if bottom-top < f.Height/4 {
		return 0, f.Height
	}
	// even rows, so the chroma rows follow them
	return top &^ 1, (bottom + 1) &^ 1
}

// cutDetector finds cuts: a picture whose 32x18 thumbnail differs from the
// one before by more than a threshold on average.
type cutDetector struct {
	prev []float32
}

const cutThreshold = 28

func (c *cutDetector) next(f *mvc.Frame) bool {
	const tw, th = 32, 18
	thumb := make([]float32, tw*th)
	for ty := range th {
		for tx := range tw {
			y0, y1 := ty*f.Height/th, (ty+1)*f.Height/th
			x0, x1 := tx*f.Width/tw, (tx+1)*f.Width/tw
			var s float32
			n := 0
			for y := y0; y < y1; y += 4 {
				for x := x0; x < x1; x += 4 {
					s += float32(f.Y[y*f.StrideY+x])
					n++
				}
			}
			thumb[ty*tw+tx] = s / float32(max(n, 1))
		}
	}
	cut := false
	if c.prev != nil {
		var d float32
		for i, v := range thumb {
			d += float32(math.Abs(float64(v - c.prev[i])))
		}
		cut = d/float32(len(thumb)) > cutThreshold
	}
	c.prev = thumb
	return cut
}
