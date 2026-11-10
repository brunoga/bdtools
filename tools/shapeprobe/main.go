// shapeprobe: how much of the disc's disparity mapping (scale and offset
// of our depth, stereo.Shape) is a film's, a shot's, and predictable from
// the picture. Reads the training samples (NAME-000123.bds), fits the
// mapping a stretch at a time and a film at a time, and tries a ridge
// regression from cheap picture features, leaving one film out at a time.
//
//	shapeprobe DIR
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brunoga/bdtools/internal/depth"
	"github.com/brunoga/bdtools/internal/nn"
)

type pic struct {
	stretch, film string
	w, h          int
	// sums for the LS fit of disparity = a depth + b over the sure pixels
	n, sx, sy, sxx, sxy float64
	// the sure pixels' depth and disparity, subsampled, for the residuals
	x, y []float32
	// the same pixels' metric feature f/Z (Depth Pro's, NAME.bmd: the
	// focal length in pixels of the half-size picture over the depth in
	// metres), if any; the fit's scale is then the interaxial in metres
	xm                       []float32
	nm, sxm, sym, sxxm, sxym float64
	feat                     []float64
	tok                      []float64 // the encoder's pooled tokens (NAME.bfe), if any
	grid                     []float64 // the last two layers' tokens on an 8x8 grid (NAME.bfg), if any
}

const gridN = 8

const step = 3 // every third pixel each way

