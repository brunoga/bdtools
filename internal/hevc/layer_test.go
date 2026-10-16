package hevc

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// layerSums decodes an MV-HEVC stream's two views, giving each view's MD5
// over its pictures as ffmpeg -view_ids N writes them (yuv420p, or
// yuv420p10le above 8 bits) and how many pictures it had.
func layerSums(t *testing.T, data []byte, threads int) (sums [2]string, n [2]int) {
	t.Helper()
	d := New()
	d.SetThreads(threads)
	d.SetLayers(2)
	h := [2]hash.Hash{md5.New(), md5.New()}
	out := func(p *Picture) error {
		w := h[p.Layer]
		n[p.Layer]++
		// 8 bits a sample, or 16 little-endian above 8 (yuv420p10le).
		row := make([]byte, 2*p.Width)
		plane := func(pl []uint16, stride, w0, h0 int) {
			for y := range h0 {
				n := 0
				for x := range w0 {
					v := pl[y*stride+x]
					if p.BitDepth > 8 {
						row[n], row[n+1] = byte(v), byte(v>>8)
						n += 2
					} else {
						row[n] = byte(v)
						n++
					}
				}
				w.Write(row[:n])
			}
		}
		plane(p.Y, p.StrideY, p.Width, p.Height)
		plane(p.Cb, p.StrideC, p.Width/2, p.Height/2)
		plane(p.Cr, p.StrideC, p.Width/2, p.Height/2)
		return nil
	}
	if err := d.Decode(data, 0, out); err != nil {
		t.Fatal(err)
	}
	if err := d.Flush(out); err != nil {
		t.Fatal(err)
	}
	if d.Errors() != 0 {
		t.Errorf("%d slices did not decode", d.Errors())
	}
	for i := range 2 {
		sums[i] = hex.EncodeToString(h[i].Sum(nil))
	}
	return sums, n
}

// NVENC's MV-HEVC (the left view as the base layer, the right predicted
// from it) decodes as ffmpeg decodes it, both views.
func TestDecodeMVHEVC(t *testing.T) {
	data, err := os.ReadFile("../mkv/testdata/mvhevc.hevc")
	if err != nil {
		t.Fatal(err)
	}
	want := [2]string{"bdd015b45ba42cae44f8e32a4aa50b45", "626b80482f8f523bbe749f6fffba28ad"} // ffmpeg -view_ids 0 and 1, yuv420p
	for _, threads := range []int{1, 8} {
		sums, n := layerSums(t, data, threads)
		if n[0] != 24 || n[1] != 24 {
			t.Fatalf("threads %d: %d and %d pictures, want 24 each", threads, n[0], n[1])
		}
		for i := range 2 {
			if sums[i] != want[i] {
				t.Errorf("threads %d: layer %d sums %s, want %s", threads, i, sums[i], want[i])
			}
		}
	}
	// Told nothing, the decoder gives the base layer alone.
	d := New()
	count := 0
	out := func(p *Picture) error {
		if p.Layer != 0 {
			t.Errorf("a picture of layer %d", p.Layer)
		}
		count++
		return nil
	}
	if err := d.Decode(data, 0, out); err != nil {
		t.Fatal(err)
	}
	if err := d.Flush(out); err != nil {
		t.Fatal(err)
	}
	if count != 24 {
		t.Errorf("%d base pictures, want 24", count)
	}
}

// TestDecodeMVHEVCFile compares the two views of the MV-HEVC stream in
// $BDTOOLS_MVHEVC (Annex B) with ffmpeg's.
func TestDecodeMVHEVCFile(t *testing.T) {
	path := os.Getenv("BDTOOLS_MVHEVC")
	if path == "" {
		t.Skip("BDTOOLS_MVHEVC not set")
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	data, err := os.ReadFile(path) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	sums, n := layerSums(t, data, 8)
	for view := range 2 {
		out, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-view_ids", fmt.Sprint(view), "-i", path, //nolint:gosec // test
			"-f", "rawvideo", "-").Output()
		if err != nil {
			t.Fatal(err)
		}
		if s := fmt.Sprintf("%x", md5.Sum(out)); s != sums[view] {
			t.Errorf("view %d (%d pictures): sums %s, ffmpeg's %s", view, n[view], sums[view], s)
		}
	}
}

