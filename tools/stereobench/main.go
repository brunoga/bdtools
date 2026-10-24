// Command stereobench measures the 2D to 3D conversion against native 3D:
//
//	stereobench [-every 4] [-model FILE] [-csv FILE] CLIP...
//
// Each CLIP is a full side-by-side Matroska file cut from a 3D disc (the
// left eye, then the right). Its left eye is converted as --to-3d converts
// a 2D film, and the right eye made from it compared with the disc's:
//
//   - depth: the disparity measured between the disc's eyes (semi-global
//     matching, where it is sure) against the depth network's, fitted to
//     it (a scale and an offset a picture, since a conversion need not
//     copy the disc's depth budget): their correlation, the error left
//     and the share of pixels off by more than 2 pixels;
//   - grading: the disc's disparity range (5th percentile, median, 95th)
//     beside the conversion's (its default shape, unfitted);
//   - the right eye, made from the left eye for five disparities, against
//     the disc's: PSNR (luma, the active area less the sides), PSNR in the
//     holes alone (what the left eye did not see, filled) and VMAF. The
//     five: none at all, flat at the disc's median ("flat"); the disc's own,
//     measured ("disc": what the renderer and filler do with perfect
//     depth); the network's fitted ("fitted": the depth's shape); the
//     conversion's ("ours": shape and grading as they are). "clip" is the
//     network's fitted once for the whole clip, not a picture at a time:
//     what a grading that only follows shots could reach.
//
// Every picture goes through the depth network (it is temporal); one in
// every few is measured.
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/convert"
	"github.com/brunoga/bdtools/internal/depth"
	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/internal/vmaf"
	"github.com/brunoga/bdtools/mvc"
)

// margin is how much of the picture's sides is left out of the pictures'
// comparison: what comes into view there is seen by neither the left eye
// nor the conversion.
const margin = 64

var methods = []string{"flat", "disc", "fitted", "clip", "ours"}

var (
	guide   stereo.Guide
	shape   stereo.Shape
	dumpDir string
)

// clipResult is a clip's measures.
type clipResult struct {
	name                 string
	frames               int
	matched              float64 // the share of pixels matched surely
	corr, epe, bad2      float64
	epeCurve, bad2Curve  float64    // after fitting a monotone curve, not a line
	disc, ours           [3]float64 // p5, median, p95 disparity
	psnr, holePSNR, vmaf map[string]float64
	holes                map[string]float64 // the share of the area filled
	clipA, clipB         float64            // the clip's fit: disparity = a depth + b
	raw                  [3]float64         // the network's depth, p5, median, p95
}