func main() {
	ckpt := flag.String("ckpt", os.ExpandEnv("$HOME/.cache/bdtools/video_depth_anything_vits.pth"), "the depth network's checkpoint")
	extract := flag.Bool("features", false, "write each sample's pooled encoder tokens (NAME.bfe, from its window's last frame) and stop")
	quick := flag.Bool("quick", false, "the fits and the metric comparison only, not the regressions")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "shapeprobe [-features] DIR")
		os.Exit(2)
	}
	paths, _ := filepath.Glob(filepath.Join(flag.Arg(0), "*.bds"))
	sort.Strings(paths)
	if *extract {
		extractFeatures(*ckpt, paths)
		return
	}
	var pics []*pic
	for _, p := range paths {
		pc, err := read(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		if pc.n < 100 {
			continue
		}
		if t, err := readFeatures(strings.TrimSuffix(p, ".bds") + ".bfe"); err == nil {
			pc.tok = t
		}
		if t, err := readFeatures(strings.TrimSuffix(p, ".bds") + ".bfg"); err == nil {
			pc.grid = t
		}
		pics = append(pics, pc)
	}
	fmt.Printf("%d pictures\n", len(pics))

	// the fits: a picture, a stretch, a film at a time; all at once
	byStretch, byFilm := group(pics, func(p *pic) string { return p.stretch }), group(pics, func(p *pic) string { return p.film })
	fmt.Printf("%d stretches, %d films\n", len(byStretch), len(byFilm))
	strFit, filmFit := fits(byStretch), fits(byFilm)
	allA, allB := fit(pics)
	fmt.Printf("all pictures at once: scale %.5f offset %.5f (of the width; shipped 0.00115 / -0.0082)\n", allA, allB)

	fixed := func(p *pic) (float64, float64) { return 0.00115, -0.0082 }
	report := func(name string, f func(p *pic) (float64, float64)) {
		var robust, abs, n float64
		for _, p := range pics {
			a, b := f(p)
			r, m := resid(p, a*float64(p.w), b*float64(p.w))
			robust += r
			abs += m
			n++
		}
		fmt.Printf("%-42s robust %.3f  |r| %.3f px\n", name, robust/n, abs/n)
	}
	fmt.Println("\nmean residual of the mapping, half-size pixels (lower is better):")
	report("shipped fixed shape", fixed)
	report("one shape for all (fitted here)", func(p *pic) (float64, float64) { return allA, allB })
	report("one shape a film (oracle)", func(p *pic) (float64, float64) { f := filmFit[p.film]; return f[0], f[1] })
	report("one shape a stretch (oracle)", func(p *pic) (float64, float64) { f := strFit[p.stretch]; return f[0], f[1] })
	report("one shape a picture (oracle)", func(p *pic) (float64, float64) { a, b := fit([]*pic{p}); return a, b })
	// the stretch's offset or scale alone, the other the global one
	report("a stretch's offset, the global scale (oracle)", func(p *pic) (float64, float64) {
		return allA, offsetFor(byStretch[p.stretch], allA)
	})
	report("a stretch's scale, the global offset (oracle)", func(p *pic) (float64, float64) {
		return scaleFor(byStretch[p.stretch], allB), allB
	})

	// rules: the global scale, the offset anchoring one depth statistic of
	// the stretch (its mean over pictures) to one global disparity, fitted
	for _, an := range []struct {
		name string
		i    int
	}{{"the farthest (p5)", 0}, {"the median", 2}, {"the nearest (p95)", 4}, {"the centre's median", 12}, {"the sharpest (focus)", 13}} {
		// c = mean over stretches of (offset_s + scale * anchor_s)
		var c, n float64
		for s, ps := range byStretch {
			c += strFit[s][1] + allA*stretchFeat(ps)[an.i]
			n++
		}
		c /= n
		report("global scale, offset anchoring "+an.name, func(p *pic) (float64, float64) {
			return allA, c - allA*stretchFeat(byStretch[p.stretch])[an.i]
		})
	}

	// the metric depth's mapping: disparity = B f/Z + c, a stretch's and
	// a film's B (the interaxial, metres) and c
	var withM []*pic
	for _, p := range pics {
		if p.xm != nil {
			withM = append(withM, p)
		}
	}
	if len(withM) > 0 {
		fmt.Printf("\n%d pictures with metric depth (Depth Pro): disparity = B f/Z + c\n", len(withM))
		mStretch, mFilm := group(withM, func(p *pic) string { return p.stretch }), group(withM, func(p *pic) string { return p.film })
		msf, mff := mfits(mStretch), mfits(mFilm)
		mA, mB := mfit(withM)
		fmt.Printf("all pictures at once: B %.4f m, c %.2f px\n", mA, mB)
		mreport := func(name string, f func(p *pic) (float64, float64)) {
			var robust, abs, n float64
			for _, p := range withM {
				a, b := f(p)
				r, m := mresid(p, a, b)
				robust += r
				abs += m
				n++
			}
			fmt.Printf("%-42s robust %.3f  |r| %.3f px\n", name, robust/n, abs/n)
		}
		oreport := func(name string, f func(p *pic) (float64, float64)) {
			var robust, abs, n float64
			for _, p := range withM {
				a, b := f(p)
				r, m := resid(p, a*float64(p.w), b*float64(p.w))
				robust += r
				abs += m
				n++
			}
			fmt.Printf("%-42s robust %.3f  |r| %.3f px\n", name, robust/n, abs/n)
		}
		oStretch, oFilm := fits(group(withM, func(p *pic) string { return p.stretch })), fits(group(withM, func(p *pic) string { return p.film }))
		oA, oB := fit(withM)
		fmt.Println("the same pictures, our depth:")
		oreport("  ours: one shape for all", func(p *pic) (float64, float64) { return oA, oB })
		oreport("  ours: one a film (oracle)", func(p *pic) (float64, float64) { f := oFilm[p.film]; return f[0], f[1] })
		oreport("  ours: one a stretch (oracle)", func(p *pic) (float64, float64) { f := oStretch[p.stretch]; return f[0], f[1] })
		fmt.Println("metric depth:")
		mreport("  metric: one mapping for all", func(p *pic) (float64, float64) { return mA, mB })
		mreport("  metric: one a film (oracle)", func(p *pic) (float64, float64) { f := mff[p.film]; return f[0], f[1] })
		mreport("  metric: one a stretch (oracle)", func(p *pic) (float64, float64) { f := msf[p.stretch]; return f[0], f[1] })
		var ba, bc []float64
		for _, f := range msf {
			ba, bc = append(ba, f[0]), append(bc, f[1])
		}
		fmt.Printf("stretches' interaxial B: mean %.4f m sd %.4f; convergence c: mean %.2f px sd %.2f\n", mean(ba), sd(ba), mean(bc), sd(bc))
		var fa, fc []float64
		for _, f := range mff {
			fa, fc = append(fa, f[0]), append(fc, f[1])
		}
		fmt.Printf("films' interaxial B: mean %.4f m sd %.4f; convergence c: mean %.2f px sd %.2f\n", mean(fa), sd(fa), mean(fc), sd(fc))
	}

	if *quick {
		return
	}

	// the spread: scale and offset across films and across stretches within a film
	var fa, fb []float64
	for _, f := range filmFit {
		fa, fb = append(fa, f[0]), append(fb, f[1])
	}
	fmt.Printf("\nfilms' shapes: scale %.5f sd %.5f, offset %.5f sd %.5f\n", mean(fa), sd(fa), mean(fb), sd(fb))
	var wa, wb []float64
	for film, ps := range byFilm {
		seen := map[string]bool{}
		var sa, sb []float64
		for _, p := range ps {
			if seen[p.stretch] {
				continue
			}
			seen[p.stretch] = true
			f := strFit[p.stretch]
			sa, sb = append(sa, f[0]-filmFit[film][0]), append(sb, f[1]-filmFit[film][1])
		}
		wa, wb = append(wa, sd(sa)), append(wb, sd(sb))
	}
	fmt.Printf("stretches within a film: scale sd %.5f, offset sd %.5f (mean over films)\n", mean(wa), mean(wb))

	// ridge regression from the stretch's mean features to its shape,
	// leaving one film out at a time
	films := keys(byFilm)
	for _, lambda := range []float64{0.1, 1, 10, 100} {
		pred := map[string][2]float64{}
		for _, hold := range films {
			var X [][]float64
			var ya, yb []float64
			for s, ps := range byStretch {
				if ps[0].film == hold {
					continue
				}
				X = append(X, stretchFeat(ps))
				ya, yb = append(ya, strFit[s][0]), append(yb, strFit[s][1])
			}
			ma, mb := ridge(X, ya, lambda), ridge(X, yb, lambda)
			for s, ps := range byStretch {
				if ps[0].film != hold {
					continue
				}
				f := stretchFeat(ps)
				pred[s] = [2]float64{ma.apply(f), mb.apply(f)}
			}
		}
		report(fmt.Sprintf("ridge from features, film held out, l=%g", lambda), func(p *pic) (float64, float64) { f := pred[p.stretch]; return f[0], f[1] })
	}
	// the film's mean of the predictions (as if the whole film were seen first)
	{
		pred := map[string][2]float64{}
		for _, hold := range films {
			var X [][]float64
			var ya, yb []float64
			for s, ps := range byStretch {
				if ps[0].film == hold {
					continue
				}
				X = append(X, stretchFeat(ps))
				ya, yb = append(ya, strFit[s][0]), append(yb, strFit[s][1])
			}
			ma, mb := ridge(X, ya, 10), ridge(X, yb, 10)
			var sa, sb, n float64
			for _, ps := range byStretch {
				if ps[0].film != hold {
					continue
				}
				f := stretchFeat(ps)
				sa += ma.apply(f)
				sb += mb.apply(f)
				n++
			}
			pred[hold] = [2]float64{sa / n, sb / n}
		}
		report("ridge l=10, averaged over the film", func(p *pic) (float64, float64) { f := pred[p.film]; return f[0], f[1] })
	}

	// the same from the encoder's pooled tokens (with the features), in
	// the dual (more dimensions than stretches)
	var withTok []*pic
	for _, p := range pics {
		if p.tok != nil {
			withTok = append(withTok, p)
		}
	}
	if len(withTok) == 0 {
		return
	}
	fmt.Printf("\n%d pictures with tokens (%d values)\n", len(withTok), len(withTok[0].tok))
	byStretch = group(withTok, func(p *pic) string { return p.stretch })
	names := keys(byStretch)
	Z := make([][]float64, len(names))
	ya, yb := make([]float64, len(names)), make([]float64, len(names))
	film := make([]string, len(names))
	for i, s := range names {
		ps := byStretch[s]
		Z[i] = append(stretchFeat(ps), stretchTok(ps)...)
		ya[i], yb[i] = strFit[s][0], strFit[s][1]
		film[i] = ps[0].film
	}
	standardise(Z)
	for _, lambda := range []float64{10000} {
		pred := map[string][2]float64{}
		for _, hold := range films {
			var train, test []int
			for i := range names {
				if film[i] == hold {
					test = append(test, i)
				} else {
					train = append(train, i)
				}
			}
			if len(test) == 0 {
				continue
			}
			wa, ba := ridgeDual(Z, ya, lambda, train)
			wb, bb := ridgeDual(Z, yb, lambda, train)
			for _, i := range test {
				pred[names[i]] = [2]float64{dot(wa, Z[i]) + ba, dot(wb, Z[i]) + bb}
			}
		}
		report(fmt.Sprintf("ridge from tokens+features, film held out, l=%g", lambda), func(p *pic) (float64, float64) {
			f, ok := pred[p.stretch]
			if !ok {
				return allA, allB
			}
			return f[0], f[1]
		})
	}
	perPicture(withTok, films, report)

	// the spatial grid, in the dual
	var withGrid []*pic
	for _, p := range pics {
		if p.grid != nil {
			withGrid = append(withGrid, p)
		}
	}
	if len(withGrid) == 0 {
		return
	}
	fmt.Printf("\n%d pictures with the grid (%d values)\n", len(withGrid), len(withGrid[0].grid))
	byStretch = group(withGrid, func(p *pic) string { return p.stretch })
	names = keys(byStretch)
	Z = make([][]float64, len(names))
	ya, yb = make([]float64, len(names)), make([]float64, len(names))
	film = make([]string, len(names))
	for i, s := range names {
		ps := byStretch[s]
		g := make([]float64, len(ps[0].grid))
		for _, p := range ps {
			for j, v := range p.grid {
				g[j] += v / float64(len(ps))
			}
		}
		Z[i] = append(append(stretchFeat(ps), stretchTok(ps)...), g...)
		ya[i], yb[i] = strFit[s][0], strFit[s][1]
		film[i] = ps[0].film
	}
	standardise(Z)
	for _, lambda := range []float64{30000, 100000, 300000, 1000000} {
		pred := map[string][2]float64{}
		for _, hold := range films {
			var train, test []int
			for i := range names {
				if film[i] == hold {
					test = append(test, i)
				} else {
					train = append(train, i)
				}
			}
			if len(test) == 0 {
				continue
			}
			wa, ba := ridgeDual(Z, ya, lambda, train)
			wb, bb := ridgeDual(Z, yb, lambda, train)
			for _, i := range test {
				pred[names[i]] = [2]float64{dot(wa, Z[i]) + ba, dot(wb, Z[i]) + bb}
			}
		}
		report(fmt.Sprintf("ridge from the grid too, film held out, l=%g", lambda), func(p *pic) (float64, float64) {
			f, ok := pred[p.stretch]
			if !ok {
				return allA, allB
			}
			return f[0], f[1]
		})
	}
}

