package av1

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"testing"
)

// testPicture is a picture with gradients, edges, texture and noise.
func testPicture(w, h, depth int, seed uint64) *Source {
	rng := rand.New(rand.NewPCG(seed, 1))
	cw, ch := (w+1)/2, (h+1)/2
	s := &Source{Y: make([]uint16, w*h), Cb: make([]uint16, cw*ch), Cr: make([]uint16, cw*ch), StrideY: w, StrideC: cw}
	maxV := float64(int(1)<<uint(depth) - 1)
	for y := range h {
		for x := range w {
			v := 0.3 + 0.4*float64(x)/float64(w) + 0.15*math.Sin(float64(x*y)/300)
			if (x/23+y/17)%3 == 0 {
				v += 0.2
			}
			v += 0.03 * rng.NormFloat64()
			s.Y[y*w+x] = uint16(min(max(v, 0), 1) * maxV)
		}
	}
	for y := range ch {
		for x := range cw {
			s.Cb[y*cw+x] = uint16((0.5 + 0.2*math.Sin(float64(x)/9)) * maxV)
			s.Cr[y*cw+x] = uint16((0.5 + 0.2*math.Cos(float64(y)/7) + 0.02*rng.NormFloat64()) * maxV)
		}
	}
	return s
}

// checkDecodes encodes pictures and checks libdav1d decodes them to the
// encoder's reconstructions.
func checkDecodes(t *testing.T, o EncoderOptions, pics []*Source) {
	t.Helper()
	checkDecodesWith(t, o, pics, nil)
}

// checkDecodesWith is checkDecodes with the encoder set up by setup (when
// not nil), giving the bytes and the first picture's luma PSNR.
func checkDecodesWith(t *testing.T, o EncoderOptions, pics []*Source, setup func(*Encoder)) (int, float64) {
	t.Helper()
	needDav1d(t)
	e, err := NewEncoder(o)
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(e)
	}
	e.keepRecon = true
	var units [][]byte
	var recons [][3][]uint16
	total := 0
	for _, u := range encodeAll(t, e, pics) {
		units = append(units, u.Data)
		total += len(u.Data)
		recons = append(recons, *u.recon)
	}
	dec := dav1dDecode(t, ivf(o.Width, o.Height, 25, 1, units), o.Width, o.Height, o.BitDepth)
	if len(dec) != len(pics) {
		t.Fatalf("decoded %d pictures of %d", len(dec), len(pics))
	}
	for i, d := range dec {
		off := 0
		for p, plane := range recons[i] {
			pw := o.Width
			if p > 0 {
				pw = (o.Width + 1) / 2
			}
			for j, v := range plane {
				var got uint16
				if o.BitDepth > 8 {
					got = binary.LittleEndian.Uint16(d[2*(off+j):])
				} else {
					got = uint16(d[off+j])
				}
				if got != v {
					t.Fatalf("picture %d plane %d (%d,%d): decoded %d, reconstructed %d", i, p, j%pw, j/pw, got, v)
				}
			}
			off += len(plane)
		}
	}
	src, rec := pics[0], recons[0][0]
	var se float64
	for y := range o.Height {
		for x := range o.Width {
			d := float64(src.Y[y*src.StrideY+x]) - float64(rec[y*o.Width+x])
			se += d * d
		}
	}
	peak := float64(int(1)<<uint(o.BitDepth) - 1)
	psnr := 10 * math.Log10(peak*peak/(se/float64(o.Width*o.Height)))
	t.Logf("%dx%d %d bits q %d: %d bytes, luma PSNR %.2f dB, loop filter %v sharpness %d", o.Width, o.Height, o.BitDepth, o.QIndex, total,
		psnr, e.frame.fp.lfLevel, e.frame.fp.lfSharp)
	return total, psnr
}

