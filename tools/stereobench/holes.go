package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// The hole filler's training data (-holes DIR): for each measured
// picture, the right eye rendered from the left twice, by the network's
// depth fitted to the disc (holes placed as the disc's geometry has
// them; NAME-fN) and by our own depth as shaped (the holes a conversion
// makes; NAME-oN), cut into crops around where the holes are, each with
// the disc's right eye there (the truth):
//
//	"BHL1", W, H (uint32 little-endian: the crop's size, a multiple of 16)
//	rendered Y   W*H bytes
//	rendered Cb, Cr  W/2*H/2 bytes each
//	holes        W*H bytes, 1 where the eye saw nothing (filled from the side)
//	disc Y       W*H bytes
//	disc Cb, Cr  W/2*H/2 bytes each
//
// Up to holeCrops crops a picture, holeCrop square, where the holes are
// densest (by holeBlock blocks), a crop with fewer than holeLeast of its
// pixels holes left out.

var (
	holesDir         string
	holeCropsWritten int
)

const (
	holeCrop   = 384
	holeBlock  = 64
	holeCrops  = 2
	holeLeast  = 0.003
	holeMargin = 0.03
)

// writeHoles writes a picture's crops: how many. The eye is rendered by
// the disparity given (the network's depth fitted to the disc: holes of
// the shape a conversion makes, not the matcher's pixel-to-pixel noise,
// which opens holes a pixel wide everywhere); holes a pixel wide are
// dropped, and those within holeMargin of the picture's sides (the eye
// looking past the frame) left out.
func writeHoles(clip string, k int, kind string, left, right *mvc.Frame, disp []float32, top, bottom int) (int, error) {
	w, h := left.Width, left.Height
	if w < holeCrop || bottom-top < holeCrop {
		return 0, nil
	}
	eye, holes := stereo.Render(left, disp, -1)
	margin := int(holeMargin * float64(w))
	for y := range h {
		row := holes[y*w : (y+1)*w]
		for x := 0; x < w; {
			if !row[x] {
				x++
				continue
			}
			a := x
			for x < w && row[x] {
				x++
			}
			if x-a < 2 || a < margin || x > w-margin {
				for i := a; i < x; i++ {
					row[i] = false
				}
			}
		}
	}
	// the holes by block
	bw, bh := (w+holeBlock-1)/holeBlock, (h+holeBlock-1)/holeBlock
	count := make([]int, bw*bh)
	for y := top; y < bottom; y++ {
		for x := range w {
			if holes[y*w+x] {
				count[(y/holeBlock)*bw+x/holeBlock]++
			}
		}
	}
	n := 0
	for range holeCrops {
		best, bi := 0, -1
		for i, c := range count {
			if c > best {
				best, bi = c, i
			}
		}
		if bi < 0 {
			break
		}
		// the crop centred on the block, within the active rows, even
		cx, cy := (bi%bw)*holeBlock+holeBlock/2, (bi/bw)*holeBlock+holeBlock/2
		x0 := min(max(cx-holeCrop/2, 0), w-holeCrop) &^ 1
		y0 := min(max(cy-holeCrop/2, top), bottom-holeCrop) &^ 1
		// the blocks the crop covers are spent
		var inCrop int
		for y := y0; y < y0+holeCrop; y++ {
			for x := x0; x < x0+holeCrop; x++ {
				if holes[y*w+x] {
					inCrop++
				}
			}
		}
		for by := y0 / holeBlock; by <= (y0+holeCrop-1)/holeBlock; by++ {
			for bx := x0 / holeBlock; bx <= (x0+holeCrop-1)/holeBlock; bx++ {
				count[by*bw+bx] = 0
			}
		}
		if float64(inCrop) < holeLeast*holeCrop*holeCrop {
			continue
		}
		if err := writeHoleCrop(clip, k, kind, n, eye, holes, right, x0, y0); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func writeHoleCrop(clip string, k int, kind string, i int, eye *mvc.Frame, holes []bool, right *mvc.Frame, x0, y0 int) error {
	name := strings.ReplaceAll(fmt.Sprintf("%s-%06d-%s%d.bhl", clip, k, kind, i), "/", "_")
	f, err := os.Create(filepath.Join(holesDir, name)) //nolint:gosec // the operator's directory
	if err != nil {
		return err
	}
	b := bufio.NewWriterSize(f, 1<<20)
	_, _ = b.WriteString("BHL1")
	for _, v := range []uint32{holeCrop, holeCrop} {
		_ = binary.Write(b, binary.LittleEndian, v)
	}
	planes := func(fr *mvc.Frame) {
		for y := y0; y < y0+holeCrop; y++ {
			_, _ = b.Write(fr.Y[y*fr.StrideY+x0 : y*fr.StrideY+x0+holeCrop])
		}
		for _, p := range [][]byte{fr.Cb, fr.Cr} {
			for y := y0 / 2; y < (y0+holeCrop)/2; y++ {
				_, _ = b.Write(p[y*fr.StrideC+x0/2 : y*fr.StrideC+(x0+holeCrop)/2])
			}
		}
	}
	planes(eye)
	w := eye.Width
	row := make([]byte, holeCrop)
	for y := y0; y < y0+holeCrop; y++ {
		for x := range holeCrop {
			row[x] = 0
			if holes[y*w+x0+x] {
				row[x] = 1
			}
		}
		_, _ = b.Write(row)
	}
	planes(right)
	return errors.Join(b.Flush(), f.Close())
}