// perPicture is the ridge regression a picture at a time (six times the
// samples, noisier targets: each picture's own fit), in the primal.
func perPicture(pics []*pic, films []string, report func(string, func(*pic) (float64, float64))) {
	X := make([][]float64, len(pics))
	ya, yb := make([]float64, len(pics)), make([]float64, len(pics))
	for i, p := range pics {
		X[i] = append(append([]float64{}, p.feat...), p.tok...)
		ya[i], yb[i] = fit([]*pic{p})
	}
	standardise(X)
	for _, lambda := range []float64{3000, 30000} {
		pred := map[*pic][2]float64{}
		for _, hold := range films {
			var Xt [][]float64
			var at, bt []float64
			for i, p := range pics {
				if p.film != hold {
					Xt = append(Xt, X[i])
					at, bt = append(at, ya[i]), append(bt, yb[i])
				}
			}
			ma, mb := ridge(Xt, at, lambda), ridge(Xt, bt, lambda)
			for i, p := range pics {
				if p.film == hold {
					pred[p] = [2]float64{ma.apply(X[i]), mb.apply(X[i])}
				}
			}
		}
		report(fmt.Sprintf("ridge a picture at a time, film held out, l=%g", lambda), func(p *pic) (float64, float64) {
			f := pred[p]
			return f[0], f[1]
		})
	}
}

