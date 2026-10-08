//go:build linux && (amd64 || arm64)

package gpu

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// annexBUnits splits an Annex B stream into access units at each picture's
// first slice (or the parameter sets and SEI before it): what a demuxer
// hands the decoder.
func annexBUnits(b []byte, hevc bool) [][]byte {
	var starts []int
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			starts = append(starts, i+3)
		}
	}
	firstSlice := func(nal []byte) (aud, prefix, slice bool) {
		if hevc {
			t := nal[0] >> 1 & 0x3f
			switch {
			case t == 35:
				return true, false, false
			case t >= 32 && t <= 39 && t != 38:
				return false, true, false
			case t < 32:
				return false, false, len(nal) > 2 && nal[2]&0x80 != 0 // first_slice_segment_in_pic
			}
			return false, false, false
		}
		t := nal[0] & 0x1f
		switch t {
		case 9:
			return true, false, false
		case 6, 7, 8:
			return false, true, false
		case 1, 5:
			return false, false, len(nal) > 1 && nal[1]&0x80 != 0 // first_mb_in_slice == 0
		}
		return false, false, false
	}
	var units [][]byte
	unitStart, open, sawSlice := 0, false, false
	for k, s := range starts {
		nalStart := s - 3
		if nalStart > 0 && b[nalStart-1] == 0 {
			nalStart--
		}
		aud, prefix, first := firstSlice(b[s:])
		if (aud || prefix || first) && open && sawSlice {
			units = append(units, b[unitStart:nalStart])
			unitStart, sawSlice = nalStart, false
		}
		if !open {
			unitStart, open = nalStart, true
		}
		if first || (!aud && !prefix) {
			sawSlice = true
		}
		_ = k
	}
	if open {
		units = append(units, b[unitStart:])
	}
	return units
}

// ffmpegFrames decodes a stream with ffmpeg into raw frames of the layout
// the GPU decoders give (nv12 or p010le).
func ffmpegFrames(t *testing.T, path, format, pixfmt string, w, h int) []string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-f", format, "-i", path, //nolint:gosec // test
		"-f", "rawvideo", "-pix_fmt", pixfmt, "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	size := w * h * 3 / 2
	if pixfmt == "p010le" {
		size *= 2
	}
	var sums []string
	for len(out) >= size {
		sums = append(sums, string(out[:size]))
		out = out[size:]
	}
	return sums
}

// decodeAll decodes a stream with NVDEC, each picture's planes with the
// pictures' timestamps.
func decodeAll(t *testing.T, codec VideoCodec, units [][]byte) ([]string, []int64, *DecodedPicture) {
	t.Helper()
	var sums []string
	var pts []int64
	var last DecodedPicture
	d, err := OpenDecoder(NVENC, DecodeConfig{Codec: codec}, func(p *DecodedPicture) error {
		var b bytes.Buffer
		row := p.Width
		if p.Depth > 8 {
			row *= 2
		}
		for y := range p.Height {
			b.Write(p.Y[y*p.Pitch : y*p.Pitch+row])
		}
		for y := range p.Height / 2 {
			b.Write(p.UV[y*p.Pitch : y*p.Pitch+row])
		}
		sums = append(sums, b.String())
		pts = append(pts, p.PTS)
		last = *p
		return nil
	})
	if errors.Is(err, ErrDecodeUnavailable) {
		t.Skipf("no NVDEC: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	for i, u := range units {
		if err := d.Decode(u, int64(i)*3754); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Flush(); err != nil {
		t.Fatal(err)
	}
	return sums, pts, &last
}

// NVDEC decodes exactly what ffmpeg's software decoders do, in display
// order with the timestamps given in decoding order put back in order.
func TestNVDECMatchesFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	gen := func(name string, args ...string) string {
		p := filepath.Join(dir, name)
		cmd := exec.CommandContext(t.Context(), "ffmpeg", append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", //nolint:gosec // test
			"testsrc2=s=640x360:r=24:d=2"}, append(args, p)...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("making a test stream: %v %s", err, out)
		}
		return p
	}
	for _, c := range []struct {
		name   string
		codec  VideoCodec
		path   string
		format string
		pixfmt string
		w, h   int
		depth  int
	}{
		{"H.264 (the MVC fixture's base view)", DecodeH264, filepath.Join("..", "..", "testdata", "mvc-source", "mvc_base.264"), "h264", "nv12", 640, 480, 8},
		{"HEVC 8-bit, B-frames, cropped", DecodeHEVC, gen("a.hevc", "-c:v", "libx265", "-x265-params", "bframes=3:log-level=error", "-pix_fmt", "yuv420p"), "hevc", "nv12", 640, 360, 8},
		{"HEVC 10-bit", DecodeHEVC, gen("b.hevc", "-c:v", "libx265", "-x265-params", "bframes=3:log-level=error", "-pix_fmt", "yuv420p10le"), "hevc", "p010le", 640, 360, 10},
		{"MPEG-2, B-frames", DecodeMPEG2, gen("c.m2v", "-c:v", "mpeg2video", "-bf", "2", "-q:v", "4"), "mpegvideo", "nv12", 640, 360, 8},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, err := os.ReadFile(c.path) //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			want := ffmpegFrames(t, c.path, c.format, c.pixfmt, c.w, c.h)
			units := annexBUnits(b, c.codec == DecodeHEVC)
			if c.codec == DecodeMPEG2 {
				units = mpeg2Units(b)
			}
			got, pts, last := decodeAll(t, c.codec, units)
			if last.Width != c.w || last.Height != c.h || last.Depth != c.depth {
				t.Errorf("pictures %dx%d at %d bits, want %dx%d at %d", last.Width, last.Height, last.Depth, c.w, c.h, c.depth)
			}
			if len(got) != len(want) {
				t.Fatalf("%d pictures, ffmpeg %d", len(got), len(want))
			}
			for i := range want {
				if c.codec == DecodeMPEG2 {
					// MPEG-2's inverse transform is not bit-exact by definition
					// (IEEE 1180 accuracy): decoders differ in the last bit.
					if p := lumaPSNR([]byte(got[i][:c.w*c.h]), []byte(want[i][:c.w*c.h])); p < 45 {
						t.Fatalf("picture %d: %.1f dB from ffmpeg's", i, p)
					}
					continue
				}
				if got[i] != want[i] {
					t.Fatalf("picture %d differs from ffmpeg's", i)
				}
			}
			for i := 1; i < len(pts); i++ {
				if pts[i] <= pts[i-1] {
					t.Fatalf("timestamps out of order: %v", pts)
				}
			}
		})
	}
}

// mpeg2Units splits an MPEG-2 video stream into pictures: each from a
// sequence or GOP header, or else a picture start code, to the next.
func mpeg2Units(b []byte) [][]byte {
	var units [][]byte
	start, sawPicture := 0, false
	for i := 0; i+3 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		switch b[i+3] {
		case 0xb3, 0xb8, 0x00: // sequence header, GOP, picture
			if sawPicture {
				units = append(units, b[start:i])
				start, sawPicture = i, false
			}
			if b[i+3] == 0x00 {
				sawPicture = true
			}
		}
	}
	return append(units, b[start:])
}
