package av1

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestBenchIntra codes the 1080p 8-bit pictures of $AV1BENCH (f*.yuv) as
// key frames at the q indexes of $AV1BENCH_Q (default 60,100,140,180),
// printing "name q bytes psnr-y seconds" lines for a BD-rate.
func TestBenchIntra(t *testing.T) {
	dir := os.Getenv("AV1BENCH")
	if dir == "" {
		t.Skip("AV1BENCH unset")
	}
	qs := []int{60, 100, 140, 180}
	if s := os.Getenv("AV1BENCH_Q"); s != "" {
		qs = nil
		for _, f := range strings.Split(s, ",") {
			q, _ := strconv.Atoi(f)
			qs = append(qs, q)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "f*.yuv"))
	const w, h = 1920, 1080
	type job struct {
		file string
		q    int
	}
	jobs := make(chan job)
	var mu sync.Mutex
	var wg sync.WaitGroup
	// half the CPUs: each picture's tiles are coded at once too
	for range max(runtime.GOMAXPROCS(0)/2, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for jb := range jobs {
				line := benchOne(t, jb.file, jb.q, w, h)
				mu.Lock()
				fmt.Println(line)
				mu.Unlock()
			}
		}()
	}
	for _, file := range files {
		for _, q := range qs {
			jobs <- job{file, q}
		}
	}
	close(jobs)
	wg.Wait()
}

func benchOne(t *testing.T, file string, q, w, h int) string {
	b, err := os.ReadFile(file) //nolint:gosec // a test
	if err != nil {
		t.Error(err)
		return ""
	}
	src := &Source{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4), StrideY: w, StrideC: w / 2}
	for i := range src.Y {
		src.Y[i] = uint16(b[i])
	}
	for i := range src.Cb {
		src.Cb[i] = uint16(b[w*h+i])
		src.Cr[i] = uint16(b[w*h*5/4+i])
	}
	e, err := NewEncoder(EncoderOptions{Width: w, Height: h, BitDepth: 8, QIndex: q})
	if err != nil {
		t.Error(err)
		return ""
	}
	start := time.Now()
	e.keepRecon = true
	units := encodeAll(t, e, []*Source{src})
	el := time.Since(start)
	u := units[0].Data
	y := units[0].recon[0]
	var se float64
	for i, v := range y {
		d := float64(v) - float64(src.Y[i])
		se += d * d
	}
	return fmt.Sprintf("%s %d %d %.4f %.2f", filepath.Base(file), q, len(u), 10*math.Log10(255*255/(se/float64(w*h))), el.Seconds())
}

