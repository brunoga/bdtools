package hevc

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// synthSource makes a test picture: gradients, an edge, a disc and noise,
// moving with the frame number.
func synthSource(w, h, depth, frame int) *Source {
	s := &Source{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4), StrideY: w, StrideC: w / 2}
	scale := 1 << (depth - 8)
	seed := uint32(12345 + frame)
	rnd := func() int {
		seed = seed*1664525 + 1013904223
		return int(seed>>24) & 15
	}
	cx, cy := w/2+frame*3, h/2+frame*2
	for y := range h {
		for x := range w {
			v := (x*2 + y + frame*4) & 255
			if x > w/2 {
				v = 255 - v
			}
			if dx, dy := x-cx, y-cy; dx*dx+dy*dy < 900 {
				v = 200 - (dx*dx+dy*dy)/8
			}
			v = min(max(v*scale+rnd()*scale/4, 0), 255*scale)
			s.Y[y*w+x] = uint16(v) //nolint:gosec // in range
		}
	}
	for y := range h / 2 {
		for x := range w / 2 {
			s.Cb[y*w/2+x] = uint16(min(max((128+(x-y+frame)/2)*scale, 0), 255*scale)) //nolint:gosec // in range
			s.Cr[y*w/2+x] = uint16(min(max((128-(x/3)+(y/5))*scale, 0), 255*scale))   //nolint:gosec // in range
		}
	}
	return s
}

// psnr of two planes.
func psnr(a, b []uint16, w, h, stride, depth int) float64 {
	var sse float64
	for y := range h {
		for x := range w {
			d := float64(a[y*stride+x]) - float64(b[y*stride+x])
			sse += d * d
		}
	}
	if sse == 0 {
		return 99
	}
	maxV := float64(int(1)<<depth - 1)
	return 10 * math.Log10(maxV*maxV*float64(w*h)/sse)
}

// encodeSynth codes frames synthetic pictures with the options, returning
// the stream, the encoder's reconstructions and the sources.
func encodeSynth(t *testing.T, o EncoderOptions, frames int) ([]byte, [][3][]uint16, []*Source) {
	t.Helper()
	w, h := o.Width, o.Height
	e, err := NewEncoder(o)
	if err != nil {
		t.Fatal(err)
	}
	recons := make([][3][]uint16, frames)
	e.reconHook = func(pic *picture, frame int) {
		var r [3][]uint16
		r[0] = make([]uint16, w*h)
		for y := range h {
			copy(r[0][y*w:y*w+w], pic.y[y*pic.strideY:])
		}
		for ci, pl := range [2][]uint16{pic.cb, pic.cr} {
			r[ci+1] = make([]uint16, w*h/4)
			for y := range h / 2 {
				copy(r[ci+1][y*w/2:y*w/2+w/2], pl[y*pic.strideC:])
			}
		}
		recons[frame] = r
	}
	var stream []byte
	var srcs []*Source
	for i := range frames {
		src := synthSource(w, h, o.BitDepth, i)
		srcs = append(srcs, src)
		aus, err := e.Encode(src)
		if err != nil {
			t.Fatalf("picture %d: %v", i, err)
		}
		for _, au := range aus {
			stream = append(stream, au...)
		}
	}
	aus, err := e.Flush()
	if err != nil {
		t.Fatal(err)
	}
	for _, au := range aus {
		stream = append(stream, au...)
	}
	if n := e.dec.Errors(); n != 0 {
		t.Fatalf("the encoder's decoder reported %d errors", n)
	}
	return stream, recons, srcs
}