func main() {
	every := flag.Int("every", 4, "measure one picture in this many")
	model := flag.String("model", "", "the depth network's checkpoint (default: --to-3d's, in the user's cache)")
	half := flag.Bool("half", true, "on the GPU, the matrix products' inputs in half precision (the tensor cores), as --to-3d's, unless the model's overflow it")
	csvPath := flag.String("csv", "", "also write the clips' measures to this CSV file")
	framesPath := flag.String("frames", "", "write each measured picture's features and its fit to the disc to this CSV file (the grading's training data)")
	flag.IntVar(&guide.Radius, "guide-radius", stereo.DefaultGuide.Radius, "the depth's edge filter's radius (0: off)")
	flag.Float64Var(&guide.Eps, "guide-eps", stereo.DefaultGuide.Eps, "the depth's edge filter's regularisation")
	flag.Float64Var(&shape.Budget, "budget", 0.019, "with -scale 0: each picture's depth range normalised to this budget (a fraction of the width)")
	flag.Float64Var(&shape.Screen, "screen", 0.57, "with -scale 0: the screen's place in that range (0 far, 1 near)")
	flag.Float64Var(&shape.Scale, "scale", stereo.DefaultShape.Scale, "the depth as it is: disparity = scale depth + offset (fractions of the width; by default the network's, stereo.ShapeFor); 0: normalised a picture at a time")
	flag.Float64Var(&shape.Offset, "offset", stereo.DefaultShape.Offset, "with -scale: the offset")
	flag.StringVar(&dumpDir, "dump", "", "write each measured picture's maps to this directory, as PNG")
	flag.StringVar(&refinePath, "refine", "", "refine the depth by these weights (tools/depthtrain's, or builtin), mapped by the refined depth's shape (stereo.ShapeFor) unless -scale or -offset")
	flag.StringVar(&samplesDir, "samples", "", "write each measured picture's depth refinement training sample (its picture, the network's depth, the disc's disparity) to this directory")
	tailPath := flag.String("tail", "", "the depth network's tail weights (tools/depthtrain -tail's) in place of the checkpoint's")
	flag.Parse()
	if refinePath != "" {
		if refinePath == "builtin" {
			refine = stereo.BuiltinRefine()
		} else {
			var err error
			if refine, err = stereo.ReadRefine(refinePath); err != nil {
				fail(err)
			}
		}
	}
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: stereobench [-every N] [-model FILE] [-csv FILE] CLIP...")
		os.Exit(2)
	}
	if *model == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			fail(err)
		}
		*model = filepath.Join(dir, "bdtools", "video_depth_anything_vits.pth")
	}
	m, err := depth.Load(*model)
	if err != nil {
		fail(err)
	}
	if *tailPath != "" {
		w, err := depth.ReadTail(*tailPath)
		if err == nil {
			err = m.SetTail(w)
		}
		if err != nil {
			fail(err)
		}
	}
	// the network's (and the refined depth's) mapping, unless one is asked
	// for, as --to-3d's
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["scale"] && !set["offset"] {
		d := stereo.ShapeFor(m.Size(), refine != nil)
		shape.Scale, shape.Offset = d.Scale, d.Offset
	}
	if g, err := nn.NewGPU(); err == nil {
		g.Half = *half && m.HalfSafe()
		m.Use(g)
		fmt.Fprintln(os.Stderr, "stereobench: the depth on", g.Name())
	}
	if *framesPath != "" {
		f, err := os.Create(*framesPath) //nolint:gosec // the operator's path
		if err != nil {
			fail(err)
		}
		framesOut = csv.NewWriter(f)
		_ = framesOut.Write(featureHead)
		defer func() {
			framesOut.Flush()
			if err := errors.Join(framesOut.Error(), f.Close()); err != nil {
				fail(err)
			}
		}()
	}
	var all []clipResult
	for _, clip := range flag.Args() {
		res, err := bench(m, clip, max(*every, 1))
		if err != nil {
			fail(fmt.Errorf("%s: %w", clip, err))
		}
		if !strings.Contains(clip, "@") {
			report(res[0])
		}
		all = append(all, res...)
	}
	if len(all) > 1 {
		report(mean(all))
	}
	if *csvPath != "" {
		if err := writeCSV(*csvPath, all); err != nil {
			fail(err)
		}
	}
}

// errNothing says a clip had no picture to measure.
var errNothing = errors.New("no pictures measured")

// window is a stretch of a file to measure, as a clip of its own.
type window struct{ start, end time.Duration }

// parseClip reads a clip argument: a file, or file@START+DUR[,START+DUR...]
// for stretches of it (one decode of the file for them all).
func parseClip(arg string) (string, []window, error) {
	path, spec, ok := strings.Cut(arg, "@")
	if !ok {
		return arg, nil, nil
	}
	var ws []window
	for _, w := range strings.Split(spec, ",") {
		st, du, ok := strings.Cut(w, "+")
		if !ok {
			return "", nil, fmt.Errorf("%q: a window is START+DURATION", w)
		}
		a, err := time.ParseDuration(st)
		if err != nil {
			return "", nil, err
		}
		d, err := time.ParseDuration(du)
		if err != nil {
			return "", nil, err
		}
		ws = append(ws, window{a, a + d})
	}
	slices.SortFunc(ws, func(x, y window) int { return int(x.start - y.start) })
	return path, ws, nil
}

