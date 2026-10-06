//go:build (darwin || linux || windows) && (amd64 || arm64)

package hwenc

import (
	"bytes"
	"errors"
	"math"
	"os/exec"
	"testing"

	"github.com/brunoga/mvc"
)

// checkDecode decodes an H.264 stream with the repository's decoder and
// checks each frame against fillGradient's pattern by PSNR.
func checkDecode(t *testing.T, stream []byte, w, h, n int) {
	t.Helper()
	dec := mvc.NewDecoder(mvc.Options{})
	ref := &Picture{Y: make([]byte, w*h), UV: make([]byte, w*h/2), Pitch: w}
	i := 0
	_, err := dec.DecodeStream(mvc.Source{Format: mvc.FormatAnnexB, R: bytes.NewReader(stream)}, mvc.DecodeOptions{},
		func(sf *mvc.StereoFrame) error {
			f := sf.Base
			if f.Width != w || f.Height != h {
				t.Fatalf("frame %d is %dx%d", i, f.Width, f.Height)
			}
			fillGradient(ref, w, h, i)
			var se float64
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					d := float64(f.Y[y*f.StrideY+x]) - float64(ref.Y[y*w+x])
					se += d * d
				}
			}
			psnr := 10 * math.Log10(255*255/(se/float64(w*h)+1e-9))
			if psnr < 30 {
				t.Errorf("frame %d: luma PSNR %.1f dB", i, psnr)
			}
			i++
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if i != n {
		t.Errorf("decoded %d frames, want %d", i, n)
	}
}

// checkDecodeFFmpeg is checkDecode for streams this repository cannot
// decode (HEVC): ffmpeg decodes them, and the test skips without it.
func checkDecodeFFmpeg(t *testing.T, stream []byte, format string, w, h, n int) {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to decode with")
	}
	cmd := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", format, "-i", "-", "-f", "rawvideo", "-pix_fmt", "yuv420p", "-") //nolint:gosec // test
	cmd.Stdin = bytes.NewReader(stream)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || stderr.Len() > 0 {
		t.Fatalf("ffmpeg: %v %s", err, stderr.String())
	}
	size := w * h * 3 / 2
	if len(out) != size*n {
		t.Fatalf("decoded %d bytes, want %d frames of %dx%d", len(out), n, w, h)
	}
	ref := &Picture{Y: make([]byte, w*h), UV: make([]byte, w*h/2), Pitch: w}
	for i := range n {
		fillGradient(ref, w, h, i)
		if psnr := lumaPSNR(out[i*size:i*size+w*h], ref.Y); psnr < 30 {
			t.Errorf("frame %d: luma PSNR %.1f dB", i, psnr)
		}
	}
}

func lumaPSNR(a, b []byte) float64 {
	var se float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		se += d * d
	}
	return 10 * math.Log10(255*255/(se/float64(len(a))+1e-9))
}

// fillGradient draws a moving test pattern.
func fillGradient(p *Picture, w, h, n int) {
	for y := 0; y < h; y++ {
		row := p.Y[y*p.Pitch : y*p.Pitch+w]
		for x := range row {
			row[x] = byte(x + y + 3*n)
		}
	}
	for y := 0; y < h/2; y++ {
		row := p.UV[y*p.Pitch : y*p.Pitch+w]
		for x := 0; x < w; x += 2 {
			row[x], row[x+1] = byte(128+y-n), byte(128+x/4)
		}
	}
}

// encodeTest encodes n frames and returns the stream, skipping when the
// encoder is not available on this machine.
func encodeTest(t *testing.T, k Kind, codec Codec, w, h, n int) []byte {
	t.Helper()
	var out bytes.Buffer
	e, err := Open(k, Config{Codec: codec, Width: w, Height: h, FPSNum: 24000, FPSDen: 1001, QP: 24, GOP: 12, Device: "/dev/dri/renderD128"}, &out)
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("%s: %v", k, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := e.Encode(func(p *Picture) { fillGradient(p, w, h, i) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