func stretchTok(ps []*pic) []float64 {
	f := make([]float64, len(ps[0].tok))
	for _, p := range ps {
		for i, v := range p.tok {
			f[i] += v / float64(len(ps))
		}
	}
	return f
}

func standardise(Z [][]float64) {
	n, d := len(Z), len(Z[0])
	for j := range d {
		var mu, sg float64
		for i := range n {
			mu += Z[i][j] / float64(n)
		}
		for i := range n {
			sg += (Z[i][j] - mu) * (Z[i][j] - mu) / float64(n)
		}
		sg = math.Max(math.Sqrt(sg), 1e-9)
		for i := range n {
			Z[i][j] = (Z[i][j] - mu) / sg
		}
	}
}

func dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// ridgeDual is the ridge regression of y on Z's rows train, solved in
// the dual: (K + lambda I) alpha = y - mean, w = Z' alpha.
func ridgeDual(Z [][]float64, y []float64, lambda float64, train []int) (w []float64, b float64) {
	n, d := len(train), len(Z[0])
	for _, i := range train {
		b += y[i] / float64(n)
	}
	K := make([][]float64, n)
	for i := range n {
		K[i] = make([]float64, n+1)
		for j := range n {
			K[i][j] = dot(Z[train[i]], Z[train[j]])
		}
		K[i][i] += lambda
		K[i][n] = y[train[i]] - b
	}
	for c := range n {
		piv := c
		for r := c + 1; r < n; r++ {
			if math.Abs(K[r][c]) > math.Abs(K[piv][c]) {
				piv = r
			}
		}
		K[c], K[piv] = K[piv], K[c]
		for r := range n {
			if r == c {
				continue
			}
			f := K[r][c] / K[c][c]
			for k := c; k <= n; k++ {
				K[r][k] -= f * K[c][k]
			}
		}
	}
	w = make([]float64, d)
	for i := range n {
		alpha := K[i][n] / K[i][i]
		for j := range d {
			w[j] += alpha * Z[train[i]][j]
		}
	}
	return w, b
}

