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
// (tools/depthtrain -fill), from an eye as Render makes it (its holes
// filled from the farther side), where its holes are and its disparity, to
// a correction of the holes' pixels: what the disc's other camera saw
// there, as near as the picture around tells.
//
// Its input, at full resolution: Y, Cb and Cr ((Y-16)/219-0.5,
// (C-128)/112; the chroma repeated over its 2x2), the holes (1 in one, 0
// elsewhere) and the disparity (pixels / FillDisparityScale). Four levels
// of 3x3 convolutions (the first of each but the top with a stride of 2),
// and back up by 2x2 transposed convolutions, each added to the level's
// own; ReLU after each but the last, which gives the correction of Y, Cb
// and Cr, kept in the holes only.

// FillLayers is the filler's layers, in their order (the upsamplings add
// the level of the same size: u3 e3b's, u2 e2b's, u1 e1b's).
var FillLayers = []RefineLayer{
	{Name: "e1a", In: 5, Out: 24, Stride: 1, Kernel: 3},
	{Name: "e1b", In: 24, Out: 24, Stride: 1, Kernel: 3},
	{Name: "e2a", In: 24, Out: 48, Stride: 2, Kernel: 3},
	{Name: "e2b", In: 48, Out: 48, Stride: 1, Kernel: 3},
	{Name: "e3a", In: 48, Out: 64, Stride: 2, Kernel: 3},
	{Name: "e3b", In: 64, Out: 64, Stride: 1, Kernel: 3},
	{Name: "e4a", In: 64, Out: 96, Stride: 2, Kernel: 3},
	{Name: "e4b", In: 96, Out: 96, Stride: 1, Kernel: 3},
	{Name: "u3", In: 96, Out: 64, Up: true},
	{Name: "d3", In: 64, Out: 64, Stride: 1, Kernel: 3},
	{Name: "u2", In: 64, Out: 48, Up: true},
	{Name: "d2", In: 48, Out: 48, Stride: 1, Kernel: 3},
	{Name: "u1", In: 48, Out: 24, Up: true},
	{Name: "d1", In: 24, Out: 24, Stride: 1, Kernel: 3},
	{Name: "out", In: 24, Out: 3, Stride: 1, Kernel: 3, Residual: true},
}

// FillSkips is the level each upsampling adds.
var FillSkips = map[string]string{"u3": "e3b", "u2": "e2b", "u1": "e1b"}

// FillDisparityScale is what the filler's disparity input is divided by.
const FillDisparityScale = 32

// FillChannels is the filler's input channels.
const FillChannels = 5

const fillMagic = "BDFL1"

// FillWeights is the filler's weights: each layer's (a convolution's
// [Out, In, k, k], a transposed one's [In, Out, 2, 2]) and its bias.
type FillWeights struct {
	W, B map[string]*nn.Tensor
}

// ReadFill reads filler weights (as WriteTo writes them).
func ReadFill(path string) (*FillWeights, error) {
	f, err := os.Open(path) //nolint:gosec // the user's file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fw, err := DecodeFill(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return fw, nil
}