func TestEncodeDecodes(t *testing.T) {
	for _, c := range []struct{ w, h, depth, q int }{
		{64, 64, 8, 100}, {200, 136, 8, 60}, {200, 136, 8, 200}, {136, 72, 10, 80},
	} {
		o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: c.depth, QIndex: c.q, FPSNum: 25, FPSDen: 1}
		checkDecodes(t, o, []*Source{testPicture(c.w, c.h, c.depth, 1)})
	}
}

func TestEncodeTiles(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ w, h, cl, rl int }{{200, 136, 1, 1}, {520, 264, 2, 1}, {136, 72, 1, 0}} {
		o := EncoderOptions{Width: c.w, Height: c.h, BitDepth: 8, QIndex: 90, FPSNum: 25, FPSDen: 1, TileColsLog2: c.cl, TileRowsLog2: c.rl}
		checkDecodes(t, o, []*Source{testPicture(c.w, c.h, 8, 2)})
	}
}

// Inter frames (of intra blocks, for now) after a key frame.
func TestEncodeInterFrames(t *testing.T) {
	t.Parallel()
	o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 100, FPSNum: 25, FPSDen: 1}
	p := testPicture(200, 136, 8, 1)
	checkDecodes(t, o, []*Source{p, shifted(p, 3, 2), shifted(p, 5, -1), testPicture(200, 136, 8, 2)})
	o.BitDepth = 10
	p = testPicture(136, 72, 10, 1)
	o.Width, o.Height = 136, 72
	checkDecodes(t, o, []*Source{p, shifted(p, -2, 1), shifted(p, -7, 3)})
}

// shifted is a picture moved by (dx, dy), its edges extended.
func shifted(p *Source, dx, dy int) *Source {
	w, h := p.StrideY, len(p.Y)/p.StrideY
	cw, ch := p.StrideC, len(p.Cb)/p.StrideC
	q := &Source{Y: make([]uint16, len(p.Y)), Cb: make([]uint16, len(p.Cb)), Cr: make([]uint16, len(p.Cr)), StrideY: w, StrideC: cw}
	cl := func(v, n int) int { return min(max(v, 0), n-1) }
	for y := range h {
		for x := range w {
			q.Y[y*w+x] = p.Y[cl(y-dy, h)*w+cl(x-dx, w)]
		}
	}
	for y := range ch {
		for x := range cw {
			q.Cb[y*cw+x] = p.Cb[cl(y-dy/2, ch)*cw+cl(x-dx/2, cw)]
			q.Cr[y*cw+x] = p.Cr[cl(y-dy/2, ch)*cw+cl(x-dx/2, cw)]
		}
	}
	return q
}

// encodeAll codes pictures, the encoder flushed at the end.
func encodeAll(t *testing.T, e *Encoder, pics []*Source) []Unit {
	t.Helper()
	var out []Unit
	for _, p := range pics {
		u, err := e.Encode(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, u...)
	}
	u, err := e.Flush()
	if err != nil {
		t.Fatal(err)
	}
	return append(out, u...)
}

// A longer run: mini-GOPs cut by key frames, the last one short, the
// slots reused.
func TestEncodeGOPs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ mini, keyint, n int }{{8, 12, 20}, {4, 0, 11}, {1, 5, 7}, {3, 0, 10}} {
		o := EncoderOptions{Width: 96, Height: 64, BitDepth: 8, QIndex: 120, FPSNum: 25, FPSDen: 1, MiniGOP: c.mini, Keyint: c.keyint}
		p := testPicture(96, 64, 8, 3)
		var pics []*Source
		for i := range c.n {
			pics = append(pics, shifted(p, i, i/3))
		}
		checkDecodes(t, o, pics)
	}
}