// bench measures a clip argument: a result for the file, or for each of
// its windows.
func bench(m *depth.Model, arg string, every int) ([]clipResult, error) {
	path, ws, err := parseClip(arg)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	o := convert.DefaultOptions()
	o.TempDir = os.TempDir()
	if ws == nil {
		cb := newClipBench(m, name, every)
		err := convert.DecodeFrames(context.Background(), runtime.GOOS, o, path, nil, cb.frame)
		if err != nil {
			return nil, err
		}
		r, err := cb.finish()
		return []clipResult{r}, err
	}
	var out []clipResult
	cur := -1
	var cb *clipBench
	at := func(t time.Duration) int {
		for i, w := range ws {
			if t >= w.start && t < w.end {
				return i
			}
		}
		return -1
	}
	done := func() error {
		if cb == nil {
			return nil
		}
		r, err := cb.finish()
		if errors.Is(err, errNothing) {
			fmt.Fprintf(os.Stderr, "stereobench: %s: nothing to measure, left out\n", cb.res.name)
			cb = nil
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", cb.res.name, err)
		}
		report(r)
		out = append(out, r)
		cb = nil
		return nil
	}
	var t time.Duration
	keep := func(d time.Duration) bool {
		t = d
		return at(d) >= 0 || d >= ws[len(ws)-1].end
	}
	err = convert.DecodeFrames(context.Background(), runtime.GOOS, o, path, keep, func(sbs *mvc.Frame) error {
		i := at(t)
		if i < 0 {
			return convert.ErrDecodeDone // past the last window
		}
		if i != cur {
			if err := done(); err != nil {
				return err
			}
			cur = i
			w := ws[i]
			cb = newClipBench(m, fmt.Sprintf("%s@%s", name, w.start), every)
		}
		return cb.frame(sbs)
	})
	if err != nil {
		return out, err
	}
	return out, done()
}

// clipBench is a clip's measures as its pictures come.
type clipBench struct {
	res     clipResult
	conv    *stereo.Converter
	every   int
	scorers map[string]*vmaf.Scorer
	// the depth network's tail inputs of the frame, when it is sampled
	wantTail bool
	tail     *tailInput
	discD    []float32
	oursD    []float32
	sums     struct {
		matched, corr, epe, bad2 float64
		epeCurve, bad2Curve      float64
		mse, holeMSE, holeN      map[string]float64
		n                        map[string]float64
	}
	// the measured pictures, rendered once all are in (the clip's fit
	// needs them all)
	recs []record
	gl   struct{ n, sx, sy, sxx, sxy float64 }
	k    int
}

type record struct {
	left, right     *mvc.Frame
	raw, disp, full []float32
	dm              *stereo.Disparity
	hr              []float32
	a, b            float64
	med             float32
	top, bottom, k  int
}

func newClipBench(m *depth.Model, name string, every int) *clipBench {
	c := &clipBench{every: every, scorers: map[string]*vmaf.Scorer{}}
	c.res = clipResult{name: name, psnr: map[string]float64{}, holePSNR: map[string]float64{}, vmaf: map[string]float64{}, holes: map[string]float64{}}
	c.conv = stereo.NewConverter(m, shape)
	if samplesDir != "" {
		m.Tap = func(t depth.TailInput) {
			if c.wantTail {
				c.tail = downloadTail(m.Backend(), t)
			}
		}
	}
	c.conv.Guide = guide
	c.conv.Refine = refine
	c.sums.mse, c.sums.holeMSE, c.sums.holeN, c.sums.n = map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}
	return c
}