// DecodeFill reads filler weights from r.
func DecodeFill(r io.Reader) (*FillWeights, error) {
	magic := make([]byte, len(fillMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != fillMagic {
		return nil, errors.New("not hole filler weights")
	}
	fw := &FillWeights{W: map[string]*nn.Tensor{}, B: map[string]*nn.Tensor{}}
	for _, l := range FillLayers {
		w, b := nn.New(l.Shape()...), nn.New(l.Out)
		if err := errors.Join(binary.Read(r, binary.LittleEndian, w.Data), binary.Read(r, binary.LittleEndian, b.Data)); err != nil {
			return nil, err
		}
		fw.W[l.Name], fw.B[l.Name] = w, b
	}
	return fw, nil
}

// WriteTo writes the weights: the magic, then each layer's weights and
// bias in order, float32 little-endian.
func (fw *FillWeights) WriteTo(w io.Writer) (int64, error) {
	b := bufio.NewWriter(w)
	_, _ = b.WriteString(fillMagic)
	n := int64(len(fillMagic))
	for _, l := range FillLayers {
		for _, t := range []*nn.Tensor{fw.W[l.Name], fw.B[l.Name]} {
			if err := binary.Write(b, binary.LittleEndian, t.Data); err != nil {
				return n, err
			}
			n += int64(4 * len(t.Data))
		}
	}
	return n, b.Flush()
}

// FillInput is the filler's input for the w x h window at (x0, y0) of an
// eye (8-bit 4:2:0, as Render makes it), its holes and its disparity (one
// value a luma pixel, the eye's width a row), the sides padded to
// RefineMultiple by repeating the last row and column.
func FillInput(eye *mvc.Frame, holes []bool, d []float32, x0, y0, w, h int) *nn.Tensor {
	pw, ph := pad8(w), pad8(h)
	x := nn.New(FillChannels, ph, pw)
	n := ph * pw
	ew := eye.Width
	rows(ph, func(r int) {
		sr := y0 + min(r, h-1)
		for c := range pw {
			sc := x0 + min(c, w-1)
			i := r*pw + c
			x.Data[i] = (float32(eye.Y[sr*eye.StrideY+sc])-16)/219 - 0.5
			x.Data[n+i] = (float32(eye.Cb[(sr/2)*eye.StrideC+sc/2]) - 128) / 112
			x.Data[2*n+i] = (float32(eye.Cr[(sr/2)*eye.StrideC+sc/2]) - 128) / 112
			if holes[sr*ew+sc] {
				x.Data[3*n+i] = 1
			}
			x.Data[4*n+i] = d[sr*ew+sc] / FillDisparityScale
		}
	})
	return x
}

// Fill runs the filler on its input (FillInput's, on be) on be: the
// correction of Y, Cb and Cr, [3, H, W] (padded as the input is; to be
// kept in the holes only). The weights go to be with it.
func (fw *FillWeights) Fill(be nn.Backend, x *nn.Tensor) *nn.Tensor {
	outs := map[string]*nn.Tensor{}
	h := x
	for _, l := range FillLayers {
		w, b := be.Upload(fw.W[l.Name]), be.Upload(fw.B[l.Name])
		if l.Up {
			h = be.ReLU(be.Add(be.ConvTranspose2dK(h, w, b), outs[FillSkips[l.Name]]))
		} else {
			h = be.Conv2d(h, w, b, l.Stride, 1)
			if !l.Residual {
				h = be.ReLU(h)
			}
		}
		outs[l.Name] = h
	}
	return h
}

// fillBand is how many rows the filler takes at a time: bands of the
// picture, each with fillContext rows more on either side, so that the
// work's memory stays small.
const (
	fillBand    = 256
	fillContext = 32
)

// FillEye corrects an eye's holes (Render's eye, its holes and the
// disparity it was rendered for) by the filler, on be: each band of rows
// with a hole in it, the luma in the holes and each chroma sample whose
// first luma pixel is one.
func (fw *FillWeights) FillEye(be nn.Backend, eye *mvc.Frame, holes []bool, d []float32) error {
	w, h := eye.Width, eye.Height
	for b0 := 0; b0 < h; b0 += fillBand {
		b1 := min(b0+fillBand, h)
		any := false
		for i := b0 * w; i < b1*w && !any; i++ {
			any = holes[i]
		}
		if !any {
			continue
		}
		// the band and its context, even rows so the chroma lines up
		y0 := max(b0-fillContext, 0) &^ 1
		y1 := min(b1+fillContext, h)
		x := FillInput(eye, holes, d, 0, y0, w, y1-y0)
		out := fw.Fill(be, be.Upload(x))
		t, err := be.Download(out)
		if err != nil {
			return err
		}
		ph, pw := t.Dim(1), t.Dim(2)
		n := ph * pw
		for r := b0; r < b1; r++ {
			for c := range w {
				if !holes[r*w+c] {
					continue
				}
				i := (r-y0)*pw + c
				v := (x.Data[i]+t.Data[i]+0.5)*219 + 16
				eye.Y[r*eye.StrideY+c] = byte(min(max(v+0.5, 0), 255))
				if r%2 == 0 && c%2 == 0 {
					cb := (x.Data[n+i]+t.Data[n+i])*112 + 128
					cr := (x.Data[2*n+i]+t.Data[2*n+i])*112 + 128
					eye.Cb[(r/2)*eye.StrideC+c/2] = byte(min(max(cb+0.5, 0), 255))
					eye.Cr[(r/2)*eye.StrideC+c/2] = byte(min(max(cr+0.5, 0), 255))
				}
			}
		}
		be.Release()
	}
	return nil
}
