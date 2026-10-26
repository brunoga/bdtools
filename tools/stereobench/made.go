package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/brunoga/bdtools/internal/convert"
	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// -made: other converters' pairs scored as ours is. Each is a directory
// of full side-by-side files named as the clips, made from the clips' left
// eyes (the left eye kept, the right made: iw3's --synthetic-view right),
// picture for picture. Beside the right eye's PSNR and VMAF, every pair
// (ours, made by the same renderer --to-3d's is, and the references too)
// is measured as a pair, which no depth budget or screen place favours:
//
//   - shape: the disparity measured between its eyes against the disc's,
//     fitted (a scale and an offset a picture): the correlation and the
//     error left, in the disc's pixels;
//   - detail: what an eye sees behind a nearer thing's edge, which the
//     other does not, against the rest of the picture (the share of its
//     detail across rows), the worse eye's: about 1 in a real pair, low
//     where it is stretched in;
//     (it depends on how much is revealed: compare pairs of like
//     depth, by the share revealed beside it);
//   - jitter: from one picture to the next, the change of its fitted
//     disparity against the disc's (pixels), where all four are sure.

// madeTool is a -made converter: its name and its pairs' directory.
type madeTool struct{ name, dir string }

var made []madeTool

// matchGPU matches the pairs on the GPU when there is one (the CPU's
// disparities exactly).
var matchGPU *nn.GPU

func parseMade(s string) ([]madeTool, error) {
	var out []madeTool
	for _, f := range strings.Split(s, ",") {
		name, dir, ok := strings.Cut(f, "=")
		if !ok || name == "" || dir == "" {
			return nil, fmt.Errorf("-made %q: NAME=DIR", f)
		}
		out = append(out, madeTool{name, dir})
	}
	return out, nil
}

func matchBoth(l, r *mvc.Frame) (dl, dr *stereo.Disparity) {
	if matchGPU != nil {
		if dl, dr, err := stereo.MatchBothGPU(matchGPU, l, r); err == nil {
			return dl, dr
		}
	}
	return stereo.MatchBoth(l, r)
}

// loadMade is a tool's pairs for a clip, those keep wants (by picture
// number), and how many it has.
func loadMade(t madeTool, clip string, keep func(k int) bool) (map[int][2]*mvc.Frame, int, error) {
	path := filepath.Join(t.dir, clip+".mkv")
	o := convert.DefaultOptions()
	o.TempDir = os.TempDir()
	out := map[int][2]*mvc.Frame{}
	k := 0
	err := convert.DecodeFrames(context.Background(), runtime.GOOS, o, path, nil, func(sbs *mvc.Frame) error {
		if keep(k) {
			l, r := split(sbs)
			out[k] = [2]*mvc.Frame{l, r}
		}
		k++
		return nil
	})
	return out, k, err
}

// pairMeasure is a pair's measures as a pair (see -made), summed over the
// pictures.
type pairMeasure struct {
	corr, epe, detail, jitter, revealed float64
	n, nDetail, nJitter                 float64
}

// shapeFit fits a pair's measured disparity (at the half resolution the
// matching has) to the disc's: the scale, the offset, their correlation
// and the error left.
func shapeFit(disc, pair *stereo.Disparity, top, bottom int) (a, b, corr, epe float64, ok bool) {
	hr := make([]float32, len(pair.D))
	for i, v := range pair.Valid {
		hr[i] = float32(math.NaN())
		if v {
			hr[i] = pair.D[i]
		}
	}
	a, b, corr, matched := fit(disc, hr, top, bottom)
	if matched < 0.05 {
		return 0, 0, 0, 0, false
	}
	var e, n float64
	for i, v := range disc.Valid {
		if y := i / disc.W; !v || y < top || y >= bottom || math.IsNaN(float64(hr[i])) {
			continue
		}
		e += math.Abs(a*float64(hr[i]) + b - float64(disc.D[i]))
		n++
	}
	if n == 0 {
		return 0, 0, 0, 0, false
	}
	return a, b, corr, e / n, true
}

