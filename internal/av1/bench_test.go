package av1

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	for _, file := range files {
		b, err := os.ReadFile(file) //nolint:gosec // a test
		if err != nil {
			t.Fatal(err)
		}
		src := &Source{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4), StrideY: w, StrideC: w / 2}
		for i := range src.Y {
			src.Y[i] = uint16(b[i])
		}
		for i := range src.Cb {
			src.Cb[i] = uint16(b[w*h+i])
			src.Cr[i] = uint16(b[w*h*5/4+i])
		}
		for _, q := range qs {
			e, err := NewEncoder(EncoderOptions{Width: w, Height: h, BitDepth: 8, QIndex: q})
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			u, err := e.Encode(src)
			if err != nil {
				t.Fatal(err)
			}
			el := time.Since(start)
			y, _, _ := e.Recon()
			var se float64
			for i, v := range y {
				d := float64(v) - float64(src.Y[i])
				se += d * d
			}
			fmt.Printf("%s %d %d %.4f %.2f\n", filepath.Base(file), q, len(u), 10*math.Log10(255*255/(se/float64(w*h))), el.Seconds())
		}
	}
}