func (c *clipBench) frame(sbs *mvc.Frame) error {
	left, right := split(sbs)
	c.wantTail, c.tail = c.k%c.every == 0, nil
	raw, disp, err := c.conv.Next(left)
	if err != nil {
		return err
	}
	if c.k++; (c.k-1)%c.every != 0 {
		return nil
	}
	top, bottom := stereo.Bars(left)
	w := left.Width
	dm := stereo.Match(left, right)
	hr := halfDepth(raw, w, left.Height)
	a, b, corr, matched := fit(dm, hr, top/2, bottom/2)
	if matched < 0.05 {
		return nil // nothing to measure on (a black picture)
	}
	c.res.frames++
	c.sums.matched += matched
	c.sums.corr += corr
	var e, bad, n float64
	for i, ok := range dm.Valid {
		if y := i / dm.W; !ok || y < top/2 || y >= bottom/2 || math.IsNaN(float64(hr[i])) {
			continue
		}
		d := math.Abs(a*float64(hr[i]) + b - float64(dm.D[i]))
		e += d
		if d > 2 {
			bad++
		}
		n++
		c.discD = append(c.discD, dm.D[i])
	}
	c.sums.epe += e / n
	c.sums.bad2 += bad / n
	ec, bc := curveError(dm, hr, top/2, bottom/2)
	c.sums.epeCurve += ec
	c.sums.bad2Curve += bc
	for y := top; y < bottom; y += 2 {
		for x := 0; x < w; x += 2 {
			c.oursD = append(c.oursD, disp[y*w+x])
		}
	}
	med := median(slices.Clone(c.discD[len(c.discD)-int(n):]))
	if samplesDir != "" {
		if err := writeSample(c.res.name, c.k-1, left, dm, hr, top, bottom); err != nil {
			return err
		}
		if c.tail != nil {
			if err := writeTail(c.res.name, c.k-1, c.tail); err != nil {
				return err
			}
		}
	}
	if framesOut != nil {
		writeFeatures(c.res.name, c.k-1, left, raw, dm, hr, a, b, corr, matched, top, bottom)
	}
	c.recs = append(c.recs, record{left: left, right: right, raw: raw, disp: disp, full: discDisparity(dm, w, left.Height),
		dm: dm, hr: hr, a: a, b: b, med: med, top: top, bottom: bottom, k: c.k - 1})
	for y := top / 2; y < bottom/2; y++ {
		for x := range dm.W {
			if i := y*dm.W + x; dm.Valid[i] && !math.IsNaN(float64(hr[i])) {
				X, Y := float64(hr[i]), float64(dm.D[i])
				c.gl.n++
				c.gl.sx += X
				c.gl.sy += Y
				c.gl.sxx += X * X
				c.gl.sxy += X * Y
			}
		}
	}
	return nil
}

func (c *clipBench) finish() (clipResult, error) {
	res, sums, gl := c.res, &c.sums, &c.gl
	if res.frames == 0 {
		return res, errNothing
	}
	// the clip's one scale and offset, for "clip"
	ca := (gl.sxy - gl.sx*gl.sy/gl.n) / max(gl.sxx-gl.sx*gl.sx/gl.n, 1e-12)
	cb := (gl.sy - ca*gl.sx) / gl.n
	for _, r := range c.recs {
		left, right, w := r.left, r.right, r.left.Width
		affine := func(a, b float64) []float32 {
			out := make([]float32, len(r.raw))
			for i, v := range r.raw {
				if !math.IsNaN(float64(v)) {
					out[i] = float32(a*float64(v) + b)
				}
			}
			return out
		}
		flat := make([]float32, len(r.raw))
		for i, v := range r.raw {
			if !math.IsNaN(float64(v)) {
				flat[i] = r.med
			}
		}
		var fittedEye *mvc.Frame
		for _, name := range methods {
			var d []float32
			switch name {
			case "flat":
				d = flat
			case "disc":
				d = r.full
			case "fitted":
				d = affine(r.a, r.b)
			case "clip":
				d = affine(ca, cb)
			case "ours":
				d = r.disp
			}
			eye, holes := stereo.Render(left, d, -1)
			if name == "fitted" {
				fittedEye = eye
			}
			var se, hse, hn, cnt float64
			for y := r.top; y < r.bottom; y++ {
				for x := margin; x < w-margin; x++ {
					i := y*w + x
					diff := float64(eye.Y[i]) - float64(right.Y[y*right.StrideY+x])
					se += diff * diff
					cnt++
					if holes[i] {
						hse += diff * diff
						hn++
					}
				}
			}
			sums.mse[name] += se
			sums.n[name] += cnt
			sums.holeMSE[name] += hse
			sums.holeN[name] += hn
			sc := c.scorers[name]
			if sc == nil {
				var err error
				if sc, err = vmaf.NewScorer(left.Height); err != nil {
					return res, err
				}
				c.scorers[name] = sc
			}
			sc.Add(luma(right), luma(eye))
		}
		if dumpDir != "" {
			if err := dump(filepath.Join(dumpDir, fmt.Sprintf("%s-%04d.png", res.name, r.k)), left, right, fittedEye, r.dm, r.hr, r.a, r.b); err != nil {
				return res, err
			}
		}
	}
	res.clipA, res.clipB = ca, cb
	var rawS []float32
	for _, r := range c.recs {
		for i := 0; i < len(r.raw); i += 97 {
			if !math.IsNaN(float64(r.raw[i])) {
				rawS = append(rawS, r.raw[i])
			}
		}
	}
	res.raw = percentiles(rawS)
	f := float64(res.frames)
	res.matched, res.corr, res.epe, res.bad2 = sums.matched/f, sums.corr/f, sums.epe/f, sums.bad2/f
	res.epeCurve, res.bad2Curve = sums.epeCurve/f, sums.bad2Curve/f
	res.disc, res.ours = percentiles(c.discD), percentiles(c.oursD)
	for _, name := range methods {
		res.psnr[name] = psnr(sums.mse[name] / sums.n[name])
		res.holePSNR[name] = psnr(sums.holeMSE[name] / max(sums.holeN[name], 1))
		res.holes[name] = sums.holeN[name] / sums.n[name]
		var v []float64
		for _, sc := range c.scorers[name].Scores() {
			v = append(v, sc.VMAF)
		}
		res.vmaf[name] = vmaf.Pool(v).Mean
	}
	c.recs = nil
	return res, nil
}

