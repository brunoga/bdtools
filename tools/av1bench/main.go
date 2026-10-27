// Command av1bench measures the AV1 encoder (internal/av1) against
// SVT-AV1 (through ffmpeg's libsvtav1): each clip coded at a few
// quantizers by each configuration, decoded by libdav1d (ffmpeg), its
// size, speed and quality (PSNR of each plane, SSIM, VMAF, by
// internal/vmaf) recorded, and the configurations compared by their
// BD-rates against an anchor's, per metric, with their speeds.
//
//	av1bench -clips DIR -res DIR -run ours:0,svt:4,svt:8 -anchor svt:4
//
// The clips are 8-bit 4:2:0 y4m files. A configuration is ours:SPEED or
// svt:PRESET, with @TAG for a variant (a build being tried): its points
// are kept in -res and coded again only with -force. One encode runs at
// a time, each with the whole machine.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/av1"
	"github.com/brunoga/bdtools/internal/vmaf"
)

// point is one clip coded at one quantizer.
type point struct {
	Q       int     `json:"q"`
	Bytes   int     `json:"bytes"`
	Frames  int     `json:"frames"`
	Seconds float64 `json:"seconds"` // wall
	CPU     float64 `json:"cpu"`     // seconds
	PSNRY   float64 `json:"psnr_y"`
	PSNRU   float64 `json:"psnr_u"`
	PSNRV   float64 `json:"psnr_v"`
	SSIM    float64 `json:"ssim"`
	VMAF    float64 `json:"vmaf"`
}

// psnrYUV weighs the planes 6:1:1.
func (p point) psnrYUV() float64 { return (6*p.PSNRY + p.PSNRU + p.PSNRV) / 8 }

type metric struct {
	name string
	of   func(point) float64
}

var metrics = []metric{
	{"PSNR-Y", func(p point) float64 { return p.PSNRY }},
	{"PSNR-YUV", point.psnrYUV},
	{"SSIM", func(p point) float64 { return -10 * math.Log10(max(1-p.SSIM, 1e-10)) }}, // in dB
	{"VMAF", func(p point) float64 { return p.VMAF }},
}

func main() {
	clips := flag.String("clips", "", "the clips' directory (*.y4m)")
	res := flag.String("res", "", "the results' directory")
	run := flag.String("run", "ours:0,svt:4", "the configurations, comma separated")
	anchor := flag.String("anchor", "svt:4", "the configuration the BD-rates are against")
	only := flag.String("only", "", "only these clips (names without .y4m, comma separated)")
	oursQ := flag.String("ours-q", "60,100,140,180", "our quantizer indexes")
	svtCRF := flag.String("svt-crf", "24,30,36,42,48", "SVT-AV1's CRFs")
	force := flag.Bool("force", false, "code the points again")
	cpuProfile := flag.String("cpuprofile", "", "a CPU profile of the run to this file")
	flag.Parse()
	if *clips == "" || *res == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			fail(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			fail(err)
		}
		defer pprof.StopCPUProfile()
	}
	files, _ := filepath.Glob(filepath.Join(*clips, "*.y4m"))
	sort.Strings(files)
	if *only != "" {
		keep := map[string]bool{}
		for _, n := range strings.Split(*only, ",") {
			keep[n] = true
		}
		var f2 []string
		for _, f := range files {
			if keep[clipName(f)] {
				f2 = append(f2, f)
			}
		}
		files = f2
	}
	if len(files) == 0 {
		fail(errors.New("no clips"))
	}
	configs := strings.Split(*run, ",")
	if !contains(configs, *anchor) {
		configs = append([]string{*anchor}, configs...)
	}
	all := map[string]map[string][]point{} // config, clip
	for _, c := range configs {
		all[c] = map[string][]point{}
		qs := ints(*oursQ)
		if strings.HasPrefix(c, "svt:") {
			qs = ints(*svtCRF)
		}
		for _, f := range files {
			for _, q := range qs {
				p, err := measure(*res, c, f, q, *force)
				if err != nil {
					fail(fmt.Errorf("%s %s q %d: %w", c, clipName(f), q, err))
				}
				all[c][clipName(f)] = append(all[c][clipName(f)], p)
			}
		}
	}
	report(os.Stdout, all, configs, *anchor, files)
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func ints(s string) []int {
	var out []int
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			fail(err)
		}
		out = append(out, n)
	}
	return out
}

