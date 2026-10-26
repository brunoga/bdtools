package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// The hole filler's training data (-fill-samples DIR): the disc's left
// eye rendered to the right with the disc's own disparity (stereo.Dense
// of the measured one, as stereo.Redepth uses), the holes filled as
// Render fills them; around the holes, crops of what the filler takes and
// what it is to give, the disc's right eye. A crop is kept where the rest
// of it agrees with the disc's eye (the disparity right there), so that
// what is learnt is the filling, not the matcher's misses. Each file,
// fillCrop pixels square, all at full resolution:
//
//	"BFS1", size (uint32 little-endian)
//	Y, Cb, Cr   the rendered eye (the chroma repeated over its 2x2)
//	mask        1 where a hole was filled, 0 elsewhere
//	disparity   float32, the disc's (pixels)
//	Y, Cb, Cr   the disc's right eye

// fillDir is -fill-samples's directory.
var fillDir string

// filler is -fill's weights: the rendered eyes' holes corrected by it.
var filler *stereo.FillWeights

// fillEye corrects a rendered eye's holes by -fill's filler (on the GPU
// when there is one), as it is when there is none.
func fillEye(eye *mvc.Frame, holes []bool, d []float32) error {
	if filler == nil {
		return nil
	}
	be := nn.CPU
	if matchGPU != nil {
		be = matchGPU
	}
	return filler.FillEye(be, eye, holes, d)
}

const (
	fillCrop     = 96
	fillPerFrame = 8
	// a crop's holes: at least this share of it, at most that
	fillMinHoles, fillMaxHoles = 0.02, 0.4
	// the rest of it against the disc's eye: at least this PSNR
	fillAgree = 30.0
)

// writeFill writes a measured picture's crops.
func writeFill(clip string, k int, left, right *mvc.Frame, dm *stereo.Disparity, top, bottom int) error {
	w, h := left.Width, left.Height
	d := stereo.Dense(dm, w, h)
	eye, holes := stereo.Render(left, d, -1)
	type cand struct {
		x, y  int
		holes int
	}
	var cs []cand
	for y := (top + 8) &^ 1; y+fillCrop <= bottom-8; y += fillCrop {
		for x := margin; x+fillCrop <= w-margin; x += fillCrop {
			var n int
			var se, cnt float64
			for r := y; r < y+fillCrop; r++ {
				for c := x; c < x+fillCrop; c++ {
					if holes[r*w+c] {
						n++
						continue
					}
					diff := float64(eye.Y[r*w+c]) - float64(right.Y[r*right.StrideY+c])
					se += diff * diff
					cnt++
				}
			}
			share := float64(n) / (fillCrop * fillCrop)
			if share < fillMinHoles || share > fillMaxHoles || psnr(se/max(cnt, 1)) < fillAgree {
				continue
			}
			cs = append(cs, cand{x, y, n})
		}
	}
	slices.SortFunc(cs, func(a, b cand) int { return b.holes - a.holes })
	for i, c := range cs[:min(len(cs), fillPerFrame)] {
		name := strings.ReplaceAll(fmt.Sprintf("%s-%06d-%d.bfs", clip, k, i), "/", "_")
		if err := writeCrop(filepath.Join(fillDir, name), eye, right, holes, d, c.x, c.y); err != nil {
			return err
		}
	}
	return nil
}

func writeCrop(path string, eye, right *mvc.Frame, holes []bool, d []float32, x0, y0 int) error {
	f, err := os.Create(path) //nolint:gosec // the operator's directory
	if err != nil {
		return err
	}
	b := bufio.NewWriterSize(f, 1<<18)
	_, _ = b.WriteString("BFS1")
	_ = binary.Write(b, binary.LittleEndian, uint32(fillCrop))
	w := eye.Width
	planes := func(fr *mvc.Frame) {
		for _, p := range []struct {
			pix    []byte
			stride int
			chroma bool
		}{{fr.Y, fr.StrideY, false}, {fr.Cb, fr.StrideC, true}, {fr.Cr, fr.StrideC, true}} {
			for r := y0; r < y0+fillCrop; r++ {
				for c := x0; c < x0+fillCrop; c++ {
					if p.chroma {
						_ = b.WriteByte(p.pix[(r/2)*p.stride+c/2])
					} else {
						_ = b.WriteByte(p.pix[r*p.stride+c])
					}
				}
			}
		}
	}
	planes(eye)
	for r := y0; r < y0+fillCrop; r++ {
		for c := x0; c < x0+fillCrop; c++ {
			v := byte(0)
			if holes[r*w+c] {
				v = 1
			}
			_ = b.WriteByte(v)
		}
	}
	row := make([]float32, fillCrop)
	for r := y0; r < y0+fillCrop; r++ {
		for c := range fillCrop {
			v := d[r*w+x0+c]
			if math.IsNaN(float64(v)) {
				v = 0
			}
			row[c] = v
		}
		_ = binary.Write(b, binary.LittleEndian, row)
	}
	planes(right)
	return errors.Join(b.Flush(), f.Close())
}