// extractFeatures writes each sample's pooled encoder tokens: the mean
// over the patch tokens of each of the four intermediate layers, from
// the window's last frame (the picture as the network took it).
func extractFeatures(ckpt string, paths []string) {
	g, err := nn.NewGPU()
	if err != nil {
		fail(err)
	}
	defer g.Close()
	m, err := depth.Load(ckpt)
	if err != nil {
		fail(err)
	}
	m.Use(g)
	mean := [3]float32{0.485, 0.456, 0.406}
	std := [3]float32{0.229, 0.224, 0.225}
	done := 0
	for _, p := range paths {
		out := strings.TrimSuffix(p, ".bds") + ".bfe"
		if _, err := os.Stat(strings.TrimSuffix(p, ".bds") + ".bfg"); err == nil {
			continue
		}
		fr, h, w, err := lastFrame(strings.TrimSuffix(p, ".bds") + ".bhw")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		x := nn.New(3, h, w)
		n := h * w
		for c := range 3 {
			for j, b := range fr[c*n : (c+1)*n] {
				x.Data[c*n+j] = (float32(b)/255 - mean[c]) / std[c]
			}
		}
		feats := m.Encode(g.Upload(x))
		var pooled, grid []float32
		ph, pw := h/14, w/14
		for k, f := range feats {
			d, err := g.Download(f)
			if err != nil {
				fail(err)
			}
			rows, cols := d.Dim(0), d.Dim(1)
			v := make([]float32, cols)
			for r := range rows {
				for c := range cols {
					v[c] += d.Data[r*cols+c] / float32(rows)
				}
			}
			pooled = append(pooled, v...)
			if k < 2 {
				continue
			}
			// the tokens pooled on a gridN x gridN grid of cells
			cell := make([]float32, gridN*gridN*cols)
			cnt := make([]float32, gridN*gridN)
			for t := range rows {
				ci := (t/pw)*gridN/ph*gridN + (t%pw)*gridN/pw
				cnt[ci]++
				for c := range cols {
					cell[ci*cols+c] += d.Data[t*cols+c]
				}
			}
			for ci := range cnt {
				for c := range cols {
					cell[ci*cols+c] /= max(cnt[ci], 1)
				}
			}
			grid = append(grid, cell...)
		}
		g.Release()
		gf, err := os.Create(strings.TrimSuffix(p, ".bds") + ".bfg") //nolint:gosec // the operator's directory
		if err != nil {
			fail(err)
		}
		_, _ = gf.WriteString("BFE1")
		_ = binary.Write(gf, binary.LittleEndian, uint32(len(grid))) //nolint:gosec // small
		_ = binary.Write(gf, binary.LittleEndian, grid)
		_ = gf.Close()
		f, err := os.Create(out) //nolint:gosec // the operator's directory
		if err != nil {
			fail(err)
		}
		_, _ = f.WriteString("BFE1")
		_ = binary.Write(f, binary.LittleEndian, uint32(len(pooled))) //nolint:gosec // small
		_ = binary.Write(f, binary.LittleEndian, pooled)
		_ = f.Close()
		if done++; done%500 == 0 {
			fmt.Printf("%d\n", done)
		}
	}
	fmt.Printf("%d written\n", done)
}

func lastFrame(path string) (frame []byte, h, w int, err error) {
	f, err := os.Open(path) //nolint:gosec // the operator's files
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = f.Close() }()
	var hdr struct {
		Magic   [4]byte
		K, H, W uint32
	}
	if err := binary.Read(f, binary.LittleEndian, &hdr); err != nil || string(hdr.Magic[:]) != "BHW1" {
		return nil, 0, 0, fmt.Errorf("%s: not a window", path)
	}
	h, w = int(hdr.H), int(hdr.W)
	size := int64(3 * h * w)
	if _, err := f.Seek(16+size*int64(hdr.K-1), io.SeekStart); err != nil {
		return nil, 0, 0, err
	}
	frame = make([]byte, size)
	if _, err := io.ReadFull(f, frame); err != nil {
		return nil, 0, 0, err
	}
	return frame, h, w, nil
}