// ffmpegViewSums is ffmpeg's decoding of each view of the stream at path,
// as layerSums gives it; ok false without ffmpeg.
func ffmpegViewSums(t *testing.T, path string) (sums [2]string, ok bool) {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return sums, false
	}
	for view := range 2 {
		out, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-view_ids", fmt.Sprint(view), "-f", "hevc", "-i", path, //nolint:gosec // test
			"-f", "rawvideo", "-").Output()
		if err != nil {
			if os.Getenv("HEVC_KEEP") != "" {
				_ = os.WriteFile(os.Getenv("HEVC_KEEP"), mustRead(path), 0o600)
			}
			t.Fatalf("ffmpeg, view %d: %v", view, err)
		}
		sums[view] = fmt.Sprintf("%x", md5.Sum(out))
	}
	return sums, true
}

// shifted is a second view of a test picture: moved left by dx samples
// (the edge repeated) and a little brighter, as the other eye sees it.
func shifted(s *Source, w, h, dx, depth int) *Source {
	r := &Source{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4), StrideY: w, StrideC: w / 2}
	maxV := uint16(1<<depth - 1)
	for y := range h {
		for x := range w {
			r.Y[y*w+x] = min(s.Y[y*w+min(x+dx, w-1)]+uint16(2<<(depth-8)), maxV)
		}
	}
	for y := range h / 2 {
		for x := range w / 2 {
			r.Cb[y*w/2+x] = s.Cb[y*w/2+min(x+dx/2, w/2-1)]
			r.Cr[y*w/2+x] = s.Cr[y*w/2+min(x+dx/2, w/2-1)]
		}
	}
	return r
}

// The encoder's MV-HEVC decodes, both views, to its own pictures (its
// verification) and as ffmpeg decodes it; the second view, predicted
// from the first, costs a fraction of it.
func TestEncoderStereo(t *testing.T) {
	const w, h, frames = 200, 136, 12
	for _, tc := range []struct {
		name                   string
		depth, keyint, bframes int
	}{{"8bit-B3", 8, 8, 3}, {"10bit-B7", 10, 0, 7}, {"8bit-P", 8, 5, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			o := EncoderOptions{Width: w, Height: h, BitDepth: tc.depth, QP: 27, FPSNum: 25, FPSDen: 1,
				Keyint: tc.keyint, BFrames: tc.bframes, Views: 2}
			e, err := NewEncoder(o)
			if err != nil {
				t.Fatal(err)
			}
			var stream []byte
			for i := range frames {
				l := synthSource(w, h, tc.depth, i)
				aus, err := e.EncodeStereo(l, shifted(l, w, h, 6, tc.depth))
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
			if st := e.Stats(); st.Pictures != 2*frames {
				t.Fatalf("%d pictures coded, want %d", st.Pictures, 2*frames)
			}
			var size [2]int
			for _, n := range nalUnits(stream) {
				if n.typ < 32 {
					size[n.layer&1] += len(n.rbsp)
				}
			}
			t.Logf("base layer %d bytes, second view %d", size[0], size[1])
			if size[1] > size[0]/2 {
				t.Errorf("the second view takes %d bytes, the first %d: little predicted from it", size[1], size[0])
			}
			sums, n := layerSums(t, stream, 4)
			if n[0] != frames || n[1] != frames {
				t.Fatalf("decoded %d and %d pictures, want %d each", n[0], n[1], frames)
			}
			path := filepath.Join(t.TempDir(), "stereo.hevc")
			if err := os.WriteFile(path, stream, 0o600); err != nil {
				t.Fatal(err)
			}
			if ff, ok := ffmpegViewSums(t, path); ok && ff != sums {
				t.Errorf("ffmpeg decodes the views to %v, this decoder to %v", ff, sums)
			}
		})
	}
}

func mustRead(path string) []byte {
	b, _ := os.ReadFile(path) //nolint:gosec // test
	return b
}
