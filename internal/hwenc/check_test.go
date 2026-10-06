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

// checkDecode10 is checkDecodeFFmpeg for a 10-bit stream: ffmpeg must
// decode it as 10-bit, and each frame must be close to fillGradient's
// 10-bit pattern, which has detail below the 8-bit step.
func checkDecode10(t *testing.T, stream []byte, format string, w, h, n int) {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to decode with")
	}
	probe := exec.CommandContext(t.Context(), ff, "-v", "info", "-f", format, "-i", "-") //nolint:gosec // test
	probe.Stdin = bytes.NewReader(stream)
	info, _ := probe.CombinedOutput() // "At least one output file" is an error, and expected
	if !bytes.Contains(info, []byte("yuv420p10le")) {
		t.Fatalf("not a 10-bit stream:\n%s", info)
	}
	cmd := exec.CommandContext(t.Context(), ff, "-v", "error", "-f", format, "-i", "-", "-f", "rawvideo", "-pix_fmt", "yuv420p10le", "-") //nolint:gosec // test
	cmd.Stdin = bytes.NewReader(stream)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || stderr.Len() > 0 {
		t.Fatalf("ffmpeg: %v %s", err, stderr.String())
	}
	size := w * h * 3 // 4:2:0, two bytes a sample
	if len(out) != size*n {
		t.Fatalf("decoded %d bytes, want %d frames of %dx%d", len(out), n, w, h)
	}
	ref := &Picture{Y: make([]byte, 2*w*h), UV: make([]byte, w*h), Pitch: 2 * w, Depth: 10}
	fine := 0 // luma samples between two 8-bit levels
	for i := range n {
		fillGradient(ref, w, h, i)
		var se float64
		for j := range w * h {
			v := int(out[i*size+2*j]) | int(out[i*size+2*j+1])<<8
			if v&3 != 0 {
				fine++
			}
			got := float64(v)
			want := float64((int(ref.Y[2*j]) | int(ref.Y[2*j+1])<<8) >> 6)
			se += (got - want) * (got - want)
		}
		if psnr := 10 * math.Log10(1023*1023/(se/float64(w*h)+1e-9)); psnr < 30 {
			t.Errorf("frame %d: luma PSNR %.1f dB", i, psnr)
		}
	}
	// The pattern steps by a quarter of an 8-bit level, so three samples in
	// four fall between 8-bit levels; a stream made at 8 bits has none.
	if share := float64(fine) / float64(w*h*n); share < 0.5 {
		t.Errorf("only %.0f%% of luma samples use the bits below 8-bit precision", 100*share)
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

// fillGradient draws a moving test pattern; at depth 10, one whose luma
// steps by a quarter of an 8-bit level.
func fillGradient(p *Picture, w, h, n int) {
	if p.Depth == 10 {
		put := func(b []byte, i, v int) { v = (v & 1023) << 6; b[2*i], b[2*i+1] = byte(v), byte(v>>8) }
		for y := 0; y < h; y++ {
			row := p.Y[y*p.Pitch : y*p.Pitch+2*w]
			for x := range w {
				put(row, x, x+y+12*n)
			}
		}
		for y := 0; y < h/2; y++ {
			row := p.UV[y*p.Pitch : y*p.Pitch+2*w]
			for x := 0; x < w; x += 2 {
				put(row, x, 512+4*(y-n))
				put(row, x+1, 512+x)
			}
		}
		return
	}
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
	return encodeDepth(t, k, codec, 8, w, h, n)
}

// encodeDepth is encodeTest at a bit depth.
func encodeDepth(t *testing.T, k Kind, codec Codec, depth, w, h, n int) []byte {
	t.Helper()
	var out bytes.Buffer
	e, err := Open(k, Config{Codec: codec, Width: w, Height: h, FPSNum: 24000, FPSDen: 1001, QP: 24, GOP: 12,
		Device: "/dev/dri/renderD128", BitDepth: depth}, &out)
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("%s: %v", k, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := e.Encode(func(p *Picture) {
			if p.Depth != max(depth, 8) {
				t.Fatalf("a %d-bit picture for a %d-bit encode", p.Depth, depth)
			}
			fillGradient(p, w, h, i)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