// The encoder's output decodes, with the package decoder, to the
// encoder's own reconstruction, and ffmpeg agrees: intra only, and with
// P pictures.
func TestEncoderRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		depth   int
		keyint  int
		bframes int
		frames  int
	}{{"8bit-intra", 8, 1, 0, 4}, {"10bit-intra", 10, 1, 0, 4}, {"8bit-P", 8, 0, 0, 4}, {"10bit-P", 10, 0, 0, 4},
		{"8bit-B", 8, 0, 3, 7}, {"10bit-B", 10, 6, 3, 9},
		// Partial groups at keyframes and at the end, in deep hierarchies.
		{"8bit-B7-tail", 8, 11, 7, 21}, {"8bit-B1-tail", 8, 5, 1, 12}, {"10bit-cropped", 10, 0, 3, 6}} {
		t.Run(tc.name, func(t *testing.T) {
			w, h := 200, 136
			if tc.name == "10bit-cropped" {
				w, h = 202, 130 // not multiples of 8: a conformance window
			}
			depth, frames := tc.depth, tc.frames
			o := EncoderOptions{Width: w, Height: h, BitDepth: depth, QP: 27, FPSNum: 25, FPSDen: 1, Keyint: tc.keyint, BFrames: tc.bframes}
			// With the loop filter off everywhere (the encoder's decoder
			// included) the decoded pictures are the reconstructions.
			noLoopFilter = true
			defer func() { noLoopFilter = false }()
			stream, recons, srcs := encodeSynth(t, o, frames)
			t.Logf("%d pictures: %d bytes, luma PSNR of the reconstructions %.2f dB", frames, len(stream),
				psnr(recons[frames-1][0], srcs[frames-1].Y, w, h, w, depth))

			d := New()
			d.SetThreads(1)
			n := 0
			out := func(p *Picture) error {
				if n >= len(recons) {
					t.Errorf("picture %d: more than coded", n)
					return nil
				}
				r := recons[n]
				check := func(name string, got []uint16, stride int, want []uint16, pw, ph int) {
					for y := range ph {
						for x := range pw {
							if got[y*stride+x] != want[y*pw+x] {
								t.Errorf("picture %d %s differs at (%d,%d): decoded %d, reconstructed %d", n, name, x, y, got[y*stride+x], want[y*pw+x])
								return
							}
						}
					}
				}
				check("Y", p.Y, p.StrideY, r[0], w, h)
				check("Cb", p.Cb, p.StrideC, r[1], w/2, h/2)
				check("Cr", p.Cr, p.StrideC, r[2], w/2, h/2)
				n++
				return nil
			}
			if err := d.Decode(stream, 0, out); err != nil {
				t.Fatal(err)
			}
			if err := d.Flush(out); err != nil {
				t.Fatal(err)
			}
			if n != frames || d.Errors() != 0 {
				t.Fatalf("decoded %d pictures with %d errors", n, d.Errors())
			}
			if t.Failed() {
				return
			}
			path := filepath.Join(t.TempDir(), "enc.hevc")
			if err := os.WriteFile(path, stream, 0o644); err != nil {
				t.Fatal(err)
			}
			compare(t, path, 1) // against ffmpeg, no loop filter
			// Coded again with the deblocking on, the decoders agree too.
			noLoopFilter = false
			stream, _, _ = encodeSynth(t, o, frames)
			if err := os.WriteFile(path, stream, 0o644); err != nil {
				t.Fatal(err)
			}
			compare(t, path, 4)
		})
	}
}

// The rows coded in parallel give the same stream as one at a time.
func TestEncoderThreadsDeterministic(t *testing.T) {
	const w, h, frames = 200, 136, 6
	var streams [3][]byte
	for i, tp := range [][2]int{{1, 1}, {4, 1}, {4, 3}} {
		o := EncoderOptions{Width: w, Height: h, BitDepth: 8, QP: 30, FPSNum: 25, FPSDen: 1, BFrames: 3, Threads: tp[0], Pipeline: tp[1]}
		streams[i], _, _ = encodeSynth(t, o, frames)
	}
	for i := 1; i < len(streams); i++ {
		if !bytes.Equal(streams[0], streams[i]) {
			t.Errorf("stream %d differs: %d and %d bytes", i, len(streams[0]), len(streams[i]))
		}
	}
}

