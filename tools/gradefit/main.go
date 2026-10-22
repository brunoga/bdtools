// Command gradefit learns how 3D discs grade depth: from stereobench
// -frames CSVs (a picture's features, and the disc's disparity fitted by
// the network's depth: disparity = a depth + b), a ridge regression of a
// and b on the features.
//
//	gradefit [-lambda L] FILE.csv...
//
// Each file is a film. It reports the error left (the disparity, at the
// picture's depth percentiles, against the disc's fit) by the fixed
// mapping and by the regression with each film left out of its own fit,
// then the regression on them all, as Go.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The features used: the depth's distribution and layout, the picture's
// brightness.
var features = []string{"d_p1", "d_p5", "d_p10", "d_p25", "d_p50", "d_p75", "d_p90", "d_p95", "d_p99", "d_mean", "d_sd",
	"d_top", "d_mid", "d_bottom", "d_centre", "d_sides", "active", "luma_mean", "luma_sd"}

// The fixed mapping, at 1920: disparity = 2.304 depth - 14.4.
const fixedA, fixedB = 0.0012 * 1920, -0.0075 * 1920

type sample struct {
	x    []float64
	a, b float64
	w    float64
	at   [3]float64 // the depth at its 10th, 50th and 90th percentiles
}

func main() {
	lambda := flag.Float64("lambda", 10, "the ridge's weight")
	centred := flag.Bool("centred", false, "score each film with its mean offset (at the depth's median) taken out: how well the grading follows the film, not where the film sits")
	test := flag.String("test", "", "films held out (comma-separated CSVs): trained on all the others, scored on these")
	flag.Parse()
	films := map[string][]sample{}
	var names []string
	for _, f := range flag.Args() {
		s, err := read(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gradefit:", err)
			os.Exit(1)
		}
		name := strings.TrimSuffix(filepath.Base(f), ".csv")
		films[name] = s
		names = append(names, name)
	}
	var all []sample
	for _, n := range names {
		all = append(all, films[n]...)
	}
	mu, sd := standardise(all)
	var fixedE, fitE, wsum float64
	for _, n := range names {
		var train []sample
		for _, m := range names {
			if m != n {
				train = append(train, films[m]...)
			}
		}
		wa, wb := ridge(train, mu, sd, *lambda)
		fe, ws := filmErr(films[n], fixed, *centred)
		re, _ := filmErr(films[n], learnt(wa, wb, mu, sd), *centred)
		fmt.Printf("%-45s %4d pictures: fixed %.2f px, learnt %.2f px\n", n, len(films[n]), fe/ws, re/ws)
		fixedE += fe
		fitE += re
		wsum += ws
	}
	fmt.Printf("all, each film left out: fixed %.2f px, learnt %.2f px\n", fixedE/wsum, fitE/wsum)
	wa, wb := ridge(all, mu, sd, *lambda)
	if *test != "" {
		var fe, re, ws float64
		for _, f := range strings.Split(*test, ",") {
			ss, err := read(f)
			if err != nil {
				fmt.Fprintln(os.Stderr, "gradefit:", err)
				os.Exit(1)
			}
			e1, w := filmErr(ss, fixed, *centred)
			e2, _ := filmErr(ss, learnt(wa, wb, mu, sd), *centred)
			fmt.Printf("held out %-30s %4d pictures: fixed %.2f px, learnt %.2f px\n", filepath.Base(f), len(ss), e1/w, e2/w)
			fe, re, ws = fe+e1, re+e2, ws+w
		}
		fmt.Printf("held out, all: fixed %.2f px, learnt %.2f px\n", fe/ws, re/ws)
	}
	fmt.Println("\n// the regression on all films")
	fmt.Printf("var gradeFeatures = %#v\n", features)
	fmt.Printf("var gradeMean = %#v\nvar gradeSD = %#v\n", mu, sd)
	fmt.Printf("var gradeA = %#v\nvar gradeB = %#v\n", wa, wb)
}