func readMetric(path string) (d []float32, h, w int, fpx float32, err error) {
	f, err := os.Open(path) //nolint:gosec // the operator's files
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 1<<20)
	var hdr struct {
		Magic [4]byte
		H, W  uint32
		F     float32
	}
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil || string(hdr.Magic[:]) != "BMD1" {
		return nil, 0, 0, 0, fmt.Errorf("%s: not a metric depth", path)
	}
	d = make([]float32, hdr.H*hdr.W)
	if err := binary.Read(r, binary.LittleEndian, d); err != nil {
		return nil, 0, 0, 0, err
	}
	return d, int(hdr.H), int(hdr.W), hdr.F, nil
}

func bilinear(d []float32, h, w int, u, v float64) float32 {
	u, v = math.Min(math.Max(u, 0), float64(w-1)), math.Min(math.Max(v, 0), float64(h-1))
	x0, y0 := int(u), int(v)
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)
	fx, fy := float32(u-float64(x0)), float32(v-float64(y0))
	return (d[y0*w+x0]*(1-fx)+d[y0*w+x1]*fx)*(1-fy) + (d[y1*w+x0]*(1-fx)+d[y1*w+x1]*fx)*fy
}

func readFeatures(path string) ([]float64, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's files
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var hdr struct {
		Magic [4]byte
		N     uint32
	}
	if err := binary.Read(f, binary.LittleEndian, &hdr); err != nil || string(hdr.Magic[:]) != "BFE1" {
		return nil, fmt.Errorf("%s: not features", path)
	}
	v := make([]float32, hdr.N)
	if err := binary.Read(f, binary.LittleEndian, v); err != nil {
		return nil, err
	}
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	return out, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "shapeprobe:", err)
	os.Exit(1)
}