// TestBenchSeq codes the 1080p 8-bit clips $AV1SEQ (seq-*.yuv) at the q
// indexes of $AV1BENCH_Q, one clip and q at a time (its tiles at once),
// printing "name q bytes mean-psnr-y seconds" lines.
func TestBenchSeq(t *testing.T) {
	dir := os.Getenv("AV1SEQ")
	if dir == "" {
		t.Skip("AV1SEQ unset")
	}
	qs := []int{60, 100, 140, 180}
	if s := os.Getenv("AV1BENCH_Q"); s != "" {
		qs = nil
		for _, f := range strings.Split(s, ",") {
			q, _ := strconv.Atoi(f)
			qs = append(qs, q)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "seq-*.yuv"))
	const w, h = 1920, 1080
	// $AV1DEPTHQ overrides the q offsets by depth (comma separated)
	if s := os.Getenv("AV1DEPTHQ"); s != "" {
		for i, f := range strings.Split(s, ",") {
			depthQ[i], _ = strconv.Atoi(f)
		}
	}
	mini, _ := strconv.Atoi(os.Getenv("AV1MINIGOP"))
	for env, v := range map[string]*int{"AV1INTERCANDS": &interCands, "AV1LUMACANDS": &lumaCands, "AV1CHROMACANDS": &chromaCands} {
		if n, err := strconv.Atoi(os.Getenv(env)); err == nil && n > 0 {
			old := *v
			*v = n
			defer func() { *v = old }()
		}
	}
	for env, v := range map[string]*bool{"AV1NOPRUNESKIP": &dbgNoPruneSkip, "AV1NOPRUNEEXT": &dbgNoPruneExt, "AV1NOPRUNEINTRA": &dbgNoPruneIntra} {
		if os.Getenv(env) != "" {
			*v = true
			defer func() { *v = false }()
		}
	}
	if v, err := strconv.ParseFloat(os.Getenv("AV1LAMBDA"), 64); err == nil {
		old := lambdaScale
		lambdaScale = v
		defer func() { lambdaScale = old }()
	}
	if v, err := strconv.Atoi(os.Getenv("AV1KEYQ")); err == nil {
		old := keyQ
		keyQ = v
		defer func() { keyQ = old }()
	}
	if os.Getenv("AV1NOFILTERRD") != "" {
		filterRD = false
		defer func() { filterRD = true }()
	}
	for env, v := range map[string]*bool{"AV1NOWARP": &dbgNoWarp, "AV1NOMASKED": &dbgNoMasked, "AV1FEWREFS": &dbgFewRefs} {
		if os.Getenv(env) != "" {
			*v = true
			defer func() { *v = false }()
		}
	}
	if os.Getenv("AV1NOOBMC") != "" {
		dbgNoObmc = true
		defer func() { dbgNoObmc = false }()
	}
	if os.Getenv("AV1NOSWITCHABLE") != "" {
		dbgNoSwitchable = true
		defer func() { dbgNoSwitchable = false }()
	}
	if os.Getenv("AV1NOPRUNE") != "" {
		dbgNoPrune = true
		defer func() { dbgNoPrune = false }()
	}
	if v, err := strconv.Atoi(os.Getenv("AV1REFSPREVDEPTH")); err == nil {
		old := refsPrevDepth
		refsPrevDepth = v
		defer func() { refsPrevDepth = old }()
	}
	for env, v := range map[string]*bool{"AV1PRUNETXTREE": &pruneTxTree, "AV1NOPRUNETXZERO": &pruneTxZero} {
		if e := os.Getenv(env); e != "" {
			old := *v
			*v = e == "1" && env == "AV1PRUNETXTREE"
			defer func() { *v = old }()
		}
	}
	if v, err := strconv.ParseFloat(os.Getenv("AV1TPLPOW"), 64); err == nil {
		old := tplLambdaPow
		tplLambdaPow = v
		defer func() { tplLambdaPow = old }()
	}
	if v, err := strconv.ParseFloat(os.Getenv("AV1TPLFRAMEK"), 64); err == nil {
		old := tplFrameK
		tplFrameK = v
		defer func() { tplFrameK = old }()
	}
	if os.Getenv("AV1TPLQ") != "" {
		tplLambda = false
		defer func() { tplLambda = true }()
	}
	if os.Getenv("AV1NOTPL") != "" {
		dbgNoTPL = true
		defer func() { dbgNoTPL = false }()
	}
	if os.Getenv("AV1NOOVERLAY") != "" {
		dbgNoOverlay = true
		defer func() { dbgNoOverlay = false }()
	}
	if v, err := strconv.Atoi(os.Getenv("AV1TFAFTER")); err == nil {
		old := tfAfter
		tfAfter = v
		defer func() { tfAfter = old }()
	}
	if v, err := strconv.Atoi(os.Getenv("AV1TXCANDS")); err == nil {
		old := txCands
		txCands = v
		defer func() { txCands = old }()
	}
	if v, err := strconv.Atoi(os.Getenv("AV1SAMEDIR")); err == nil {
		old := sameDirComp
		sameDirComp = v
		defer func() { sameDirComp = old }()
	}
	if v, err := strconv.ParseFloat(os.Getenv("AV1CANDRATIO"), 64); err == nil {
		old := candRatio
		candRatio = v
		defer func() { candRatio = old }()
	}
	if v, err := strconv.ParseFloat(os.Getenv("AV1INTRASKIP"), 64); err == nil {
		old := intraSkip
		intraSkip = v
		defer func() { intraSkip = old }()
	}
	if os.Getenv("AV1TF") != "" {
		dbgNoTF = false
		defer func() { dbgNoTF = true }()
	}
	speed, _ := strconv.Atoi(os.Getenv("AV1SPEED"))
	for _, file := range files {
		b, err := os.ReadFile(file) //nolint:gosec // a test
		if err != nil {
			t.Fatal(err)
		}
		n := len(b) / (w * h * 3 / 2)
		for _, q := range qs {
			e, err := NewEncoder(EncoderOptions{Width: w, Height: h, BitDepth: 8, QIndex: q, TileColsLog2: -1, TileRowsLog2: -1, MiniGOP: mini, Speed: speed})
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			total, seAll := 0, 0.0
			e.keepRecon = true
			var pics []*Source
			for k := range n {
				p := b[k*w*h*3/2:]
				src := &Source{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4), StrideY: w, StrideC: w / 2}
				for i := range src.Y {
					src.Y[i] = uint16(p[i])
				}
				for i := range src.Cb {
					src.Cb[i], src.Cr[i] = uint16(p[w*h+i]), uint16(p[w*h*5/4+i])
				}
				pics = append(pics, src)
			}
			for k, u := range encodeAll(t, e, pics) {
				total += len(u.Data)
				var se float64
				for i, v := range u.recon[0] {
					d := float64(v) - float64(pics[k].Y[i])
					se += d * d
				}
				seAll += se
			}
			// PSNR from the clip's mean squared error, as ffmpeg's psnr filter
			psnr := 10 * math.Log10(255*255/(seAll/float64(n*w*h)))
			fmt.Printf("%s %d %d %.4f %.2f\n", strings.TrimSuffix(filepath.Base(file), ".yuv"), q, total, psnr, time.Since(start).Seconds())
		}
	}
}