func clipName(f string) string { return strings.TrimSuffix(filepath.Base(f), ".y4m") }

func fail(err error) {
	fmt.Fprintln(os.Stderr, "av1bench:", err)
	os.Exit(1)
}

// measure gives a configuration's point for a clip and quantizer, from
// the results directory unless force.
func measure(res, config, clip string, q int, force bool) (point, error) {
	dir := filepath.Join(res, strings.ReplaceAll(config, ":", "-"))
	file := filepath.Join(dir, fmt.Sprintf("%s-%d.json", clipName(clip), q))
	var p point
	if !force {
		if b, err := os.ReadFile(file); err == nil { //nolint:gosec // the results' own file
			return p, json.Unmarshal(b, &p)
		}
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return p, err
	}
	kind, arg, _ := strings.Cut(config, ":")
	arg, _, _ = strings.Cut(arg, "@")
	n, err := strconv.Atoi(arg)
	if err != nil {
		return p, fmt.Errorf("configuration %q: %w", config, err)
	}
	stream := filepath.Join(dir, fmt.Sprintf("%s-%d.obu", clipName(clip), q))
	switch kind {
	case "ours":
		p, err = encodeOurs(clip, stream, n, q)
	case "svt":
		p, err = encodeSVT(clip, stream, n, q)
	default:
		err = fmt.Errorf("configuration %q: ours or svt", config)
	}
	if err != nil {
		return p, err
	}
	if err := score(&p, clip, stream); err != nil {
		return p, err
	}
	_ = os.Remove(stream)
	fmt.Fprintf(os.Stderr, "%-12s %-14s q %3d: %8d bytes %6.2f fps  PSNR-Y %.3f  SSIM %.5f  VMAF %.3f\n",
		config, clipName(clip), q, p.Bytes, float64(p.Frames)/p.Seconds, p.PSNRY, p.SSIM, p.VMAF)
	b, _ := json.MarshalIndent(p, "", "\t")
	return p, os.WriteFile(file, b, 0o600)
}

// y4m reads an 8-bit 4:2:0 y4m file's pictures.
type y4m struct {
	r    *bufio.Reader
	w, h int
	buf  []byte
}

func openY4M(f io.Reader) (*y4m, error) {
	r := bufio.NewReaderSize(f, 1<<20)
	hdr, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(hdr, "YUV4MPEG2 ") {
		return nil, errors.New("not a y4m file")
	}
	y := &y4m{r: r}
	for _, f := range strings.Fields(hdr)[1:] {
		switch f[0] {
		case 'W':
			y.w, _ = strconv.Atoi(f[1:])
		case 'H':
			y.h, _ = strconv.Atoi(f[1:])
		case 'C':
			if !strings.HasPrefix(f, "C420") || strings.Contains(f, "p10") {
				return nil, fmt.Errorf("y4m colour space %s: 8-bit 4:2:0 only", f[1:])
			}
		}
	}
	y.buf = make([]byte, y.w*y.h*3/2)
	return y, nil
}

// next gives the next picture's planes (valid until the next call).
func (y *y4m) next() ([]byte, error) {
	if _, err := y.r.ReadString('\n'); err != nil {
		return nil, err
	}
	_, err := io.ReadFull(y.r, y.buf)
	return y.buf, err
}

func encodeOurs(clip, stream string, speed, q int) (point, error) {
	p := point{Q: q}
	f, err := os.Open(clip) //nolint:gosec // the bench's clip
	if err != nil {
		return p, err
	}
	defer func() { _ = f.Close() }()
	y, err := openY4M(f)
	if err != nil {
		return p, err
	}
	w, h := y.w, y.h
	e, err := av1.NewEncoder(av1.EncoderOptions{Width: w, Height: h, BitDepth: 8, QIndex: q,
		FPSNum: 24000, FPSDen: 1001, Primaries: 1, Transfer: 1, Matrix: 1, Speed: speed,
		TileColsLog2: -1, TileRowsLog2: -1})
	if err != nil {
		return p, err
	}
	out, err := os.Create(stream) //nolint:gosec // the results' own file
	if err != nil {
		return p, err
	}
	defer func() { _ = out.Close() }()
	bw := bufio.NewWriter(out)
	write := func(us []av1.Unit) {
		for _, u := range us {
			p.Bytes += len(u.Data)
			_, _ = bw.Write(u.Data)
		}
	}
	cpu0 := cpuSeconds()
	start := time.Now()
	for {
		b, err := y.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return p, err
		}
		src := &av1.Source{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4), StrideY: w, StrideC: w / 2}
		for i := range src.Y {
			src.Y[i] = uint16(b[i])
		}
		for i := range src.Cb {
			src.Cb[i] = uint16(b[w*h+i])
			src.Cr[i] = uint16(b[w*h*5/4+i])
		}
		us, err := e.Encode(src)
		if err != nil {
			return p, err
		}
		write(us)
		p.Frames++
	}
	us, err := e.Flush()
	if err != nil {
		return p, err
	}
	write(us)
	p.Seconds = time.Since(start).Seconds()
	p.CPU = cpuSeconds() - cpu0
	return p, bw.Flush()
}

