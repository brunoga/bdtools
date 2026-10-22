package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// The depth refinement's training data (-samples DIR): for each measured
// picture, a file of what the refinement takes and what it is to give,
// all at half the picture's size (W x H):
//
//	"BDS1", W, H, top, bottom (uint32 little-endian; the active rows)
//	Y, Cb, Cr   W*H bytes each (Y the 2x2 average, the chroma as it is)
//	depth       W*H float32, the network's (NaN in the bars)
//	disparity   W*H float32, the disc's (half-size pixels, NaN unsure)

var (
	samplesDir string
	refinePath string
	refine     *stereo.RefineWeights
)

func writeSample(clip string, k int, left *mvc.Frame, dm *stereo.Disparity, hr []float32, top, bottom int) error {
	w, h := dm.W, dm.H
	f, err := os.Create(filepath.Join(samplesDir, strings.ReplaceAll(fmt.Sprintf("%s-%06d.bds", clip, k), "/", "_"))) //nolint:gosec // the operator's directory
	if err != nil {
		return err
	}
	b := bufio.NewWriterSize(f, 1<<20)
	_, _ = b.WriteString("BDS1")
	for _, v := range []uint32{uint32(w), uint32(h), uint32(top / 2), uint32(bottom / 2)} {
		_ = binary.Write(b, binary.LittleEndian, v)
	}
	y := make([]byte, w*h)
	for r := range h {
		for c := range w {
			s := int(left.Y[2*r*left.StrideY+2*c]) + int(left.Y[2*r*left.StrideY+2*c+1]) +
				int(left.Y[(2*r+1)*left.StrideY+2*c]) + int(left.Y[(2*r+1)*left.StrideY+2*c+1])
			y[r*w+c] = byte((s + 2) / 4)
		}
	}
	_, _ = b.Write(y)
	for _, p := range [][]byte{left.Cb, left.Cr} {
		for r := range h {
			_, _ = b.Write(p[r*left.StrideC : r*left.StrideC+w])
		}
	}
	nan := float32(math.NaN())
	d := make([]float32, w*h)
	for i := range d {
		d[i] = nan
		if dm.Valid[i] {
			d[i] = dm.D[i]
		}
	}
	_ = binary.Write(b, binary.LittleEndian, hr)
	_ = binary.Write(b, binary.LittleEndian, d)
	return errors.Join(b.Flush(), f.Close())
}
