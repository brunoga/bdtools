//go:build (linux || windows) && (amd64 || arm64)

package gpu

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

func TestNVENCProducesAStream(t *testing.T) {
	for _, c := range []Codec{H264, HEVC} {
		b := encodeTest(t, NVENC, c, 640, 360, 40)
		if !bytes.HasPrefix(b, []byte{0, 0, 0, 1}) || len(b) < 1000 {
			t.Fatalf("codec %d: %d bytes, starting % x", c, len(b), b[:min(8, len(b))])
		}
	}
}

// The stream must decode to the pictures that went in: decoded with this
// repository's decoder, every frame is close to the source pattern.
func TestNVENCDecodesBack(t *testing.T) {
	const w, h, n = 640, 368, 30
	b := encodeTest(t, NVENC, H264, w, h, n)
	checkDecode(t, b, w, h, n)
}

func TestNVENCHEVCDecodesBack(t *testing.T) {
	const w, h, n = 640, 368, 30
	b := encodeTest(t, NVENC, HEVC, w, h, n)
	checkDecodeFFmpeg(t, b, "hevc", w, h, n)
}

// AV1 comes out as low-overhead OBUs, which ffmpeg's obu demuxer reads.
func TestNVENCAV1DecodesBack(t *testing.T) {
	const w, h, n = 640, 368, 30
	b := encodeTest(t, NVENC, AV1, w, h, n)
	checkDecodeFFmpeg(t, b, "obu", w, h, n)
}

// At 10 bits the stream is HEVC Main 10 or 10-bit AV1, made from P010.
func TestNVENC10Bit(t *testing.T) {
	const w, h, n = 640, 368, 30
	checkDecode10(t, encodeDepth(t, NVENC, HEVC, 10, w, h, n), "hevc", w, h, n)
	checkDecode10(t, encodeDepth(t, NVENC, AV1, 10, w, h, n), "obu", w, h, n)
}

// At a constant quality the stream decodes as well, and a finer quality
// takes more bits.
func TestNVENCConstantQuality(t *testing.T) {
	const w, h, n = 640, 368, 30
	cfg := Config{Codec: HEVC, Width: w, Height: h, FPSNum: 24000, FPSDen: 1001, GOP: 12, BitDepth: 10}
	cfg.CQ = 20
	fine := encodeConfig(t, NVENC, cfg, n)
	checkDecode10(t, fine, "hevc", w, h, n)
	cfg.CQ = 40
	if coarse := encodeConfig(t, NVENC, cfg, n); len(coarse) >= len(fine) {
		t.Errorf("CQ 40 took %d bytes, CQ 20 %d", len(coarse), len(fine))
	}
}

// A stereo session codes two views: layer 1 NAL units appear, and each
// view decodes back to its own pattern (ffmpeg's hevc decoder picks a
// view with -view_ids).
func TestNVENCMVHEVCDecodesBothViews(t *testing.T) {
	const w, h, n = 640, 368, 24
	cfg := Config{Codec: HEVC, Width: w, Height: h, FPSNum: 24000, FPSDen: 1001, QP: 24, GOP: 12, Views: 2}
	var out bytes.Buffer
	e, err := Open(NVENC, cfg, &out)
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("NVENC: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	se, ok := e.(StereoEncoder)
	if !ok {
		t.Fatal("not a StereoEncoder")
	}
	for i := 0; i < n; i++ {
		if err := se.EncodeStereo(func(left, right *Picture) {
			fillGradient(left, w, h, i)
			fillGradient(right, w, h, i+50) // a different picture for the other eye
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	stream := out.Bytes()
	if path := os.Getenv("NVENC_MVHEVC_DUMP"); path != "" {
		if err := os.WriteFile(path, stream, 0o600); err != nil { //nolint:gosec // a test dump
			t.Fatal(err)
		}
	}
	layers := map[int]int{}
	for i := 0; i+5 < len(stream); i++ {
		if stream[i] == 0 && stream[i+1] == 0 && stream[i+2] == 1 {
			layers[int(stream[i+3]&1)<<5|int(stream[i+4]>>3)]++
		}
	}
	if layers[1] < n || layers[0] < n {
		t.Fatalf("NAL units per layer %v, want at least %d in layers 0 and 1", layers, n)
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to decode with")
	}
	for view := 0; view < 2; view++ {
		cmd := exec.CommandContext(t.Context(), ff, "-v", "error", "-view_ids", fmt.Sprint(view), "-f", "hevc", "-i", "-", //nolint:gosec // test
			"-f", "rawvideo", "-pix_fmt", "yuv420p", "-")
		cmd.Stdin = bytes.NewReader(stream)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		dec, err := cmd.Output()
		if err != nil {
			t.Fatalf("ffmpeg view %d: %v %s", view, err, stderr.String())
		}
		size := w * h * 3 / 2
		if len(dec) != size*n {
			t.Fatalf("view %d: decoded %d bytes, want %d frames of %dx%d", view, len(dec), n, w, h)
		}
		ref := &Picture{Y: make([]byte, w*h), UV: make([]byte, w*h/2), Pitch: w}
		for i := range n {
			fillGradient(ref, w, h, i+50*view)
			if psnr := lumaPSNR(dec[i*size:i*size+w*h], ref.Y); psnr < 30 {
				t.Errorf("view %d frame %d: luma PSNR %.1f dB", view, i, psnr)
			}
		}
	}
}