// filmErr is the error of a film's pictures, weighted, given each
// picture's grading (and their weights' sum); centred, the film's mean
// error at the depth's median is taken out first.
func filmErr(ss []sample, grade func(sample) (float64, float64), centred bool) (e, w float64) {
	var shift float64
	if centred {
		for _, s := range ss {
			a, b := grade(s)
			shift += s.w * ((a-s.a)*s.at[1] + (b - s.b))
			w += s.w
		}
		shift /= w
		w = 0
	}
	for _, s := range ss {
		a, b := grade(s)
		e += s.w * errAt(s, a, b-shift)
		w += s.w
	}
	return e, w
}

// errAt is the disparity error of a mapping against the disc's fit, at the
// picture's depth percentiles.
func errAt(s sample, a, b float64) float64 {
	var e float64
	for _, d := range s.at {
		e += math.Abs((a-s.a)*d + (b - s.b))
	}
	return e / 3
}

func fixed(sample) (float64, float64) { return fixedA, fixedB }

func learnt(wa, wb, mu, sd []float64) func(sample) (float64, float64) {
	return func(s sample) (float64, float64) { return predict(wa, s, mu, sd), predict(wb, s, mu, sd) }
}

func predict(w []float64, s sample, mu, sd []float64) float64 {
	v := w[0]
	for i, x := range s.x {
		v += w[i+1] * (x - mu[i]) / sd[i]
	}
	return v
}

func standardise(all []sample) (mu, sd []float64) {
	n := len(features)
	mu, sd = make([]float64, n), make([]float64, n)
	for _, s := range all {
		for i, x := range s.x {
			mu[i] += x / float64(len(all))
		}
	}
	for _, s := range all {
		for i, x := range s.x {
			sd[i] += (x - mu[i]) * (x - mu[i]) / float64(len(all))
		}
	}
	for i := range sd {
		sd[i] = math.Max(math.Sqrt(sd[i]), 1e-9)
	}
	return mu, sd
}

// ridge fits a and b by weighted ridge regression (the intercept not
// penalised): the weights for [1, standardised features...].
func ridge(train []sample, mu, sd []float64, lambda float64) (wa, wb []float64) {
	p := len(features) + 1
	xtx := make([][]float64, p)
	for i := range xtx {
		xtx[i] = make([]float64, p)
	}
	xa, xb := make([]float64, p), make([]float64, p)
	row := make([]float64, p)
	for _, s := range train {
		row[0] = 1
		for i, x := range s.x {
			row[i+1] = (x - mu[i]) / sd[i]
		}
		for i := range p {
			for j := range p {
				xtx[i][j] += s.w * row[i] * row[j]
			}
			xa[i] += s.w * row[i] * s.a
			xb[i] += s.w * row[i] * s.b
		}
	}
	for i := 1; i < p; i++ {
		xtx[i][i] += lambda
	}
	return solve(xtx, xa), solve(xtx, xb)
}

// solve solves m x = v by Gaussian elimination (m copied).
func solve(m [][]float64, v []float64) []float64 {
	n := len(v)
	a := make([][]float64, n)
	for i := range a {
		a[i] = append(append([]float64(nil), m[i]...), v[i])
	}
	for c := range n {
		p := c
		for r := c + 1; r < n; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		a[c], a[p] = a[p], a[c]
		for r := range n {
			if r == c || a[c][c] == 0 {
				continue
			}
			f := a[r][c] / a[c][c]
			for k := c; k <= n; k++ {
				a[r][k] -= f * a[c][k]
			}
		}
	}
	x := make([]float64, n)
	for i := range n {
		if a[i][i] != 0 {
			x[i] = a[i][n] / a[i][i]
		}
	}
	return x
}

func read(path string) ([]sample, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's files
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil || len(rows) < 2 {
		return nil, fmt.Errorf("%s: %v (%d rows)", path, err, len(rows))
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	num := func(r []string, name string) float64 {
		v, _ := strconv.ParseFloat(r[col[name]], 64)
		return v
	}
	var out []sample
	for _, r := range rows[1:] {
		s := sample{a: num(r, "a"), b: num(r, "b")}
		corr, matched := num(r, "corr"), num(r, "matched")
		if corr < 0.3 || matched < 0.1 {
			continue // the disc's fit not to be trusted
		}
		s.w = matched * corr * corr
		for _, name := range features {
			s.x = append(s.x, num(r, name))
		}
		s.at = [3]float64{num(r, "d_p10"), num(r, "d_p50"), num(r, "d_p90")}
		out = append(out, s)
	}
	return out, nil
}