// split is a full side-by-side frame's two eyes.
func split(f *mvc.Frame) (left, right *mvc.Frame) {
	w := f.Width / 2
	cw := (w + 1) / 2
	eye := func(x0 int) *mvc.Frame {
		e := &mvc.Frame{Width: w, Height: f.Height, StrideY: w, StrideC: cw, PTS: f.PTS,
			Y: make([]byte, w*f.Height), Cb: make([]byte, cw*((f.Height+1)/2)), Cr: make([]byte, cw*((f.Height+1)/2))}
		for y := range f.Height {
			copy(e.Y[y*w:(y+1)*w], f.Y[y*f.StrideY+x0:])
		}
		for y := range (f.Height + 1) / 2 {
			copy(e.Cb[y*cw:(y+1)*cw], f.Cb[y*f.StrideC+x0/2:])
			copy(e.Cr[y*cw:(y+1)*cw], f.Cr[y*f.StrideC+x0/2:])
		}
		return e
	}
	return eye(0), eye(w)
}

// halfDepth is a depth map at half its size (the 2x2 averaged, NaN where
// any of them is).
func halfDepth(d []float32, w, h int) []float32 {
	hw, hh := w/2, h/2
	out := make([]float32, hw*hh)
	for y := range hh {
		for x := range hw {
			i := 2*y*w + 2*x
			out[y*hw+x] = (d[i] + d[i+1] + d[i+w] + d[i+w+1]) / 4
		}
	}
	return out
}

// fit fits the disc's disparity by the network's depth (a scale and an
// offset) over the sure matches of the active rows [top, bottom), giving
// their correlation and the share of pixels matched.
func fit(dm *stereo.Disparity, hr []float32, top, bottom int) (a, b, corr, matched float64) {
	var n, sx, sy, sxx, syy, sxy float64
	for y := top; y < bottom; y++ {
		for x := range dm.W {
			i := y*dm.W + x
			if !dm.Valid[i] || math.IsNaN(float64(hr[i])) {
				continue
			}
			X, Y := float64(hr[i]), float64(dm.D[i])
			n++
			sx += X
			sy += Y
			sxx += X * X
			syy += Y * Y
			sxy += X * Y
		}
	}
	if n < 2 {
		return 0, 0, 0, 0
	}
	vx, vy, cxy := sxx-sx*sx/n, syy-sy*sy/n, sxy-sx*sy/n
	if vx > 0 {
		a = cxy / vx
	}
	b = (sy - a*sx) / n
	if vx > 0 && vy > 0 {
		corr = cxy / math.Sqrt(vx*vy)
	}
	return a, b, corr, n / float64(dm.W*(bottom-top))
}

