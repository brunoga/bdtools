package mvc

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
)

// depTestSources makes a dependent view for each picture of a base view
// stream: the base picture shifted right (a disparity), with a gradient
// and some noise, by POC.
func depTestSources(t *testing.T, data []byte) (map[int32]*DepSource, int, int) {
	t.Helper()
	srcs := map[int32]*DepSource{}
	d := NewDecoder(Options{Threads: 1})
	w, h := 0, 0
	_, err := d.DecodeStream(Source{R: bytes.NewReader(data)}, DecodeOptions{}, func(sf *StereoFrame) error {
		f := sf.Base
		w, h = (f.Width+15)&^15, (f.Height+15)&^15
		ds := &DepSource{Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4), StrideY: w, StrideC: w / 2}
		for y := 0; y < h; y++ {
			sy := min(y, f.Height-1)
			for x := 0; x < w; x++ {
				sx := min(max(x-5, 0), f.Width-1)
				v := int(f.Y[sy*f.StrideY+sx]) + y/8 + (x*7+y*13)%5 - 2
				ds.Y[y*w+x] = byte(min(max(v, 0), 255))
			}
		}
		for y := 0; y < h/2; y++ {
			sy := min(y, f.Height/2-1)
			for x := 0; x < w/2; x++ {
				sx := min(max(x-2, 0), f.Width/2-1)
				ds.Cb[y*w/2+x] = f.Cb[sy*f.StrideC+sx]
				ds.Cr[y*w/2+x] = byte(min(int(f.Cr[sy*f.StrideC+sx])+(x+y)%3, 255))
			}
		}
		srcs[f.POC] = ds
		sf.Release()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return srcs, w, h
}

// The coder's model of each macroblock's reconstruction (which its
// rate-distortion decisions rest on) must be what the decoder
// reconstructs, in P and B slices, 4x4 and 8x8 transforms, and chroma.
func TestDepWriterReconstructionMatchesDecoder(t *testing.T) {
	testDepWriterReconstruction(t, false)
}

// The same through the pipeline: every access unit submitted before any
// is collected, so the pictures are coded several at a time.
func TestDepWriterPipelineReconstructionMatchesDecoder(t *testing.T) {
	testDepWriterReconstruction(t, true)
}

func testDepWriterReconstruction(t *testing.T, pipelined bool) {
	t.Helper()
	raw, err := os.ReadFile("../testdata/mvc-source/mvc_base.264")
	if err != nil {
		t.Skip(err)
	}
	// The fixture's base view came off an MVC stream, with prefix NAL
	// units: a plain H.264 base view has none.
	var data []byte
	if err := forEachNAL(raw, func(nal []byte) error {
		if nal[0]&31 != nalPrefix {
			data = append(append(data, 0, 0, 0, 1), nal...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srcs, w, h := depTestSources(t, data)
	mbW := w / 16

	type mbRec struct {
		luma   [256]byte
		chroma [2][64]byte
	}
	// pictures are coded concurrently: the maps are shared
	var mu sync.Mutex
	recon := map[int32][]mbRec{}
	kinds := map[int32][]string{}
	depReconHook = func(pic, mbX, mbY int, luma *[256]byte, chroma *[2][64]byte) {
		mu.Lock()
		recon[int32(pic)][mbY*mbW+mbX] = mbRec{*luma, *chroma}
		mu.Unlock()
	}
	depKindHook = func(pic, mbX, mbY int, kind string) {
		mu.Lock()
		kinds[int32(pic)][mbY*mbW+mbX] = kind
		mu.Unlock()
	}
	defer func() { depReconHook, depKindHook = nil, nil }()

	if m := os.Getenv("DEPINTRA"); m != "" {
		intraKinds = int(m[0] - '0')
		defer func() { intraKinds = 7 }()
	}
	dw := NewDepWriter(28)
	dw.noDeblock = true
	dw.Slices = 2
	dw.AQ = 1 // per-macroblock quantisers, with their deltas
	r := NewAUReader(bytes.NewReader(data))
	var out bytes.Buffer
	for {
		au, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		pick := func(poc int32, _ bool) (*DepSource, error) {
			mu.Lock()
			recon[poc] = make([]mbRec, mbW*(h/16))
			kinds[poc] = make([]string, mbW*(h/16))
			mu.Unlock()
			return srcs[poc], nil
		}
		if pipelined {
			maus, err := dw.Submit(au, pick)
			if err != nil {
				t.Fatal(err)
			}
			for _, mau := range maus {
				out.Write(mau)
			}
			continue
		}
		mau, err := dw.AccessUnitFor(au, pick)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(mau)
	}
	if pipelined {
		maus, err := dw.Flush()
		if err != nil {
			t.Fatal(err)
		}
		for _, mau := range maus {
			out.Write(mau)
		}
	}
	st := dw.Stats()
	if st.Bi == 0 || st.Direct == 0 || st.Skip == 0 || st.InterView == 0 || st.Temporal == 0 {
		t.Errorf("the base view's B pictures should bring every prediction kind into play: %+v", st)
	}

	d := NewDecoder(Options{Threads: 1})
	n := 0
	stats, err := d.DecodeStream(Source{R: bytes.NewReader(out.Bytes())}, DecodeOptions{OnError: func(err error) { t.Error(err) }}, func(sf *StereoFrame) error {
		defer sf.Release()
		f := sf.Dependent
		if f == nil {
			t.Fatalf("POC %d: no dependent view", sf.Base.POC)
		}
		mbs := recon[f.POC]
		if mbs == nil {
			t.Fatalf("POC %d: not coded", f.POC)
		}
		n++
		bad := 0
		for y := 0; y < f.Height; y++ {
			for x := 0; x < f.Width; x++ {
				m := &mbs[(y/16)*mbW+x/16]
				if f.Y[y*f.StrideY+x] != m.luma[(y&15)*16+x&15] {
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
			first := -1
			for y := 0; y < f.Height && first < 0; y++ {
				for x := 0; x < f.Width; x++ {
					m := &mbs[(y/16)*mbW+x/16]
					if f.Y[y*f.StrideY+x] != m.luma[(y&15)*16+x&15] {
						first = (y/16)*mbW + x/16
						break
					}
				}
			}
			var per []int
			for i := first; i < first+8 && i < len(mbs); i++ {
				n := 0
				mx, my := i%mbW, i/mbW
				for y := 0; y < 16; y++ {
					for x := 0; x < 16; x++ {
						if f.Y[(my*16+y)*f.StrideY+mx*16+x] != mbs[i].luma[y*16+x] {
							n++
						}
					}
				}
				per = append(per, n)
			}
			t.Logf("POC %d: luma samples differing per macroblock from %d: %v (kinds %v)", f.POC, first, per, kinds[f.POC][first:min(first+8, len(mbs))])
			m := &mbs[first]
			mx, my := first%mbW, first/mbW
			var dec [16]byte
			copy(dec[:], f.Y[my*16*f.StrideY+mx*16:my*16*f.StrideY+mx*16+16])
			t.Errorf("POC %d: %d samples differ between the coder's reconstruction and the decoder's, first at macroblock %d (%s): row 0 coder %v decoder %v", f.POC, bad, first, kinds[f.POC][first], m.luma[:16], dec)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != len(srcs) || stats.Errors != 0 {
		t.Fatalf("decoded %d of %d dependent pictures, %d errors", n, len(srcs), stats.Errors)
	}
}

// The forward transforms and quantisers invert through the decoder's
// level scales and inverse transforms: at a fine quantiser a residual
// comes back within a step.
func TestDepTransformsRoundTrip(t *testing.T) {
	c := newDepCoder(1, 1, 6)
	c.src = DepSource{Y: make([]byte, 256), Cb: make([]byte, 64), Cr: make([]byte, 64), StrideY: 16, StrideC: 8}
	for i := range c.src.Y {
		c.src.Y[i] = byte(128 + (i*37)%61 - 30)
		c.pred[i] = byte(128 + (i*11)%23 - 11)
	}
	for i := range c.src.Cb {
		c.src.Cb[i] = byte(120 + (i*13)%41)
		c.src.Cr[i] = byte(100 + (i*29)%53)
		c.predCb[i] = byte(128 + (i*5)%17)
		c.predCr[i] = byte(110 + (i*3)%29)
	}
	for _, t8 := range []bool{false, true} {
		var res residual
		c.transformQuant(0, 0, &res)
		if t8 && !c.transformQuant8(0, 0, &res) {
			t.Fatal("the 8x8 transforms coded nothing of a residual the 4x4 ones coded")
		}
		var rec [256]byte
		var recC [2][64]byte
		c.reconLuma(&res, &rec)
		c.reconChroma(&res, &recC)
		worst := 0
		for i := range rec {
			worst = max(worst, abs(int(rec[i])-int(c.src.Y[i])))
		}
		for i := range recC[0] {
			worst = max(worst, abs(int(recC[0][i])-int(c.src.Cb[i])), abs(int(recC[1][i])-int(c.src.Cr[i])))
		}
		if worst > 3 {
			t.Errorf("8x8 transform %v: a sample came back %d off at QP 6", t8, worst)
		}
	}
}
