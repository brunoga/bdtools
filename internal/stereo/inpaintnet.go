package stereo

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/mvc"
)

// The hole filler: a small U-Net, learnt from 3D discs' pairs
// (tools/inpainttrain), from a rendered eye and the mask of its holes
// (what the eye saw nothing of, filled from the side by Render) to what
// belongs in the holes: the eye plus a correction, taken in the holes
// only. The refinement's shape of network (RefineLayer), at the
// picture's full size.
//
// Its input: Y, Cb and Cr ((Y-16)/219-0.5, (C-128)/112, the chroma
// doubled to the luma's size) and the mask (1 in a hole). Its output: a
// correction to each of Y, Cb and Cr, in the same units.

// InpaintLayers is the filler's layers, in their order.
var InpaintLayers = []RefineLayer{
	{Name: "e1a", In: 4, Out: 16, Stride: 1, Kernel: 3},
	{Name: "e1b", In: 16, Out: 16, Stride: 1, Kernel: 3},
	{Name: "e2a", In: 16, Out: 32, Stride: 2, Kernel: 3},
	{Name: "e2b", In: 32, Out: 32, Stride: 1, Kernel: 3},
	{Name: "e3a", In: 32, Out: 48, Stride: 2, Kernel: 3},
	{Name: "e3b", In: 48, Out: 48, Stride: 1, Kernel: 3},
	{Name: "e4a", In: 48, Out: 64, Stride: 2, Kernel: 3},
	{Name: "e4b", In: 64, Out: 64, Stride: 1, Kernel: 3},
	{Name: "u3", In: 64, Out: 48, Up: true},
	{Name: "d3", In: 48, Out: 48, Stride: 1, Kernel: 3},
	{Name: "u2", In: 48, Out: 32, Up: true},
	{Name: "d2", In: 32, Out: 32, Stride: 1, Kernel: 3},
	{Name: "u1", In: 32, Out: 16, Up: true},
	{Name: "d1", In: 16, Out: 16, Stride: 1, Kernel: 3},
	{Name: "out", In: 16, Out: 3, Stride: 1, Kernel: 3, Residual: true},
}

// InpaintWeights is the filler's weights, each layer's as RefineWeights'.
type InpaintWeights struct {
	W, B map[string]*nn.Tensor
}

const inpaintMagic = "BDIP1"

// ReadInpaint reads the filler's weights (as WriteTo writes them).
func ReadInpaint(path string) (*InpaintWeights, error) {
	f, err := os.Open(path) //nolint:gosec // the user's file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	iw, err := DecodeInpaint(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return iw, nil
}

