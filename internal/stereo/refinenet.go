package stereo

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/brunoga/bdtools/internal/nn"
)

// The depth refinement: a small U-Net, learnt from 3D discs' pairs
// (tools/depthtrain), from a picture and the depth network's depth of it,
// both at half the picture's size, to a correction of that depth's shape:
// the network's depth, standardised (its mean and deviation over the
// active rows), plus the correction, put back to the depth's own mean and
// deviation, so the grading (Shape) applies to it as to the depth.
//
// Its input: Y, Cb and Cr (Y halved by 2x2 averages, (Y-16)/219-0.5,
// (C-128)/112) and the standardised depth. Three levels of 3x3
// convolutions (the first of each with a stride of 2), and back up by 2x2
// transposed convolutions, each added to the level's own; ReLU after each
// but the last.

// RefineLayer is one of the refinement's layers.
type RefineLayer struct {
	Name     string
	In, Out  int
	Stride   int  // a convolution's: 1 or 2
	Up       bool // a 2x2 transposed convolution (an upsampling)
	Kernel   int  // a convolution's: 3
	Residual bool // the last: no ReLU
}

// RefineLayers is the refinement's layers, in their order.
var RefineLayers = []RefineLayer{
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
	{Name: "out", In: 16, Out: 1, Stride: 1, Kernel: 3, Residual: true},
}

// RefineMultiple is what the input's sides are padded to a multiple of.
const RefineMultiple = 8

// RefineWeights is the refinement's weights: each layer's (a convolution's
// [Out, In, k, k], a transposed one's [In, Out, 2, 2]) and its bias.
type RefineWeights struct {
	W, B map[string]*nn.Tensor
}

// Shape is a layer's weights' shape.
func (l RefineLayer) Shape() []int {
	if l.Up {
		return []int{l.In, l.Out, 2, 2}
	}
	return []int{l.Out, l.In, l.Kernel, l.Kernel}
}

const refineMagic = "BDRF1"

// ReadRefine reads refinement weights (as WriteTo writes them).
func ReadRefine(path string) (*RefineWeights, error) {
	f, err := os.Open(path) //nolint:gosec // the user's file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	magic := make([]byte, len(refineMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != refineMagic {
		return nil, fmt.Errorf("%s: not depth refinement weights", path)
	}
	rw := &RefineWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
	for _, l := range RefineLayers {
		w, b := nn.New(l.Shape()...), nn.New(l.Out)
		if err := errors.Join(binary.Read(r, binary.LittleEndian, w.Data), binary.Read(r, binary.LittleEndian, b.Data)); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		rw.W[l.Name], rw.B[l.Name] = w, b
	}
	return rw, nil
}

// WriteTo writes the weights: the magic, then each layer's weights and
// bias in order, float32 little-endian.
func (rw *RefineWeights) WriteTo(w io.Writer) (int64, error) {
	b := bufio.NewWriter(w)
	_, _ = b.WriteString(refineMagic)
	n := int64(len(refineMagic))
	for _, l := range RefineLayers {
		for _, t := range []*nn.Tensor{rw.W[l.Name], rw.B[l.Name]} {
			if err := binary.Write(b, binary.LittleEndian, t.Data); err != nil {
				return n, err
			}
			n += int64(4 * len(t.Data))
		}
	}
	return n, b.Flush()
}

// RefineInput is the refinement's input for a picture's active rows at
// half size (w x h): its Y (halved), Cb and Cr, each h rows of w, stride
// apart, and its depth (w x h); the sides padded to RefineMultiple by
// repeating the last row and column. It gives the depth's mean and
// deviation, which the refined depth is put back to.
func RefineInput(y, cb, cr []byte, stride int, depth []float32, w, h int) (x *nn.Tensor, mean, sd float64) {
	pw, ph := pad8(w), pad8(h)
	var s, ss float64
	for _, v := range depth {
		s += float64(v)
		ss += float64(v) * float64(v)
	}
	n := float64(max(len(depth), 1))
	mean = s / n
	sd = math.Sqrt(max(ss/n-mean*mean, 1e-12))
	x = nn.New(4, ph, pw)
	for r := range ph {
		sr := min(r, h-1)
		for c := range pw {
			sc := min(c, w-1)
			i := r*pw + c
			x.Data[i] = (float32(y[sr*stride+sc])-16)/219 - 0.5
			x.Data[ph*pw+i] = (float32(cb[sr*stride+sc]) - 128) / 112
			x.Data[2*ph*pw+i] = (float32(cr[sr*stride+sc]) - 128) / 112
			x.Data[3*ph*pw+i] = float32((float64(depth[sr*w+sc]) - mean) / sd)
		}
	}
	return x, mean, sd
}

func pad8(v int) int { return (v + RefineMultiple - 1) / RefineMultiple * RefineMultiple }

// Refine runs the refinement on its input (RefineInput's) on be: the
// standardised depth corrected, [1, H, W] (padded as the input is).
func (rw *RefineWeights) Refine(be nn.Backend, x *nn.Tensor) *nn.Tensor {
	var skips [3]*nn.Tensor
	h := x
	z := be.Rows(x.Reshape(4, x.Dim(1)*x.Dim(2)), 3) // the standardised depth
	for _, l := range RefineLayers {
		w, b := rw.W[l.Name], rw.B[l.Name]
		switch {
		case l.Up:
			h = be.ConvTranspose2dK(h, w, b)
			// the level's own added
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
	return be.Add(h, z.Reshape(h.Shape...))
}
