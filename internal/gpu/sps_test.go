package gpu

import (
	"fmt"
	"os/exec"
	"slices"
	"testing"
)

// The SPS readers give the displayed size and depth ffprobe does, for
// streams that crop (1080 coded as 1088) and at 10 bits.
func TestSPSInfo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	hdr := []string{"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc"}
	for _, c := range []struct {
		hevc  bool
		args  []string
		w, h  int
		depth int
	}{
		{true, append([]string{"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params",
			"log-level=error:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc"}, hdr...), 1920, 1080, 10},
		{true, []string{"-c:v", "libx265", "-pix_fmt", "yuv420p", "-x265-params", "log-level=error:scaling-list=default"}, 640, 360, 8},
		{true, []string{"-c:v", "libx265", "-pix_fmt", "yuv420p", "-x265-params", "log-level=error"}, 1278, 718, 8},
		{false, []string{"-c:v", "libx264", "-pix_fmt", "yuv420p"}, 1920, 1080, 8},
		{false, append([]string{"-c:v", "libx264", "-pix_fmt", "yuv420p10le", "-x264-params",
			"colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc"}, hdr...), 1278, 718, 10},
		{false, []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-flags", "+ilme+ildct"}, 1920, 1080, 8},
	} {
		t.Run(fmt.Sprintf("%v %dx%d %d", c.hevc, c.w, c.h, c.depth), func(t *testing.T) {
			format := "h264"
			if c.hevc {
				format = "hevc"
			}
			args := append([]string{"-v", "error", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=s=%dx%d:r=24:d=0.1", c.w, c.h)}, c.args...)
			es, err := exec.CommandContext(t.Context(), "ffmpeg", append(args, "-f", format, "-")...).Output() //nolint:gosec // test
			if err != nil {
				t.Skipf("making the stream: %v", err)
			}
			var info spsInfo
			found := false
			for _, nal := range annexBNALs(es) {
				if c.hevc && nal[0]>>1&0x3f == 33 {
					info, err = hevcSPS(nal)
					found = true
					break
				}
				if !c.hevc && nal[0]&0x1f == 7 {
					info, err = h264SPS(nal)
					found = true
					break
				}
			}
			if !found || err != nil {
				t.Fatalf("no SPS read: %v", err)
			}
			if info.width() != c.w || info.height() != c.h || info.depth != c.depth {
				t.Errorf("%dx%d at %d bits (coded %dx%d), want %dx%d at %d", info.width(), info.height(), info.depth,
					info.codedW, info.codedH, c.w, c.h, c.depth)
			}
			want := ColorInfo{Primaries: 2, Transfer: 2, Matrix: 2}
			if slices.Contains(c.args, "smpte2084") {
				want = ColorInfo{Primaries: 9, Transfer: 16, Matrix: 9}
			}
			if info.color != want {
				t.Errorf("colour %+v, want %+v", info.color, want)
			}
		})
	}
}
