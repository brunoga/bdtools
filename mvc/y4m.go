package mvc

import (
	"bufio"
	"fmt"
	"io"
)

// Layout is how the two views of a stereo frame are arranged in a Y4M frame.
type Layout string

const (
	// LayoutSideBySide puts the base view on the left and the dependent view
	// on the right, at full width each (3840x1080 from a 1080p pair).
	LayoutSideBySide Layout = "sbs"
	// LayoutTopBottom stacks the views, base view on top.
	LayoutTopBottom Layout = "tab"
	// LayoutBase writes the base view only.
	LayoutBase Layout = "base"
	// LayoutDependent writes the dependent view only (the base view when an
	// access unit has none).
	LayoutDependent Layout = "dep"
)

// Valid reports whether l is a known layout.
func (l Layout) Valid() bool {
	switch l {
	case LayoutSideBySide, LayoutTopBottom, LayoutBase, LayoutDependent:
		return true
	}
	return false
}

// Y4MWriter writes stereo frames as a YUV4MPEG2 stream.
type Y4MWriter struct {
	w      *bufio.Writer
	layout Layout
	// SwapViews exchanges the two views, for a disc whose base view is the
	// right eye: the dependent view then comes first (left, or top).
	SwapViews bool
	// FPSNum and FPSDen are the frame rate written in the stream header.
	// They are read when the first frame is written, so a caller can fill
	// them from Decoder.FrameRate once the stream's parameter sets have been
	// seen.
	FPSNum, FPSDen int
	// Depth is the sample depth written: 8 (or 0), or 10, which writes each
	// sample as the 10-bit value four times it, two bytes little-endian
	// (C420p10), for an encoder that is to work at 10 bits.
	Depth   int
	header  bool
	err     error
	scratch []byte
}

// NewY4MWriter returns a writer emitting frames in the given layout. The
// frame rate defaults to 24000/1001 until the fields are set.
func NewY4MWriter(w io.Writer, layout Layout) *Y4MWriter {
	return &Y4MWriter{w: bufio.NewWriterSize(w, 8<<20), layout: layout, FPSNum: 24000, FPSDen: 1001}
}

// Write emits one stereo frame. A 2D access unit (no dependent view) has its
// base view used for both.
func (y *Y4MWriter) Write(sf *StereoFrame) error {
	if y.err != nil {
		return y.err
	}
	a, b := sf.Base, sf.Dependent
	if b == nil {
		b = a
	}
	if y.SwapViews {
		a, b = b, a
	}
	switch y.layout {
	case LayoutBase:
		b = a
	case LayoutDependent:
		a = b
	}
	w, h := a.Width, a.Height
	switch y.layout {
	case LayoutSideBySide:
		w *= 2
	case LayoutTopBottom:
		h *= 2
	}
	if !y.header {
		num, den := y.FPSNum, y.FPSDen
		if num <= 0 || den <= 0 {
			num, den = 24000, 1001
		}
		colour := "C420jpeg"
		if y.Depth == 10 {
			colour = "C420p10"
		}
		y.put(fmt.Appendf(nil, "YUV4MPEG2 W%d H%d F%d:%d Ip A1:1 %s\n", w, h, num, den, colour))
		y.header = true
	}
	y.put([]byte("FRAME\n"))
	switch y.layout {
	case LayoutSideBySide:
		for row := 0; row < a.Height; row++ {
			y.samples(a.Y[row*a.StrideY : row*a.StrideY+a.Width])
			y.samples(b.Y[row*b.StrideY : row*b.StrideY+b.Width])
		}
		for c := 0; c < 2; c++ {
			pa, pb := a.Cb, b.Cb
			if c == 1 {
				pa, pb = a.Cr, b.Cr
			}
			for row := 0; row < a.Height/2; row++ {
				y.samples(pa[row*a.StrideC : row*a.StrideC+a.Width/2])
				y.samples(pb[row*b.StrideC : row*b.StrideC+b.Width/2])
			}
		}
	case LayoutTopBottom:
		for _, f := range []*Frame{a, b} {
			for row := 0; row < f.Height; row++ {
				y.samples(f.Y[row*f.StrideY : row*f.StrideY+f.Width])
			}
		}
		for c := 0; c < 2; c++ {
			for _, f := range []*Frame{a, b} {
				p := f.Cb
				if c == 1 {
					p = f.Cr
				}
				for row := 0; row < f.Height/2; row++ {
					y.samples(p[row*f.StrideC : row*f.StrideC+f.Width/2])
				}
			}
		}
	default:
		for row := 0; row < a.Height; row++ {
			y.samples(a.Y[row*a.StrideY : row*a.StrideY+a.Width])
		}
		for _, p := range [][]byte{a.Cb, a.Cr} {
			for row := 0; row < a.Height/2; row++ {
				y.samples(p[row*a.StrideC : row*a.StrideC+a.Width/2])
			}
		}
	}
	return y.err
}

// samples buffers a row of samples at the writer's depth.
func (y *Y4MWriter) samples(row []byte) {
	if y.Depth != 10 {
		y.put(row)
		return
	}
	if cap(y.scratch) < 2*len(row) {
		y.scratch = make([]byte, 2*len(row))
	}
	d := y.scratch[:2*len(row)]
	for i, v := range row {
		d[2*i], d[2*i+1] = v<<2, v>>6
	}
	y.put(d)
}

// put buffers p. bufio keeps the first error and returns it on every later
// call, so one check per frame is enough; that is what y.err records.
func (y *Y4MWriter) put(p []byte) {
	if y.err == nil {
		_, y.err = y.w.Write(p)
	}
}

// Flush writes out buffered data.
func (y *Y4MWriter) Flush() error {
	if y.err != nil {
		return y.err
	}
	return y.w.Flush()
}

// WritePlanes writes a frame as raw planar YUV 4:2:0 (I420).
func WritePlanes(w io.Writer, f *Frame) error {
	for y := 0; y < f.Height; y++ {
		if _, err := w.Write(f.Y[y*f.StrideY : y*f.StrideY+f.Width]); err != nil {
			return err
		}
	}
	for _, p := range [][]byte{f.Cb, f.Cr} {
		for y := 0; y < f.Height/2; y++ {
			if _, err := w.Write(p[y*f.StrideC : y*f.StrideC+f.Width/2]); err != nil {
				return err
			}
		}
	}
	return nil
}