// detail is, for the worse of a pair's two eyes, the share of across-row
// detail where it sees what the other does not (behind a nearer thing's
// edge), against where both see the same; false when there is too little
// of either to say. The worse: a converter keeps one eye as it was, which
// has the picture's own detail there.
func detail(l, r *mvc.Frame, ml, mr *stereo.Disparity) (ratio, revealed float64, ok bool) {
	eye := func(f *mvc.Frame, me, other *stereo.Disparity, sign float32) (occ, rest float64, no, nr int) {
		w := me.W
		for y := 2; y < me.H-2; y++ {
			for x := 2; x < w-2; x++ {
				i := y*w + x
				d := me.D[i] / 2 // half resolution pixels
				xo := int(math.Round(float64(float32(x) - sign*d)))
				if xo < 0 || xo >= w {
					continue
				}
				back := other.D[y*w+xo] / 2
				X, Y := 2*x, 2*y
				gx := float64(f.Y[Y*f.StrideY+X+1]) - float64(f.Y[Y*f.StrideY+X-1])
				gy := float64(f.Y[(Y+1)*f.StrideY+X]) - float64(f.Y[(Y-1)*f.StrideY+X])
				e := gx*gx + gy*gy
				if e < 64 {
					continue // flat: says nothing
				}
				switch {
				case back > d+3:
					// the other eye sees something nearer there: behind an edge
					occ += gx * gx / e
					no++
				case me.Valid[i]:
					rest += gx * gx / e
					nr++
				}
			}
		}
		return occ, rest, no, nr
	}
	worst := math.Inf(1)
	var eyes, occ, all int
	for _, v := range []struct {
		f         *mvc.Frame
		me, other *stereo.Disparity
		sign      float32
	}{{l, ml, mr, 1}, {r, mr, ml, -1}} {
		o, rest, no, nr := eye(v.f, v.me, v.other, v.sign)
		occ += no
		all += no + nr
		if no < 200 || nr < 200 {
			continue
		}
		worst = min(worst, (o/float64(no))/(rest/float64(nr)))
		eyes++
	}
	if all > 0 {
		revealed = float64(occ) / float64(all)
	}
	if eyes == 0 {
		return 0, revealed, false
	}
	return worst, revealed, true
}

// jitter is the mean change of a pair's disparity from one picture to
// the next, scaled by a to the disc's, against the disc's change, where
// all four are sure.
func jitter(disc0, disc1, pair0, pair1 *stereo.Disparity, a float64, top, bottom int) (float64, bool) {
	var e, n float64
	for i := range disc0.D {
		if y := i / disc0.W; y < top || y >= bottom || !disc0.Valid[i] || !disc1.Valid[i] || !pair0.Valid[i] || !pair1.Valid[i] {
			continue
		}
		e += math.Abs(a*float64(pair1.D[i]-pair0.D[i]) - float64(disc1.D[i]-disc0.D[i]))
		n++
	}
	if n < 1000 {
		return 0, false
	}
	return e / n, true
}

// measurePair adds a pair's measures (at picture k, and its next when it
// is there) to m.
func measurePair(m *pairMeasure, disc0, disc1 *stereo.Disparity, l0, r0, l1, r1 *mvc.Frame, top, bottom int) {
	ml, mr := matchBoth(l0, r0)
	a, _, corr, epe, ok := shapeFit(disc0, ml, top/2, bottom/2)
	if !ok {
		return
	}
	m.corr += corr
	m.epe += epe
	m.n++
	d, rev, ok := detail(l0, r0, ml, mr)
	m.revealed += rev
	if ok {
		m.detail += d
		m.nDetail++
	}
	if disc1 == nil || l1 == nil {
		return
	}
	nl, _ := matchBoth(l1, r1)
	if j, ok := jitter(disc0, disc1, ml, nl, a, top/2, bottom/2); ok {
		m.jitter += j
		m.nJitter++
	}
}

func (m pairMeasure) means() (corr, epe, detail, jitter, revealed float64) {
	nan := math.NaN()
	corr, epe, detail, jitter, revealed = nan, nan, nan, nan, nan
	if m.n > 0 {
		corr, epe, revealed = m.corr/m.n, m.epe/m.n, m.revealed/m.n
	}
	if m.nDetail > 0 {
		detail = m.detail / m.nDetail
	}
	if m.nJitter > 0 {
		jitter = m.jitter / m.nJitter
	}
	return corr, epe, detail, jitter, revealed
}