// curveError is the depth's error once the disc's disparity is fitted by
// a monotone curve of it (the pixels in 32 bins of the depth, each bin's
// mean disparity made non-decreasing, interpolated between bins): what is
// left is the depth's order wrong, not its scale.
func curveError(dm *stereo.Disparity, hr []float32, top, bottom int) (epe, bad2 float64) {
	type pair struct{ x, y float32 }
	var ps []pair
	for y := top; y < bottom; y++ {
		for x := range dm.W {
			i := y*dm.W + x
			if dm.Valid[i] && !math.IsNaN(float64(hr[i])) {
				ps = append(ps, pair{hr[i], dm.D[i]})
			}
		}
	}
	const bins = 32
	if len(ps) < bins {
		return 0, 0
	}
	slices.SortFunc(ps, func(a, b pair) int {
		if a.x < b.x {
			return -1
		}
		if a.x > b.x {
			return 1
		}
		return 0
	})
	cx, cy := make([]float64, bins), make([]float64, bins)
	for b := range bins {
		seg := ps[b*len(ps)/bins : (b+1)*len(ps)/bins]
		for _, p := range seg {
			cx[b] += float64(p.x)
			cy[b] += float64(p.y)
		}
		cx[b] /= float64(len(seg))
		cy[b] /= float64(len(seg))
	}
	// pool adjacent violators (equal weights)
	type block struct{ v, n float64 }
	var st []block
	for _, v := range cy {
		st = append(st, block{v, 1})
		for len(st) > 1 && st[len(st)-2].v > st[len(st)-1].v {
			a, b := st[len(st)-2], st[len(st)-1]
			st = append(st[:len(st)-2], block{(a.v*a.n + b.v*b.n) / (a.n + b.n), a.n + b.n})
		}
	}
	k := 0
	for _, b := range st {
		for range int(b.n) {
			cy[k] = b.v
			k++
		}
	}
	curve := func(x float64) float64 {
		j, _ := slices.BinarySearch(cx, x)
		switch {
		case j == 0:
			return cy[0]
		case j >= bins:
			return cy[bins-1]
		}
		t := (x - cx[j-1]) / max(cx[j]-cx[j-1], 1e-12)
		return cy[j-1] + t*(cy[j]-cy[j-1])
	}
	for _, p := range ps {
		d := math.Abs(curve(float64(p.x)) - float64(p.y))
		epe += d
		if d > 2 {
			bad2++
		}
	}
	n := float64(len(ps))
	return epe / n, bad2 / n
}

// discDisparity is the disc's measured disparity at the picture's size:
// each row's unsure runs filled from their farther side, then each value
// spread over the 2x2 it measured.
func discDisparity(dm *stereo.Disparity, w, h int) []float32 {
	half := slices.Clone(dm.D)
	for y := range dm.H {
		row, ok := half[y*dm.W:(y+1)*dm.W], dm.Valid[y*dm.W:(y+1)*dm.W]
		for x := 0; x < dm.W; {
			if ok[x] {
				x++
				continue
			}
			a := x
			for x < dm.W && !ok[x] {
				x++
			}
			var v float32
			switch {
			case a == 0 && x == dm.W:
				v = 0
			case a == 0:
				v = row[x]
			case x == dm.W:
				v = row[a-1]
			default:
				v = min(row[a-1], row[x])
			}
			for i := a; i < x; i++ {
				row[i] = v
			}
		}
	}
	out := make([]float32, w*h)
	for y := range h {
		for x := range w {
			out[y*w+x] = half[min(y/2, dm.H-1)*dm.W+min(x/2, dm.W-1)]
		}
	}
	return out
}

func luma(f *mvc.Frame) vmaf.Frame {
	v := vmaf.Frame{W: f.Width, H: f.Height, BPC: 8, Y: make([]uint16, f.Width*f.Height)}
	for y := range f.Height {
		for x := range f.Width {
			v.Y[y*f.Width+x] = uint16(f.Y[y*f.StrideY+x])
		}
	}
	return v
}

