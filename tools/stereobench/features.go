package main

import (
	"encoding/csv"
	"math"
	"slices"
	"strconv"

	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// The grading's training data (-frames): for each measured picture, what
// a conversion can know of it (its depth's distribution and layout, its
// brightness) and how the disc graded it (the network's depth fitted to
// the disc's disparity: disparity = a depth + b, in pixels at 1920).

var framesOut *csv.Writer

var featureHead = []string{"clip", "picture", "a", "b", "corr", "matched",
	"disc_p5", "disc_p50", "disc_p95",
	"d_p1", "d_p5", "d_p10", "d_p25", "d_p50", "d_p75", "d_p90", "d_p95", "d_p99", "d_mean", "d_sd",
	"d_top", "d_mid", "d_bottom", "d_centre", "d_sides", "active", "luma_mean", "luma_sd"}

func writeFeatures(clip string, k int, left *mvc.Frame, raw []float32, dm *stereo.Disparity, hr []float32,
	a, b, corr, matched float64, top, bottom int) {
	w, h := left.Width, left.Height
	scale := 1920 / float64(w) // the fit in pixels at 1920
	var all []float32
	var sum, ss float64
	region := map[string][]float32{}
	ah := bottom - top
	for y := top; y < bottom; y += 3 {
		for x := 0; x < w; x += 3 {
			v := raw[y*w+x]
			if math.IsNaN(float64(v)) {
				continue
			}
			all = append(all, v)
			sum += float64(v)
			ss += float64(v) * float64(v)
			switch ry := (y - top) * 3 / max(ah, 1); ry {
			case 0:
				region["top"] = append(region["top"], v)
			case 1:
				region["mid"] = append(region["mid"], v)
			default:
				region["bottom"] = append(region["bottom"], v)
			}
			if x > w/4 && x < 3*w/4 {
				region["centre"] = append(region["centre"], v)
			} else {
				region["sides"] = append(region["sides"], v)
			}
		}
	}
	if len(all) == 0 {
		return
	}
	slices.Sort(all)
	q := func(p float64) float64 { return float64(all[int(p*float64(len(all)-1))]) }
	med := func(v []float32) float64 {
		if len(v) == 0 {
			return q(0.5)
		}
		slices.Sort(v)
		return float64(v[len(v)/2])
	}
	n := float64(len(all))
	mean := sum / n
	var disc []float32
	for i, ok := range dm.Valid {
		if y := i / dm.W; ok && y >= top/2 && y < bottom/2 && !math.IsNaN(float64(hr[i])) {
			disc = append(disc, dm.D[i])
		}
	}
	dp := percentiles(disc)
	var ls, lss float64
	var ln float64
	for y := top; y < bottom; y += 3 {
		for x := 0; x < w; x += 3 {
			v := float64(left.Y[y*left.StrideY+x])
			ls += v
			lss += v * v
			ln++
		}
	}
	lm := ls / max(ln, 1)
	ff := func(v float64) string { return strconv.FormatFloat(v, 'g', 6, 64) }
	row := []string{clip, strconv.Itoa(k), ff(a * scale), ff(b * scale), ff(corr), ff(matched),
		ff(dp[0] * scale), ff(dp[1] * scale), ff(dp[2] * scale),
		ff(q(0.01)), ff(q(0.05)), ff(q(0.10)), ff(q(0.25)), ff(q(0.5)), ff(q(0.75)), ff(q(0.90)), ff(q(0.95)), ff(q(0.99)),
		ff(mean), ff(math.Sqrt(max(ss/n-mean*mean, 0))),
		ff(med(region["top"])), ff(med(region["mid"])), ff(med(region["bottom"])), ff(med(region["centre"])), ff(med(region["sides"])),
		ff(float64(ah) / float64(h)), ff(lm), ff(math.Sqrt(max(lss/max(ln, 1)-lm*lm, 0)))}
	_ = framesOut.Write(row)
}