func read(path string) (*pic, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's files
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, 1<<20)
	var hdr struct {
		Magic             [4]byte
		W, H, Top, Bottom uint32
	}
	if err := binary.Read(r, binary.LittleEndian, &hdr); err != nil || string(hdr.Magic[:]) != "BDS1" {
		return nil, fmt.Errorf("%s: not a sample", path)
	}
	w, h := int(hdr.W), int(hdr.H)
	y := make([]byte, w*h)
	if _, err := readFull(r, y); err != nil {
		return nil, err
	}
	if _, err := r.Discard(2 * w * h); err != nil {
		return nil, err
	}
	depth, disp := make([]float32, w*h), make([]float32, w*h)
	if err := binary.Read(r, binary.LittleEndian, depth); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, disp); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(filepath.Base(path), ".bds")
	stretch := base[:strings.LastIndex(base, "-")]
	film := stretch
	if i := strings.LastIndex(stretch, "@"); i > 0 {
		film = stretch[:i]
	}
	p := &pic{stretch: stretch, film: film, w: w, h: h}
	// features: depth percentiles and moments, luma moments, the dark
	// fraction, the active rows' share
	var ds []float32
	var ly, lyy, dark, nl float64
	for rr := int(hdr.Top); rr < int(hdr.Bottom) && rr < h; rr += step {
		for c := 0; c < w; c += step {
			i := rr*w + c
			d := depth[i]
			if math.IsNaN(float64(d)) {
				continue
			}
			ds = append(ds, d)
			v := float64(y[i])
			ly += v
			lyy += v * v
			if v < 40 {
				dark++
			}
			nl++
			if q := disp[i]; !math.IsNaN(float64(q)) {
				p.x, p.y = append(p.x, d), append(p.y, q)
				p.n++
				p.sx += float64(d)
				p.sy += float64(q)
				p.sxx += float64(d) * float64(d)
				p.sxy += float64(d) * float64(q)
			}
		}
	}
	if len(ds) == 0 {
		return nil, fmt.Errorf("%s: no depth", path)
	}
	if md, mh, mw, fpx, err := readMetric(strings.TrimSuffix(path, ".bds") + ".bmd"); err == nil {
		// the metric depth covers the active rows, resized; sample it
		// bilinearly at the sure pixels
		top, bottom := int(hdr.Top), int(hdr.Bottom)
		fh := float64(fpx) * float64(w) / float64(mw)
		p.xm = make([]float32, 0, len(p.x))
		k := 0
		for rr := top; rr < bottom && rr < h; rr += step {
			for c := 0; c < w; c += step {
				i := rr*w + c
				if math.IsNaN(float64(depth[i])) || math.IsNaN(float64(disp[i])) {
					continue
				}
				u := (float64(c)+0.5)/float64(w)*float64(mw) - 0.5
				v := (float64(rr-top)+0.5)/float64(bottom-top)*float64(mh) - 0.5
				z := bilinear(md, mh, mw, u, v)
				xv := float32(fh / math.Max(float64(z), 0.05))
				p.xm = append(p.xm, xv)
				q := float64(p.y[k])
				p.nm++
				p.sxm += float64(xv)
				p.sym += q
				p.sxxm += float64(xv) * float64(xv)
				p.sxym += float64(xv) * q
				k++
			}
		}
		if k != len(p.x) {
			return nil, fmt.Errorf("%s: metric pixels %d of %d", path, k, len(p.x))
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	pct := func(q float64) float64 { return float64(ds[int(q*float64(len(ds)-1))]) }
	var dm, dv float64
	for _, d := range ds {
		dm += float64(d)
	}
	dm /= float64(len(ds))
	for _, d := range ds {
		dv += (float64(d) - dm) * (float64(d) - dm)
	}
	dv = math.Sqrt(dv / float64(len(ds)))
	lm := ly / nl
	var cs []float32
	for rr := int(hdr.Top) + (int(hdr.Bottom)-int(hdr.Top))/3; rr < int(hdr.Bottom)-(int(hdr.Bottom)-int(hdr.Top))/3 && rr < h; rr += step {
		for c := w / 3; c < 2*w/3; c += step {
			if d := depth[rr*w+c]; !math.IsNaN(float64(d)) {
				cs = append(cs, d)
			}
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i] < cs[j] })
	centre := pct(0.5)
	if len(cs) > 0 {
		centre = float64(cs[len(cs)/2])
	}
	// where the picture is sharpest: the depth weighted by the luma's
	// local contrast (the Laplacian's magnitude, squared), the focused
	// subject's depth if the shot has a shallow focus
	var fs, fw float64
	for rr := int(hdr.Top) + 2; rr+2 < int(hdr.Bottom) && rr+2 < h; rr += 2 {
		for c := 2; c+2 < w; c += 2 {
			i := rr*w + c
			d := depth[i]
			if math.IsNaN(float64(d)) {
				continue
			}
			lap := 4*float64(y[i]) - float64(y[i-1]) - float64(y[i+1]) - float64(y[i-w]) - float64(y[i+w])
			wt := lap * lap
			fs += wt * float64(d)
			fw += wt
		}
	}
	focus := pct(0.5)
	if fw > 0 {
		focus = fs / fw
	}
	p.feat = []float64{pct(0.05), pct(0.25), pct(0.5), pct(0.75), pct(0.95), dm, dv, lm / 255, math.Sqrt(math.Max(lyy/nl-lm*lm, 0)) / 255, dark / nl, float64(hdr.Bottom-hdr.Top) / float64(h), float64(w) / float64(h), centre, focus}
	return p, nil
}