func psnr(mse float64) float64 {
	if mse <= 0 {
		return 100
	}
	return 10 * math.Log10(255*255/mse)
}

func median(v []float32) float32 {
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	return v[len(v)/2]
}

func percentiles(v []float32) [3]float64 {
	if len(v) == 0 {
		return [3]float64{}
	}
	slices.Sort(v)
	p := func(q float64) float64 { return float64(v[int(q*float64(len(v)-1))]) }
	return [3]float64{p(0.05), p(0.5), p(0.95)}
}

// mean is the clips' measures averaged (the pictures' measures each
// clip's equally).
func mean(all []clipResult) clipResult {
	r := clipResult{name: "mean", psnr: map[string]float64{}, holePSNR: map[string]float64{}, vmaf: map[string]float64{}, holes: map[string]float64{}}
	n := float64(len(all))
	for _, c := range all {
		r.frames += c.frames
		r.matched += c.matched / n
		r.corr += c.corr / n
		r.epe += c.epe / n
		r.bad2 += c.bad2 / n
		r.epeCurve += c.epeCurve / n
		r.bad2Curve += c.bad2Curve / n
		for i := range 3 {
			r.disc[i] += c.disc[i] / n
			r.ours[i] += c.ours[i] / n
		}
		for _, m := range methods {
			r.psnr[m] += c.psnr[m] / n
			r.holePSNR[m] += c.holePSNR[m] / n
			r.vmaf[m] += c.vmaf[m] / n
			r.holes[m] += c.holes[m] / n
		}
	}
	return r
}

func report(r clipResult) {
	fmt.Printf("== %s: %d pictures measured, %.0f%% of their pixels matched\n", r.name, r.frames, 100*r.matched)
	fmt.Printf("depth: correlation %.3f, error after fitting %.2f px, %.1f%% off by more than 2 px\n", r.corr, r.epe, 100*r.bad2)
	fmt.Printf("       after fitting a curve: %.2f px, %.1f%% off by more than 2 px\n", r.epeCurve, 100*r.bad2Curve)
	fmt.Printf("disparity p5/median/p95: disc %.1f / %.1f / %.1f px, ours %.1f / %.1f / %.1f px\n",
		r.disc[0], r.disc[1], r.disc[2], r.ours[0], r.ours[1], r.ours[2])
	if r.clipA != 0 {
		fmt.Printf("network depth p5/median/p95 %.3f / %.3f / %.3f; the clip's fit: disparity = %.2f depth %+.2f px\n",
			r.raw[0], r.raw[1], r.raw[2], r.clipA, r.clipB)
	}
	fmt.Printf("%-8s %7s %7s %6s %7s\n", "right", "PSNR", "holes", "area", "VMAF")
	for _, m := range methods {
		fmt.Printf("%-8s %7.2f %7.2f %5.1f%% %7.2f\n", m, r.psnr[m], r.holePSNR[m], 100*r.holes[m], r.vmaf[m])
	}
}

func writeCSV(path string, all []clipResult) error {
	f, err := os.Create(path) //nolint:gosec // the operator's path
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	head := []string{"clip", "pictures", "matched", "corr", "epe", "bad2", "epe_curve", "bad2_curve", "disc_p5", "disc_p50", "disc_p95", "ours_p5", "ours_p50", "ours_p95"}
	for _, m := range methods {
		head = append(head, m+"_psnr", m+"_hole_psnr", m+"_holes", m+"_vmaf")
	}
	_ = w.Write(head)
	ff := func(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }
	for _, c := range all {
		row := []string{c.name, strconv.Itoa(c.frames), ff(c.matched), ff(c.corr), ff(c.epe), ff(c.bad2), ff(c.epeCurve), ff(c.bad2Curve),
			ff(c.disc[0]), ff(c.disc[1]), ff(c.disc[2]), ff(c.ours[0]), ff(c.ours[1]), ff(c.ours[2])}
		for _, m := range methods {
			row = append(row, ff(c.psnr[m]), ff(c.holePSNR[m]), ff(c.holes[m]), ff(c.vmaf[m]))
		}
		_ = w.Write(row)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "stereobench:", err)
	os.Exit(1)
}