// The tiles coded at once (for the race detector, where there is no
// decoder to check the stream with): a picture with four tiles and an
// inter frame.
func TestEncodeTilesConcurrently(t *testing.T) {
	o := EncoderOptions{Width: 136, Height: 136, BitDepth: 8, QIndex: 150, TileColsLog2: 1, TileRowsLog2: 1, MiniGOP: 2}
	e, err := NewEncoder(o)
	if err != nil {
		t.Fatal(err)
	}
	p := testPicture(136, 136, 8, 4)
	if units := encodeAll(t, e, []*Source{p, shifted(p, 2, 1), shifted(p, 4, 2)}); len(units) != 3 {
		t.Fatalf("%d units", len(units))
	}
}

// Frames coded at once make the stream one at a time makes.
func TestEncodeFramesConcurrently(t *testing.T) {
	o := EncoderOptions{Width: 96, Height: 64, BitDepth: 8, QIndex: 120, MiniGOP: 8}
	p := testPicture(96, 64, 8, 3)
	var pics []*Source
	for i := range 12 {
		pics = append(pics, shifted(p, i, i/3))
	}
	code := func(serial bool) []byte {
		dbgSerialFrames = serial
		defer func() { dbgSerialFrames = false }()
		e, err := NewEncoder(o)
		if err != nil {
			t.Fatal(err)
		}
		var b []byte
		for _, u := range encodeAll(t, e, pics) {
			b = append(b, u.Data...)
		}
		return b
	}
	if a, b := code(false), code(true); !bytes.Equal(a, b) {
		t.Fatalf("at once %d bytes, one at a time %d: different", len(a), len(b))
	}
}

// The superblock rows coded at once make the same stream however they are
// scheduled.
func TestEncodeRowsDeterministic(t *testing.T) {
	o := EncoderOptions{Width: 264, Height: 200, BitDepth: 8, QIndex: 120, MiniGOP: 4}
	p := testPicture(264, 200, 8, 3)
	pics := []*Source{p, shifted(p, 2, 1), shifted(p, 4, 2), shifted(p, 6, 3), shifted(p, 8, 4)}
	if raceOn {
		pics = pics[:3] // a key frame, an anchor and a frame between
	}
	code := func(procs int) []byte {
		defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(procs))
		e, err := NewEncoder(o)
		if err != nil {
			t.Fatal(err)
		}
		var b []byte
		for _, u := range encodeAll(t, e, pics) {
			b = append(b, u.Data...)
		}
		return b
	}
	if a, b := code(1), code(8); !bytes.Equal(a, b) {
		t.Fatalf("one CPU %d bytes, eight %d: different", len(a), len(b))
	}
}

// raceCut is s, or its first element with the race detector in.
func raceCut[T any](s []T) []T {
	if raceOn {
		return s[:1]
	}
	return s
}

// Inter transform trees decided with DCT_DCT and their types searched
// after (off by default): decoded to the reconstruction.
func TestPruneTxTree(t *testing.T) {
	defer func(was bool) { pruneTxTree = was }(pruneTxTree)
	pruneTxTree = true
	o := EncoderOptions{Width: 136, Height: 96, BitDepth: 8, QIndex: 90, FPSNum: 25, FPSDen: 1, MiniGOP: 4}
	p := testPicture(136, 96, 8, 2)
	var pics []*Source
	for i := range 5 {
		pics = append(pics, shifted(p, 2*i, i))
	}
	checkDecodes(t, o, pics)
}

// The faster settings code smaller searches, decoded to the
// reconstruction, the more so the faster.
func TestSpeeds(t *testing.T) {
	t.Parallel()
	p := testPicture(136, 96, 8, 2)
	var pics []*Source
	for i := range 9 {
		pics = append(pics, shifted(p, 2*i, i))
	}
	for speed := -2; speed <= 2; speed++ {
		if raceOn && speed < 0 {
			continue // the slow ones
		}
		t.Run(fmt.Sprint(speed), func(t *testing.T) {
			o := EncoderOptions{Width: 136, Height: 96, BitDepth: 8, QIndex: 120, FPSNum: 25, FPSDen: 1, MiniGOP: 8, Speed: speed}
			checkDecodes(t, o, pics)
		})
	}
}
