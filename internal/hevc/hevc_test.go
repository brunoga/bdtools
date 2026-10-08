package hevc

import (
	"bytes"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The partial butterfly gives the matrix product of 8.6.4.2, the DST too.
func TestInverseTransform(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for log2 := 2; log2 <= 5; log2++ {
		n := 1 << log2
		for _, dst := range []bool{false, true} {
			if dst && n != 4 {
				continue
			}
			for trial := range 200 {
				c := make([]int32, n*n)
				// Sparse blocks as well as full ones, as the zero skipping
				// takes them.
				for i := range c {
					if trial%2 == 0 || rng.IntN(8) == 0 {
						c[i] = int32(rng.IntN(65536) - 32768)
					}
				}
				want := referenceTransform(c, log2, 10, dst)
				got := append([]int32(nil), c...)
				inverseTransform(got, log2, 10, dst)
				for i := range got {
					if got[i] != want[i] {
						t.Fatalf("%dx%d dst %v, trial %d: sample %d is %d, want %d", n, n, dst, trial, i, got[i], want[i])
					}
				}
			}
		}
	}
}

// referenceTransform is 8.6.4.2 as written.
func referenceTransform(c []int32, log2, depth int, dst bool) []int32 {
	n := 1 << log2
	coef := func(k, j int) int64 {
		if dst {
			return int64(dstMatrix[k][j])
		}
		return int64(transMatrix[k<<(5-log2)][j])
	}
	tmp := make([]int64, n*n)
	for x := range n {
		for y := range n {
			var s int64
			for k := range n {
				s += coef(k, y) * int64(c[k*n+x])
			}
			tmp[y*n+x] = min(max((s+64)>>7, -32768), 32767)
		}
	}
	bd := 20 - depth
	out := make([]int32, n*n)
	for y := range n {
		for x := range n {
			var s int64
			for k := range n {
				s += coef(k, x) * tmp[y*n+k]
			}
			out[y*n+x] = int32((s + 1<<(bd-1)) >> bd)
		}
	}
	return out
}

// bitWriter writes the codes the reader reads back.
type bitWriter struct {
	b []byte
	n int
}

func (w *bitWriter) put(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>i&1 != 0 {
			w.b[w.n/8] |= 0x80 >> (w.n % 8)
		}
		w.n++
	}
}

func (w *bitWriter) ue(v int) {
	x := uint64(v) + 1
	l := 0
	for x>>l > 1 {
		l++
	}
	w.put(0, l)
	w.put(x, l+1)
}

func TestExpGolomb(t *testing.T) {
	var w bitWriter
	vals := []int{0, 1, 2, 3, 7, 8, 254, 255, 1 << 20, 1<<31 - 2}
	for _, v := range vals {
		w.ue(v)
		w.put(1, 1)
	}
	signed := []int{0, 1, -1, 2, -2, 1000, -1000}
	for _, v := range signed {
		k := 2*v - 1
		if v <= 0 {
			k = -2 * v
		}
		w.ue(k)
	}
	w.put(0x2a, 7)
	r := &bits{b: w.b}
	for _, v := range vals {
		if got := r.ue(); got != v {
			t.Errorf("ue %d read %d", v, got)
		}
		if !r.flag() {
			t.Errorf("after %d: the marker", v)
		}
	}
	for _, v := range signed {
		if got := r.se(); got != v {
			t.Errorf("se %d read %d", v, got)
		}
	}
	if got := r.u(7); got != 0x2a {
		t.Errorf("u(7) = %#x", got)
	}
}

// Emulation prevention bytes go, and offsets map across them both ways,
// as entry points count them.
func TestUnescape(t *testing.T) {
	au := []byte{0, 0, 0, 1, 0x40, 0x01, 0xaa, 0, 0, 3, 1, 0, 0, 3, 0, 0xbb, 0, 0, 1, 0x42, 0x01, 0xcc}
	ns := nalUnits(au)
	if len(ns) != 2 || ns[0].typ != nalVPS || ns[1].typ != nalSPS {
		t.Fatalf("NAL units %+v", ns)
	}
	n := ns[0]
	if want := []byte{0xaa, 0, 0, 1, 0, 0, 0, 0xbb}; !bytes.Equal(n.rbsp, want) {
		t.Errorf("RBSP % x, want % x", n.rbsp, want)
	}
	if want := []int{3, 7}; len(n.ep) != 2 || n.ep[0] != want[0] || n.ep[1] != want[1] {
		t.Errorf("emulation prevention at %v, want %v", n.ep, want)
	}
	// Payload offset 4 (the 1) is RBSP offset 3; 9 (0xbb) is 7.
	for _, c := range []struct{ raw, off int }{{0, 0}, {3, 3}, {4, 3}, {8, 6}, {9, 7}} {
		if got := n.unescaped(c.raw); got != c.off {
			t.Errorf("unescaped(%d) = %d, want %d", c.raw, got, c.off)
		}
	}
	for _, c := range []struct{ off, raw int }{{0, 0}, {3, 4}, {6, 8}, {7, 9}} {
		if got := n.escaped(c.off); got != c.raw {
			t.Errorf("escaped(%d) = %d, want %d", c.off, got, c.raw)
		}
	}
}

// Streams x265 makes, with the tools it has, decode as ffmpeg decodes
// them: one picture at a time, and with the rows of a wavefront picture
// in parallel. Skips without ffmpeg and its libx265.
func TestDecodeMatchesFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	for _, c := range []struct {
		name, pixfmt, params string
	}{
		{"8bit", "yuv420p", "wpp=0:bframes=4:b-pyramid=1:ref=4"},
		{"10bit wpp", "yuv420p10le", "wpp=1:bframes=3:weightb=1:weightp=1:amp=1:rect=1"},
		{"wpp slices", "yuv420p", "wpp=1:slices=3:ctu=32:sao=1:tu-intra-depth=3:tu-inter-depth=3"},
		{"lossless", "yuv420p", "lossless=1:wpp=1"},
		{"constrained intra", "yuv420p10le", "constrained-intra=1:ctu=16:min-cu-size=8:no-sao=1:keyint=8:open-gop=1"},
		{"cu qp", "yuv420p", "aq-mode=3:aq-strength=2:qg-size=16:cbqpoffs=-3:crqpoffs=4:signhide=1:rdoq-level=2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.name+".hevc")
			out, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=200x136:r=25:d=1", //nolint:gosec // test
				"-c:v", "libx265", "-pix_fmt", c.pixfmt, "-x265-params", "log-level=error:"+c.params, "-f", "hevc", path).CombinedOutput()
			if err != nil {
				t.Skipf("making the stream (no libx265?): %v %s", err, out)
			}
			for _, threads := range []int{1, 4} {
				compare(t, path, threads)
			}
		})
	}
}

// BenchmarkDecode decodes the stream in $BDTOOLS_HEVC_BENCH (Annex B).
func BenchmarkDecode(b *testing.B) {
	path := os.Getenv("BDTOOLS_HEVC_BENCH")
	if path == "" {
		b.Skip("BDTOOLS_HEVC_BENCH not set")
	}
	data, err := os.ReadFile(path) //nolint:gosec // test
	if err != nil {
		b.Fatal(err)
	}
	n := 0
	count := func(*Picture) error { n++; return nil }
	start := time.Now()
	for b.Loop() {
		d := New()
		if err := d.Decode(data, 0, count); err != nil {
			b.Fatal(err)
		}
		if err := d.Flush(count); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(n)/time.Since(start).Seconds(), "pictures/s")
}