func encodeSVT(clip, stream string, preset, crf int) (point, error) {
	p := point{Q: crf}
	cmd := exec.CommandContext(context.Background(), "ffmpeg", "-v", "error", "-y", "-i", clip, "-c:v", "libsvtav1", //nolint:gosec // the bench's own arguments
		"-preset", strconv.Itoa(preset), "-crf", strconv.Itoa(crf), "-g", "250", "-f", "obu", stream)
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "SVT_LOG=1") // errors only
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return p, err
	}
	p.Seconds = time.Since(start).Seconds()
	p.CPU = (cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()).Seconds()
	st, err := os.Stat(stream)
	if err != nil {
		return p, err
	}
	p.Bytes = int(st.Size())
	return p, nil
}

// score decodes stream (libdav1d, through ffmpeg) and measures it against
// clip.
func score(p *point, clip, stream string) error {
	f, err := os.Open(clip) //nolint:gosec // the bench's clip
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	src, err := openY4M(f)
	if err != nil {
		return err
	}
	w, h := src.w, src.h
	cmd := exec.CommandContext(context.Background(), "ffmpeg", "-v", "error", "-c:v", "libdav1d", "-i", stream, //nolint:gosec // the bench's own arguments
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-fps_mode", "passthrough", "pipe:1")
	cmd.Stderr = os.Stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	dr := bufio.NewReaderSize(pipe, 1<<20)
	sc, err := vmaf.NewScorer(h)
	if err != nil {
		return err
	}
	dec := make([]byte, w*h*3/2)
	n := 0
	var su, sv float64
	widen := func(b []byte) []uint16 {
		o := make([]uint16, len(b))
		for i, v := range b {
			o[i] = uint16(v)
		}
		return o
	}
	for {
		s, err := src.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if _, err := io.ReadFull(dr, dec); err != nil {
			return fmt.Errorf("decoded %d pictures of the clip's more: %w", n, err)
		}
		ys, cs := w*h, w*h/4
		sc.Add(vmaf.Frame{W: w, H: h, BPC: 8, Y: widen(s[:ys])}, vmaf.Frame{W: w, H: h, BPC: 8, Y: widen(dec[:ys])})
		su += planePSNR(s[ys:ys+cs], dec[ys:ys+cs])
		sv += planePSNR(s[ys+cs:], dec[ys+cs:])
		n++
	}
	if _, err := io.Copy(io.Discard, dr); err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return err
	}
	if n != p.Frames && p.Frames != 0 {
		return fmt.Errorf("decoded %d pictures, coded %d", n, p.Frames)
	}
	p.Frames = n
	var y, ss, vm float64
	for _, s := range sc.Scores() {
		y += s.PSNR
		ss += s.SSIM
		vm += s.VMAF
	}
	fn := float64(n)
	p.PSNRY, p.SSIM, p.VMAF, p.PSNRU, p.PSNRV = y/fn, ss/fn, vm/fn, su/fn, sv/fn
	return nil
}

func planePSNR(a, b []byte) float64 {
	var sse float64
	for i, v := range a {
		d := float64(v) - float64(b[i])
		sse += d * d
	}
	return min(10*math.Log10(255*255/max(sse/float64(len(a)), 1e-16)), 60)
}