// The assembly kernels agree with the Go ones.
func TestEncKernels(t *testing.T) {
	seed := uint32(7)
	rnd := func() uint16 {
		seed = seed*1664525 + 1013904223
		return uint16(seed >> 16)
	}
	for _, depth := range []int{8, 10} {
		maxV := uint16(1<<depth - 1)
		a := make([]uint16, 80*80)
		b := make([]uint16, 80*80)
		for i := range a {
			a[i], b[i] = rnd()&maxV, rnd()&maxV
		}
		for _, w := range []int{4, 8, 16, 24, 32, 64} {
			for _, h := range []int{4, 8, 16, 32, 64} {
				if w >= 8 {
					if got, want := sadBlock(a[3:], 80, b[5:], 80, w, h), sadBlockGo(a[3:], 80, b[5:], 80, w, h); got != want {
						t.Errorf("%d-bit sad %dx%d: %d, want %d", depth, w, h, got, want)
					}
				}
				if got, want := ssdBlock(a[3:], 80, b[5:], 80, w, h), ssdBlockGo(a[3:], 80, b[5:], 80, w, h); got != want {
					t.Errorf("%d-bit ssd %dx%d: %d, want %d", depth, w, h, got, want)
				}
				if w >= 8 {
					d0, d1 := make([]uint16, 64*64), make([]uint16, 64*64)
					avgBlock(d0, 64, a[3:], 80, b[5:], 80, w, h)
					avgBlockGo(d1, 64, a[3:], 80, b[5:], 80, w, h)
					for i := range d0 {
						if d0[i] != d1[i] {
							t.Fatalf("%d-bit avg %dx%d differs at %d", depth, w, h, i)
						}
					}
				}
			}
		}
		for i := range 20 {
			if got, want := satd8(a[i*3:], 80, b[i*5:], 80), satd8Go(a[i*3:], 80, b[i*5:], 80); got != want {
				t.Errorf("%d-bit satd8 %d: %d, want %d", depth, i, got, want)
			}
			want := satd8Go(a[i*3:], 80, b[i*5:], 80) + satd8Go(a[i*3+8:], 80, b[i*5+8:], 80)
			if got := satd16x8(a[i*3:], 80, b[i*5:], 80); got != want {
				t.Errorf("%d-bit satd16x8 %d: %d, want %d", depth, i, got, want)
			}
		}
		// The extremes: the largest differences of the bit depth.
		for i := range a {
			a[i], b[i] = maxV*uint16(i&1), maxV*uint16(1-i&1)
		}
		if got, want := satd16x8(a, 80, b, 80), satd8Go(a, 80, b, 80)+satd8Go(a[8:], 80, b[8:], 80); got != want {
			t.Errorf("%d-bit satd16x8 extremes: %d, want %d", depth, got, want)
		}
		for i := range a {
			a[i], b[i] = maxV, 0
		}
		if got, want := satd16x8(a, 80, b, 80), satd8Go(a, 80, b, 80)+satd8Go(a[8:], 80, b[8:], 80); got != want {
			t.Errorf("%d-bit satd16x8 flat extremes: %d, want %d", depth, got, want)
		}
	}
}

// The forward transform is the matrix product.
func TestForwardTransformMatchesMatrix(t *testing.T) {
	seed := uint32(3)
	for log2 := 2; log2 <= 5; log2++ {
		n := 1 << log2
		in := make([]int32, n*n)
		for i := range in {
			seed = seed*1664525 + 1013904223
			in[i] = int32(seed>>20) - 2048
		}
		for _, dst := range []bool{false, log2 == 2} {
			got := make([]int32, n*n)
			forwardTransform(in, got, log2, 8, dst)
			// Rows then columns by the matrix, with the same rounding.
			shift1, shift2 := uint(log2+8-9), uint(log2+6)
			coef := func(k, j int) int32 {
				if dst {
					return int32(dstMatrix[k][j])
				}
				return int32(transMatrix[k*(32/n)][j])
			}
			tmp := make([]int32, n*n)
			for y := range n {
				for k := range n {
					var sum int32
					for j := range n {
						sum += coef(k, j) * in[y*n+j]
					}
					tmp[y*n+k] = min(max((sum+1<<(shift1-1))>>shift1, -32768), 32767)
				}
			}
			for x := range n {
				for k := range n {
					var sum int32
					for j := range n {
						sum += coef(k, j) * tmp[j*n+x]
					}
					if want := (sum + 1<<(shift2-1)) >> shift2; got[k*n+x] != want {
						t.Fatalf("size %d dst %v: coefficient (%d,%d) = %d, want %d", n, dst, x, k, got[k*n+x], want)
					}
				}
			}
		}
	}
}

