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
	Guide           Guide
	cuts            cutDetector
	top, bottom     int // the active rows
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
	return left, right, nil
}

// Next gives the next picture's depth and disparity: depth, the network's
// (larger nearer, in its own units), NaN in the letterbox bars; disparity,
// shaped from it (zero in the bars), in pixels, as Pair takes it. The
// error is the depth model's backend's (a GPU's).
func (c *Converter) Next(f *mvc.Frame) (depth, disparity []float32, err error) {
	top, bottom := bars(f)
	cut := c.cuts.next(f)
	if c.frames == 0 || cut || top != c.top || bottom != c.bottom {
		if c.frames > 0 && cut {
			c.cutsHit++
		}
		c.Model.Reset()
		c.Reset()
		c.top, c.bottom = top, bottom
		c.netW, c.netH = netSize(f.Width, bottom-top)
	}
	c.frames++
	w, h := f.Width, f.Height
	depth = make([]float32, w*h)
	disparity = make([]float32, w*h)
	for i := range depth {
		depth[i] = float32(math.NaN())
	}
	if ah := bottom - top; ah >= 28 {
		x := netInput(f, top, bottom, c.netW, c.netH)
		dm, err := c.Model.FrameErr(x)
		if err != nil {
			return nil, nil, err
		}
		up := nn.Bilinear(dm.Reshape(1, c.netH, c.netW), ah, w)
		c.Guide.guided(up.Data, f.Y[top*f.StrideY:], f.StrideY, w, ah)
		copy(depth[top*w:bottom*w], up.Data)
		copy(disparity[top*w:bottom*w], c.Disparity(up.Data, w))
	}
	return depth, disparity, nil
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