// DecodeInpaint reads the filler's weights from r.
func DecodeInpaint(r io.Reader) (*InpaintWeights, error) {
	magic := make([]byte, len(inpaintMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != inpaintMagic {
		return nil, errors.New("not hole filler weights")
	}
	iw := &InpaintWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
	for _, l := range InpaintLayers {
		w, b := nn.New(l.Shape()...), nn.New(l.Out)
		if err := errors.Join(binary.Read(r, binary.LittleEndian, w.Data), binary.Read(r, binary.LittleEndian, b.Data)); err != nil {
			return nil, err
		}
		iw.W[l.Name], iw.B[l.Name] = w, b
	}
	return iw, nil
}

// WriteTo writes the weights: the magic, then each layer's weights and
// bias in order, float32 little-endian.
func (iw *InpaintWeights) WriteTo(w io.Writer) (int64, error) {
	b := bufio.NewWriter(w)
	_, _ = b.WriteString(inpaintMagic)
	n := int64(len(inpaintMagic))
	for _, l := range InpaintLayers {
		for _, t := range []*nn.Tensor{iw.W[l.Name], iw.B[l.Name]} {
			if err := binary.Write(b, binary.LittleEndian, t.Data); err != nil {
				return n, err
			}
			n += int64(4 * len(t.Data))
		}
	}
	return n, b.Flush()
}

// InpaintInput is the filler's input for an eye's rows [top, bottom) (w
// wide: its Y, and its Cb and Cr at half size, strideY and strideC
// apart) and its holes (w x h of the whole eye, true in a hole): [4,
// H, W], the sides padded to RefineMultiple by repeating the last row
// and column.
func InpaintInput(eye *mvc.Frame, holes []bool, top, bottom int) *nn.Tensor {
	w, h := eye.Width, bottom-top
	pw, ph := pad8(w), pad8(h)
	x := nn.New(4, ph, pw)
	rows(ph, func(r int) {
		sr := top + min(r, h-1)
		yr := eye.Y[sr*eye.StrideY:]
		cbr, crr := eye.Cb[(sr/2)*eye.StrideC:], eye.Cr[(sr/2)*eye.StrideC:]
		hr := holes[sr*w:]
		for c := range pw {
			sc := min(c, w-1)
			i := r*pw + c
			x.Data[i] = (float32(yr[sc])-16)/219 - 0.5
			x.Data[ph*pw+i] = (float32(cbr[sc/2]) - 128) / 112
			x.Data[2*ph*pw+i] = (float32(crr[sc/2]) - 128) / 112
			if hr[sc] {
				x.Data[3*ph*pw+i] = 1
			}
		}
	})
	return x
}

// Inpaint runs the filler on its input (InpaintInput's) on be: the
// corrections to Y, Cb and Cr, [3, H, W] (padded as the input is).
func (iw *InpaintWeights) Inpaint(be nn.Backend, x *nn.Tensor) *nn.Tensor {
	var skips [3]*nn.Tensor
	h := x
	for _, l := range InpaintLayers {
		w, b := iw.W[l.Name], iw.B[l.Name]
		switch {
		case l.Up:
			h = be.ConvTranspose2dK(h, w, b)
			lvl := map[string]int{"u3": 2, "u2": 1, "u1": 0}[l.Name]
			h = be.ReLU(be.Add(h, skips[lvl]))
		default:
			h = be.Conv2d(h, w, b, l.Stride, 1)
			if !l.Residual {
				h = be.ReLU(h)
			}
		}
		switch l.Name {
		case "e1b":
			skips[0] = h
		case "e2b":
			skips[1] = h
		case "e3b":
			skips[2] = h
		}
	}
	return h
}

// ApplyInpaint takes the filler's corrections (Inpaint's, downloaded:
// [3, H, W] for the rows [top, bottom) of eye, padded) into eye's holes:
// a luma pixel that is a hole, a chroma sample any of whose luma pixels
// is.
func ApplyInpaint(eye *mvc.Frame, holes []bool, delta []float32, top, bottom int) {
	w, h := eye.Width, bottom-top
	pw, ph := pad8(w), pad8(h)
	clip := func(v float32) byte { return byte(min(max(v+0.5, 0), 255)) }
	rows(h, func(r int) {
		sr := top + r
		yr, hr := eye.Y[sr*eye.StrideY:], holes[sr*w:]
		for c := range w {
			if hr[c] {
				yr[c] = clip(float32(yr[c]) + 219*delta[r*pw+c])
			}
		}
	})
	rows((h+1)/2, func(cr int) {
		sr := top/2 + cr
		cbr, crr := eye.Cb[sr*eye.StrideC:], eye.Cr[sr*eye.StrideC:]
		r0, r1 := 2*cr, min(2*cr+1, h-1)
		for cc := range (w + 1) / 2 {
			c0, c1 := 2*cc, min(2*cc+1, w-1)
			hole := false
			var dcb, dcr float32
			for _, r := range []int{r0, r1} {
				for _, c := range []int{c0, c1} {
					if holes[(top+r)*w+c] {
						hole = true
					}
					dcb += delta[ph*pw+r*pw+c]
					dcr += delta[2*ph*pw+r*pw+c]
				}
			}
			if hole {
				cbr[cc] = clip(float32(cbr[cc]) + 112*dcb/4)
				crr[cc] = clip(float32(crr[cc]) + 112*dcr/4)
			}
		}
	})
}

// Filler is the hole filler ready to run on a backend: its weights
// uploaded once.
type Filler struct {
	w  *InpaintWeights
	on *InpaintWeights // the weights on the backend
	be nn.Backend
}

// NewFiller is a filler with weights.
func NewFiller(w *InpaintWeights) *Filler { return &Filler{w: w} }

// Fill corrects the holes of an eye's rows [top, bottom) on be: the
// filler run on the eye and its holes (Render's), its corrections taken
// in the holes. On a GPU the caller releases the pass's memory
// (nn.GPU.Release) after; the weights stay.
func (f *Filler) Fill(be nn.Backend, eye *mvc.Frame, holes []bool, top, bottom int) error {
	if f.on == nil || f.be != be {
		f.on = &InpaintWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
		for _, l := range InpaintLayers {
			for _, p := range []struct{ to, from map[string]*nn.Tensor }{{f.on.W, f.w.W}, {f.on.B, f.w.B}} {
				t := be.Upload(p.from[l.Name])
				be.Keep(t)
				p.to[l.Name] = t
			}
		}
		f.be = be
	}
	if bottom-top < 8 {
		return nil
	}
	x := InpaintInput(eye, holes, top, bottom)
	d, err := be.Download(f.on.Inpaint(be, be.Upload(x)))
	if err != nil {
		return err
	}
	ApplyInpaint(eye, holes, d.Data, top, bottom)
	return nil
}