// The fast forward transform is the Go one.
func TestForwardTransformFast(t *testing.T) {
	var scratch transformScratch
	seed := uint32(5)
	for _, depth := range []int{8, 10} {
		for log2 := 3; log2 <= 5; log2++ {
			n := 1 << log2
			for trial := range 20 {
				r := make([]int16, n*n)
				r32 := make([]int32, n*n)
				for i := range r {
					seed = seed*1664525 + 1013904223
					v := int16(int32(seed>>16)%(2<<depth) - 1<<depth) //nolint:gosec // in range
					if trial == 0 {
						v = int16(1<<depth - 1) // the extreme
						if i&1 == 1 {
							v = -v
						}
					}
					r[i], r32[i] = v, int32(v)
				}
				want := make([]int32, n*n)
				forwardTransform(r32, want, log2, depth, false)
				got := make([]int32, n*n)
				if !forwardTransformFast(r, got, log2, depth, &scratch) {
					t.Skip("no fast transform here")
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("%d-bit %dx%d trial %d: coefficient %d = %d, want %d", depth, n, n, trial, i, got[i], want[i])
					}
				}
			}
		}
	}
}

// The fast quantiser is the Go one.
// The fast inverse transform is the Go one, bit for bit.
func TestInverseTransformFast(t *testing.T) {
	seed := uint32(11)
	for _, depth := range []int{8, 10} {
		for log2 := 3; log2 <= 5; log2++ {
			n := 1 << log2
			for trial := range 40 {
				a := make([]int32, n*n)
				for i := range a {
					seed = seed*1664525 + 1013904223
					switch {
					case trial == 0:
						a[i] = 32767 * int32(1-2*(i&1)) // the extremes
					case trial%3 == 1 && i > 6:
						// sparse
					default:
						a[i] = int32(seed>>16) - 32768 //nolint:gosec // 16 bits
					}
				}
				b := append([]int32(nil), a...)
				inverseTransformGo(a, log2, depth, false)
				if !inverseTransformFast(b, log2, depth) {
					t.Skip("no fast transform here")
				}
				for i := range a {
					if a[i] != b[i] {
						t.Fatalf("%d-bit %dx%d trial %d: sample %d = %d, want %d", depth, n, n, trial, i, b[i], a[i])
					}
				}
			}
		}
	}
}

// The bands built with shared horizontal filtering are the ones built by
// interpolate, block by block.
func TestPlaneBands(t *testing.T) {
	for _, depth := range []int{8, 10} {
		e, err := NewEncoder(EncoderOptions{Width: 200, Height: 136, BitDepth: depth, QP: 30, NoVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		pic := e.newPicture()
		src := synthSource(200, 136, depth, 3)
		for y := range 136 {
			copy(pic.y[y*pic.strideY:], src.Y[y*200:y*200+200])
		}
		pic.setAllFinal()
		var a, b refPlanes
		a.init(pic, e.s)
		b.init(pic, e.s)
		for band := range a.bands {
			a.buildBand(band)
			b.buildBandInterp(band)
		}
		for phase := range 16 {
			for i := range a.planes[phase] {
				if a.planes[phase][i] != b.planes[phase][i] {
					t.Fatalf("%d-bit phase %d: sample %d (%d,%d) = %d, want %d", depth, phase, i, i%a.stride, i/a.stride, a.planes[phase][i], b.planes[phase][i])
				}
			}
		}
		a.release()
		b.release()
	}
}

// The SAO edge statistics kernel is the Go one.
func TestSAOEOFast(t *testing.T) {
	seed := uint32(13)
	rnd := func() uint16 {
		seed = seed*1664525 + 1013904223
		return uint16(seed >> 16)
	}
	const stride = 80
	for _, depth := range []int{8, 10} {
		rec := make([]uint16, stride*72)
		src := make([]uint16, stride*72)
		for i := range rec {
			rec[i] = rnd() & uint16(1<<depth-1) & 0xfff8 // runs of equal samples too
			src[i] = rnd() & uint16(1<<depth-1)
		}
		for _, w := range []int{15, 16, 31, 63} {
			for _, off := range [][2]int{{-1, 1}, {-stride, stride}, {-stride - 1, stride + 1}, {-stride + 1, stride - 1}} {
				var s1, c1, s2, c2 [4]int64
				saoEO(rec, stride+1, stride, src, stride+1, stride, w, 63, off[0], off[1], &s1, &c1)
				saoEOGo(rec, stride+1, stride, src, stride+1, stride, w, 63, off[0], off[1], &s2, &c2)
				if s1 != s2 || c1 != c2 {
					t.Fatalf("%d-bit w %d offsets %v: %v %v, want %v %v", depth, w, off, s1, c1, s2, c2)
				}
			}
		}
	}
}