func readFull(r *bufio.Reader, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := r.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func group(pics []*pic, key func(*pic) string) map[string][]*pic {
	g := map[string][]*pic{}
	for _, p := range pics {
		g[key(p)] = append(g[key(p)], p)
	}
	return g
}

func keys(m map[string][]*pic) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// fit is the LS fit over the pictures together, disparity = a depth + b,
// as a scale and offset of the width (half-size px over the half width).
func fit(ps []*pic) (scale, offset float64) {
	var n, sx, sy, sxx, sxy float64
	for _, p := range ps {
		n, sx, sy, sxx, sxy = n+p.n, sx+p.sx, sy+p.sy, sxx+p.sxx, sxy+p.sxy
	}
	a := (sxy - sx*sy/n) / math.Max(sxx-sx*sx/n, 1e-9)
	b := (sy - a*sx) / n
	w := float64(ps[0].w)
	return a / w, b / w
}

// mfit is the LS fit of disparity = B x + c over the pictures' metric
// feature x = f/Z, B in metres and c in half-size pixels.
func mfit(ps []*pic) (b, c float64) {
	var n, sx, sy, sxx, sxy float64
	for _, p := range ps {
		n, sx, sy, sxx, sxy = n+p.nm, sx+p.sxm, sy+p.sym, sxx+p.sxxm, sxy+p.sxym
	}
	b = (sxy - sx*sy/n) / math.Max(sxx-sx*sx/n, 1e-9)
	c = (sy - b*sx) / n
	return b, c
}

func mfits(g map[string][]*pic) map[string][2]float64 {
	out := map[string][2]float64{}
	for k, ps := range g {
		b, c := mfit(ps)
		out[k] = [2]float64{b, c}
	}
	return out
}

func mresid(p *pic, b, c float64) (robust, abs float64) {
	for i := range p.xm {
		r := b*float64(p.xm[i]) + c - float64(p.y[i])
		robust += math.Sqrt(r*r+1) - 1
		abs += math.Abs(r)
	}
	n := float64(len(p.xm))
	return robust / n, abs / n
}

// offsetFor is the LS offset of the pictures with the scale given.
func offsetFor(ps []*pic, scale float64) float64 {
	var n, sx, sy float64
	for _, p := range ps {
		n, sx, sy = n+p.n, sx+p.sx, sy+p.sy
	}
	w := float64(ps[0].w)
	return (sy - scale*w*sx) / n / w
}

// scaleFor is the LS scale of the pictures with the offset given.
func scaleFor(ps []*pic, offset float64) float64 {
	var sxx, sxy, sx float64
	for _, p := range ps {
		sxx, sxy, sx = sxx+p.sxx, sxy+p.sxy, sx+p.sx
	}
	w := float64(ps[0].w)
	return (sxy - offset*w*sx) / math.Max(sxx, 1e-9) / w
}

func fits(g map[string][]*pic) map[string][2]float64 {
	out := map[string][2]float64{}
	for k, ps := range g {
		a, b := fit(ps)
		out[k] = [2]float64{a, b}
	}
	return out
}

// resid is the mean robust residual sqrt(r^2+1)-1 and mean |r| of
// disparity = a depth + b (a, b in half-size pixels) over the sure pixels.
func resid(p *pic, a, b float64) (robust, abs float64) {
	for i := range p.x {
		r := a*float64(p.x[i]) + b - float64(p.y[i])
		robust += math.Sqrt(r*r+1) - 1
		abs += math.Abs(r)
	}
	n := float64(len(p.x))
	return robust / n, abs / n
}

func stretchFeat(ps []*pic) []float64 {
	f := make([]float64, len(ps[0].feat))
	for _, p := range ps {
		for i, v := range p.feat {
			f[i] += v / float64(len(ps))
		}
	}
	return f
}

type model struct {
	mu, sg, w []float64
	b         float64
}

func (m model) apply(f []float64) float64 {
	y := m.b
	for i, v := range f {
		y += m.w[i] * (v - m.mu[i]) / m.sg[i]
	}
	return y
}

// (ridge standardises its own columns; already standardised ones pass
// through with mean 0 and deviation 1, so apply is right either way.)

// ridge is a ridge regression of y on the standardised features.
func ridge(X [][]float64, y []float64, lambda float64) model {
	n, d := len(X), len(X[0])
	mu, sg := make([]float64, d), make([]float64, d)
	for j := range d {
		for i := range n {
			mu[j] += X[i][j] / float64(n)
		}
		for i := range n {
			sg[j] += (X[i][j] - mu[j]) * (X[i][j] - mu[j]) / float64(n)
		}
		sg[j] = math.Max(math.Sqrt(sg[j]), 1e-9)
	}
	ym := mean(y)
	// normal equations (Z'Z + lambda I) w = Z'(y - ym)
	A := make([][]float64, d)
	for j := range A {
		A[j] = make([]float64, d+1)
	}
	for i := range n {
		z := make([]float64, d)
		for j := range d {
			z[j] = (X[i][j] - mu[j]) / sg[j]
		}
		for j := range d {
			for k := range d {
				A[j][k] += z[j] * z[k]
			}
			A[j][d] += z[j] * (y[i] - ym)
		}
	}
	for j := range d {
		A[j][j] += lambda
	}
	// Gaussian elimination
	for c := range d {
		piv := c
		for r := c + 1; r < d; r++ {
			if math.Abs(A[r][c]) > math.Abs(A[piv][c]) {
				piv = r
			}
		}
		A[c], A[piv] = A[piv], A[c]
		for r := range d {
			if r == c {
				continue
			}
			f := A[r][c] / A[c][c]
			for k := c; k <= d; k++ {
				A[r][k] -= f * A[c][k]
			}
		}
	}
	w := make([]float64, d)
	for j := range d {
		w[j] = A[j][d] / A[j][j]
	}
	return model{mu: mu, sg: sg, w: w, b: ym}
}

func mean(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(max(len(v), 1))
}

func sd(v []float64) float64 {
	m := mean(v)
	var s float64
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(max(len(v), 1)))
}
