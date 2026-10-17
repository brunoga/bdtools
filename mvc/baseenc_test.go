package mvc

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// baseTestFrames decodes the fixture's base view into coded-size sources.
func baseTestFrames(t *testing.T) ([]*DepSource, int, int) {
	t.Helper()
	raw, err := os.ReadFile("../testdata/mvc-source/mvc_base.264")
	if err != nil {
		t.Skip(err)
	}
	var frames []*DepSource
	w, h := 0, 0
	d := NewDecoder(Options{Threads: 1})
	_, err = d.DecodeStream(Source{R: bytes.NewReader(raw)}, DecodeOptions{}, func(sf *StereoFrame) error {
		f := sf.Base
		w, h = f.Width, f.Height
		cw, ch := (w+15)&^15, (h+15)&^15
		ds := &DepSource{Y: make([]byte, cw*ch), Cb: make([]byte, cw*ch/4), Cr: make([]byte, cw*ch/4), StrideY: cw, StrideC: cw / 2}
		for y := 0; y < ch; y++ {
			sy := min(y, h-1)
			for x := 0; x < cw; x++ {
				ds.Y[y*cw+x] = f.Y[sy*f.StrideY+min(x, w-1)]
			}
		}
		for y := 0; y < ch/2; y++ {
			sy := min(y, h/2-1)
			for x := 0; x < cw/2; x++ {
				ds.Cb[y*cw/2+x] = f.Cb[sy*f.StrideC+min(x, w/2-1)]
				ds.Cr[y*cw/2+x] = f.Cr[sy*f.StrideC+min(x, w/2-1)]
			}
		}
		frames = append(frames, ds)
		sf.Release()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return frames, w, h
}

// The base view encoder's stream decodes, picture for picture, to what
// the coder modelled, through IDR, P and B pictures (with the deblocking
// filter off so the model is comparable); and ffmpeg, when there is one,
// decodes it to the same pictures as the decoder here.
func TestBaseEncoderRoundTrip(t *testing.T) {
	frames, w, h := baseTestFrames(t)
	slices, bframes := 2, 2
	if v := os.Getenv("BASESLICES"); v != "" {
		slices = int(v[0] - '0')
	}
	if v := os.Getenv("BASEBF"); v != "" {
		bframes = int(v[0] - '0')
	}
	e, err := NewBaseEncoder(BaseOptions{Width: w, Height: h, QP: 26, Keyint: 4, BFrames: bframes, Slices: slices, AQ: 1})
	if err != nil {
		t.Fatal(err)
	}
	e.noDeblock = true
	mbW := e.w / 16
	type mbRec struct {
		luma   [256]byte
		chroma [2][64]byte
	}
	// pictures are coded concurrently: the hooks name each one by its
	// display index
	recon := map[int][]mbRec{}
	kinds := map[int][]string{}
	var mu sync.Mutex
	depReconHook = func(pic, mbX, mbY int, luma *[256]byte, chroma *[2][64]byte) {
		mu.Lock()
		defer mu.Unlock()
		if recon[pic] == nil {
			recon[pic] = make([]mbRec, mbW*(e.h/16))
		}
		recon[pic][mbY*mbW+mbX] = mbRec{*luma, *chroma}
	}
	depKindHook = func(pic, mbX, mbY int, kind string) {
		mu.Lock()
		defer mu.Unlock()
		if kinds[pic] == nil {
			kinds[pic] = make([]string, mbW*(e.h/16))
		}
		kinds[pic][mbY*mbW+mbX] = kind
	}
	defer func() { depReconHook, depKindHook = nil, nil }()

	var out []byte
	for i, f := range frames {
		aus, err := e.Encode(f, int64(i))
		if err != nil {
			t.Fatal(err)
		}
		for _, au := range aus {
			out = append(out, au...)
		}
	}
	aus, err := e.Flush()
	if err != nil {
		t.Fatal(err)
	}
	for _, au := range aus {
		out = append(out, au...)
	}
	st := e.Stats()
	if st.Intra == 0 || st.Skip+st.Direct == 0 || st.Temporal+st.Bi+st.Partitioned == 0 {
		t.Errorf("IDR, P and B pictures should bring every prediction kind into play: %+v", st)
	}

	d := NewDecoder(Options{Threads: 1})
	n := 0
	var decoded [][]byte // Y planes in output order, for ffmpeg
	stats, err := d.DecodeStream(Source{R: bytes.NewReader(out)}, DecodeOptions{OnError: func(err error) { t.Error(err) }}, func(sf *StereoFrame) error {
		defer sf.Release()
		f := sf.Base
		mbs := recon[n] // output order is display order
		if mbs == nil {
			t.Fatalf("frame %d: not coded", n)
		}
		n++
		bad := 0
		for y := 0; y < f.Height; y++ {
			for x := 0; x < f.Width; x++ {
				if f.Y[y*f.StrideY+x] != mbs[(y/16)*mbW+x/16].luma[(y&15)*16+x&15] {
					bad++
				}
			}
		}
		for y := 0; y < f.Height/2; y++ {
			for x := 0; x < f.Width/2; x++ {
				m := &mbs[(y/8)*mbW+x/8]
				if f.Cb[y*f.StrideC+x] != m.chroma[0][(y&7)*8+x&7] || f.Cr[y*f.StrideC+x] != m.chroma[1][(y&7)*8+x&7] {
					bad++
				}
			}
		}
		if bad != 0 {
			var per []int
			first := -1
			for i := range mbs {
				n := 0
				mx, my := i%mbW, i/mbW
				for y := 0; y < 16; y++ {
					for x := 0; x < 16; x++ {
						if f.Y[(my*16+y)*f.StrideY+mx*16+x] != mbs[i].luma[y*16+x] {
							n++
						}
					}
				}
				if n != 0 && first < 0 {
					first = i
				}
				if first >= 0 && len(per) < 8 {
					per = append(per, n)
				}
			}
			t.Errorf("frame %d (POC %d): %d samples differ between the coder's reconstruction and the decoder's; from macroblock %d: %v (kinds %v)", n-1, f.POC, bad, first, per, kinds[n-1][first:min(first+8, len(mbs))])
			mx, my := first%mbW, first/mbW
			for b8 := 0; b8 < 4; b8++ {
				bx, by := (b8&1)*8, (b8>>1)*8
				n := 0
				for y := 0; y < 8; y++ {
					for x := 0; x < 8; x++ {
						if f.Y[(my*16+by+y)*f.StrideY+mx*16+bx+x] != mbs[first].luma[(by+y)*16+bx+x] {
							n++
						}
					}
				}
				if n != 0 {
					var dec, enc [8]byte
					copy(dec[:], f.Y[(my*16+by)*f.StrideY+mx*16+bx:])
					copy(enc[:], mbs[first].luma[by*16+bx:])
					t.Logf("macroblock %d block %d: %d samples differ; row 0 decoder %v coder %v", first, b8, n, dec, enc)
				}
			}
		}
		yp := make([]byte, 0, f.Width*f.Height*3/2)
		for y := 0; y < f.Height; y++ {
			yp = append(yp, f.Y[y*f.StrideY:y*f.StrideY+f.Width]...)
		}
		for _, p := range [][]byte{f.Cb, f.Cr} {
			for y := 0; y < f.Height/2; y++ {
				yp = append(yp, p[y*f.StrideC:y*f.StrideC+f.Width/2]...)
			}
		}
		decoded = append(decoded, yp)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != len(frames) || stats.Errors != 0 {
		t.Fatalf("decoded %d of %d pictures, %d errors", n, len(frames), stats.Errors)
	}

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Log("no ffmpeg: its decode not compared")
		return
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "base.264")
	if err := os.WriteFile(in, out, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := exec.CommandContext(t.Context(), ffmpeg, "-v", "error", "-i", in, "-f", "rawvideo", "-pix_fmt", "yuv420p", "-").Output() //nolint:gosec // test
	if err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	want := bytes.Join(decoded, nil)
	if !bytes.Equal(got, want) {
		t.Errorf("ffmpeg decodes %d bytes differently from the decoder here (%d vs %d bytes)", countDiff(got, want), len(got), len(want))
	}
}

func countDiff(a, b []byte) int {
	n := 0
	for i := 0; i < min(len(a), len(b)); i++ {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}

// The base view encoder codes the same frames to the same stream however
// its concurrent pictures are scheduled.
func TestBaseEncoderDeterministic(t *testing.T) {
	frames, w, h := baseTestFrames(t)
	encode := func() []byte {
		e, err := NewBaseEncoder(BaseOptions{Width: w, Height: h, QP: 26, Keyint: 8, BFrames: 2, Slices: 2})
		if err != nil {
			t.Fatal(err)
		}
		var out []byte
		for i, f := range frames {
			aus, err := e.Encode(f, int64(i))
			if err != nil {
				t.Fatal(err)
			}
			for _, au := range aus {
				out = append(out, au...)
			}
		}
		aus, err := e.Flush()
		if err != nil {
			t.Fatal(err)
		}
		for _, au := range aus {
			out = append(out, au...)
		}
		return out
	}
	if !bytes.Equal(encode(), encode()) {
		t.Fatal("the same frames coded to a different stream")
	}
}