// report prints each configuration's BD-rate against the anchor's, per
// metric (the clips' mean; negative is smaller), and its speed.
func report(w io.Writer, all map[string]map[string][]point, configs []string, anchor string, files []string) {
	fmt.Fprintf(w, "BD-rate against %s (mean of %d clips; negative is smaller at equal quality)\n\n", anchor, len(files))
	fmt.Fprintf(w, "%-14s", "config")
	for _, m := range metrics {
		fmt.Fprintf(w, " %9s", m.name)
	}
	fmt.Fprintf(w, " %9s %10s %9s\n", "fps", "cpu-s/pic", "x anchor")
	speed := func(c string) (fps, cpu float64) {
		var frames, secs, cpus float64
		for _, ps := range all[c] {
			for _, p := range ps {
				frames += float64(p.Frames)
				secs += p.Seconds
				cpus += p.CPU
			}
		}
		return frames / secs, cpus / frames
	}
	afps, _ := speed(anchor)
	for _, c := range configs {
		fmt.Fprintf(w, "%-14s", c)
		for _, m := range metrics {
			var sum float64
			n := 0
			for _, f := range files {
				if d, ok := bdRate(all[anchor][clipName(f)], all[c][clipName(f)], m.of); ok {
					sum += d
					n++
				}
			}
			if n == 0 {
				fmt.Fprintf(w, " %9s", "-")
				continue
			}
			fmt.Fprintf(w, " %+8.2f%%", sum/float64(n))
		}
		fps, cpu := speed(c)
		fmt.Fprintf(w, " %9.2f %10.3f %8.2fx\n", fps, cpu, fps/afps)
	}
	fmt.Fprintln(w, "\nper clip, PSNR-YUV:")
	for _, c := range configs {
		fmt.Fprintf(w, "%-14s", c)
		for _, f := range files {
			if d, ok := bdRate(all[anchor][clipName(f)], all[c][clipName(f)], point.psnrYUV); ok {
				fmt.Fprintf(w, " %s %+.2f%%", clipName(f), d)
			}
		}
		fmt.Fprintln(w)
	}
}

// bdRate is the Bjøntegaard rate difference of b against a (percent):
// the log rates as functions of the metric, interpolated by monotone
// piecewise cubics (Fritsch-Carlson), integrated over the metrics' common
// interval.
func bdRate(a, b []point, of func(point) float64) (float64, bool) {
	ca, cb := curve(a, of), curve(b, of)
	if ca == nil || cb == nil {
		return 0, false
	}
	lo := max(ca.x[0], cb.x[0])
	hi := min(ca.x[len(ca.x)-1], cb.x[len(cb.x)-1])
	if hi <= lo {
		return 0, false
	}
	const steps = 1000
	var sa, sb float64
	for i := range steps {
		x := lo + (hi-lo)*(float64(i)+0.5)/steps
		sa += ca.at(x)
		sb += cb.at(x)
	}
	return (math.Exp((sb-sa)/steps) - 1) * 100, true
}

type pchip struct{ x, y, m []float64 }

func curve(ps []point, of func(point) float64) *pchip {
	if len(ps) < 2 {
		return nil
	}
	type xy struct{ x, y float64 }
	v := make([]xy, len(ps))
	for i, p := range ps {
		v[i] = xy{of(p), math.Log(float64(p.Bytes))}
	}
	sort.Slice(v, func(i, j int) bool { return v[i].x < v[j].x })
	c := &pchip{}
	for _, p := range v {
		if n := len(c.x); n > 0 && p.x <= c.x[n-1] {
			continue // the metric must rise with the rate
		}
		c.x, c.y = append(c.x, p.x), append(c.y, p.y)
	}
	n := len(c.x)
	if n < 2 {
		return nil
	}
	d := make([]float64, n-1)
	for i := range d {
		d[i] = (c.y[i+1] - c.y[i]) / (c.x[i+1] - c.x[i])
	}
	c.m = make([]float64, n)
	c.m[0], c.m[n-1] = d[0], d[n-2]
	for i := 1; i < n-1; i++ {
		if d[i-1]*d[i] <= 0 {
			continue
		}
		h0, h1 := c.x[i]-c.x[i-1], c.x[i+1]-c.x[i]
		w0, w1 := 2*h1+h0, h1+2*h0
		c.m[i] = (w0 + w1) / (w0/d[i-1] + w1/d[i])
	}
	return c
}

func (c *pchip) at(x float64) float64 {
	i := sort.SearchFloat64s(c.x, x) - 1
	i = min(max(i, 0), len(c.x)-2)
	h := c.x[i+1] - c.x[i]
	t := (x - c.x[i]) / h
	t2, t3 := t*t, t*t*t
	return (2*t3-3*t2+1)*c.y[i] + (t3-2*t2+t)*h*c.m[i] + (-2*t3+3*t2)*c.y[i+1] + (t3-t2)*h*c.m[i+1]
}
