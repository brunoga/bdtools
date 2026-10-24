package convert

import (
	"context"
	"encoding/binary"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseClock(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90": 90 * time.Second, "1:30": 90 * time.Second, "1:02:03.5": time.Hour + 2*time.Minute + 3500*time.Millisecond,
		"1h30m": 90 * time.Minute, "45s": 45 * time.Second, "0.25": 250 * time.Millisecond,
	} {
		if got, err := ParseClock(in); err != nil || got != want {
			t.Errorf("ParseClock(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"1:60", "a", "1:2:3:4", "-5"} {
		if _, err := ParseClock(bad); err == nil {
			t.Errorf("ParseClock(%q) took it", bad)
		}
	}
}

// A trim across a playlist's clips: the windows narrowed, a clip outside
// emptied, the offsets on the trimmed timeline.
func TestTrimWindows(t *testing.T) {
	s := int64(90000)
	ins, outs := []int64{100 * s, 0, 50 * s}, []int64{110 * s, 20 * s, 60 * s} // 10 s, 20 s, 10 s
	off := trimWindows(ins, outs, Trim{Start: 12 * time.Second, End: 35 * time.Second})
	if ins[0] != outs[0] {
		t.Errorf("clip 0 kept: %d-%d", ins[0], outs[0])
	}
	if ins[1] != 2*s || outs[1] != 20*s || off[1] != 0 {
		t.Errorf("clip 1: %d-%d at %d", ins[1]/s, outs[1]/s, off[1]/s)
	}
	if ins[2] != 50*s || outs[2] != 55*s || off[2] != 18*s {
		t.Errorf("clip 2: %d-%d at %d", ins[2]/s, outs[2]/s, off[2]/s)
	}
	ch := trimChapters([]time.Duration{0, 10 * time.Second, 30 * time.Second, 40 * time.Second}, Trim{Start: 12 * time.Second, End: 35 * time.Second})
	if fmt.Sprint(ch) != "[0s 18s]" {
		t.Errorf("chapters %v", ch)
	}
}

// --start and --duration: a flash and a beep at 4 s in the source are at
// 1 s in the output, which lasts 3 s; from Matroska and a transport
// stream alike.
func TestRunnerTrims(t *testing.T) {
	work := t.TempDir()
	for _, c := range []struct{ name, format string }{{"src.mkv", "matroska"}, {"src.m2ts", "mpegts"}} {
		t.Run(c.format, func(t *testing.T) {
			src := filepath.Join(work, c.name)
			if err := runCmd(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "color=c=black:s=320x240:r=24:d=10,drawbox=c=white:t=fill:enable='between(t,4,4.03)'",
				"-f", "lavfi", "-i", "sine=f=1000:r=48000:d=10,volume=0:enable='not(between(t,4,4.1))'",
				"-c:v", "libx264", "-g", "24", "-c:a", "ac3", "-pes_payload_size", "0", "-f", c.format, src); err != nil {
				t.Skipf("could not build the source: %v", err)
			}
			out := filepath.Join(t.TempDir(), "out.mkv")
			o := DefaultOptions()
			o.Input, o.Output, o.TempDir = src, out, t.TempDir()
			o.Encoder, o.Codec, o.CRF, o.Decoder = EncoderBuiltin, CodecH265, 30, DecoderCPU
			o.Trim = Trim{Start: 3 * time.Second, End: 6 * time.Second}
			var lines []string
			if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(context.Background()); err != nil {
				t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
			}
			ff, err := LookPath("ffmpeg")
			if err != nil {
				t.Skip("no ffmpeg")
			}
			frames, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-i", out, "-vf", "scale=8:8,format=gray", "-f", "rawvideo", "-").Output() //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			n := len(frames) / 64
			flash := -1
			for i := range n {
				var s int
				for _, v := range frames[i*64 : (i+1)*64] {
					s += int(v)
				}
				if s/64 > 128 {
					flash = i
					break
				}
			}
			if n < 70 || n > 74 || flash != 24 {
				t.Errorf("%d frames, the flash at frame %d (want 72, 24)\n%s", n, flash, strings.Join(lines, "\n"))
			}
			pcm, err := exec.CommandContext(t.Context(), ff, "-v", "error", "-i", out, "-ac", "1", "-ar", "8000", "-f", "s16le", "-").Output() //nolint:gosec // test
			if err != nil {
				t.Fatal(err)
			}
			beep := -1
			for i := 0; i+1 < len(pcm); i += 2 {
				if v := int16(binary.LittleEndian.Uint16(pcm[i:])); v > 3000 || v < -3000 {
					beep = i / 2
					break
				}
			}
			at := time.Duration(beep) * time.Second / 8000
			if d := at - time.Second; d < -40*time.Millisecond || d > 40*time.Millisecond {
				t.Errorf("the beep at %v, want 1s", at)
			}
			if total := time.Duration(len(pcm)/2) * time.Second / 8000; total < 2900*time.Millisecond || total > 3100*time.Millisecond {
				t.Errorf("%v of sound, want 3s", total)
			}
		})
	}
}

// The part converted plays from its start to its end, or the source's.
func TestTrimSpan(t *testing.T) {
	const film = 2 * time.Hour
	for _, c := range []struct {
		trim Trim
		want time.Duration
	}{
		{Trim{}, film},
		{Trim{Start: 30 * time.Minute, End: 31 * time.Minute}, time.Minute},
		{Trim{Start: 30 * time.Minute}, film - 30*time.Minute},
		{Trim{Start: 119 * time.Minute, End: 3 * time.Hour}, time.Minute},
		{Trim{Start: 3 * time.Hour}, 0},
	} {
		if got := c.trim.span(film); got != c.want {
			t.Errorf("%v: %v, want %v", c.trim, got, c.want)
		}
	}
	if (Trim{}).span(0) != 0 {
		t.Error("an unknown length")
	}
}
