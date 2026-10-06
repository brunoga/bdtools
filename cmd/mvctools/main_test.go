package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capture runs the command with argv, returning stdout, stderr and the code.
// Real *os.File handles are used because that is what run takes, and they are
// what the flag package writes to.
func capture(t *testing.T, argv ...string) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	mk := func(name string) *os.File {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	out, errf := mk("stdout"), mk("stderr")
	code := run(argv, out, errf)
	_ = out.Close()
	_ = errf.Close()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return read("stdout"), read("stderr"), code
}

func TestVersion(t *testing.T) {
	out, _, code := capture(t, "--version")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "mvctools") {
		t.Errorf("stdout = %q, should name the tool", out)
	}
}

// --dry-run must work on a machine where nothing is installed yet: its whole
// point is showing what would run. The encoder is pinned so the assertion does
// not depend on what hardware the test machine happens to have.
func TestDryRunNeedsNoTools(t *testing.T) {
	out, _, code := capture(t, "--dry-run", "--encoder", "x264",
		"--input", "/media/Life of Pi (2012)/disc.iso",
		"--output", "/out/Life of Pi (2012).mkv",
		"--temp", "/tmp/w")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"read /media/Life of Pi (2012)/disc.iso", "mvcdec", "x264", "write /out/Life of Pi (2012).mkv", "built in"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run should mention %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tsMuxeR") || strings.Contains(out, "mkvmerge") {
		t.Errorf("the built-in demuxer and muxer are the default:\n%s", out)
	}
	mkv, _, code := capture(t, "--dry-run", "--encoder", "x264", "--muxer", "mkvmerge",
		"--input", "/media/a.iso", "--output", "/out/a.mkv", "--temp", "/tmp/w")
	if code != 0 || !strings.Contains(mkv, "mkvmerge -o /out/a.mkv") {
		t.Errorf("--muxer mkvmerge: exit %d\n%s", code, mkv)
	}
	// And tsMuxeR is still there when asked for.
	tsm, _, code := capture(t, "--dry-run", "--encoder", "x264", "--demuxer", "tsmuxer",
		"--input", "/media/a.iso", "--output", "/out/a.mkv", "--temp", "/tmp/w")
	if code != 0 || !strings.Contains(tsm, "tsMuxeR") {
		t.Errorf("--demuxer tsmuxer: exit %d\n%s", code, tsm)
	}
	// The decoder stacks the eyes itself, so nothing else should.
	for _, unwanted := range []string{"StackHorizontal", "vspipe", "vapoursynth"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the pipeline no longer uses %q:\n%s", unwanted, out)
		}
	}
}

// Full-SBS is what the decoder emits, so it needs no filter and the standalone
// encoder runs it. Half-SBS squeezes the stacked pair, which x264 cannot do, so
// the same library is reached through ffmpeg instead of the request being
// refused.
func TestDryRunShowsTheChosenLayout(t *testing.T) {
	full, _, code := capture(t, "--dry-run", "--encoder", "x264", "--layout", "full",
		"--input", "/in/a.iso", "--output", "/out/a.mkv", "--temp", "/tmp/w")
	if code != 0 {
		t.Fatalf("full-SBS with x264 should work, exit = %d", code)
	}
	if strings.Contains(full, "scale=") {
		t.Errorf("full-SBS must not rescale:\n%s", full)
	}
	if !strings.Contains(full, "x264 --demuxer y4m") {
		t.Errorf("unfiltered software encoding should use the standalone binary:\n%s", full)
	}

	soft, _, code := capture(t, "--dry-run", "--encoder", "software", "--layout", "half",
		"--input", "/in/a.iso", "--output", "/out/a.mkv", "--temp", "/tmp/w")
	if code != 0 {
		t.Fatalf("half-SBS with software encoding should work now, exit = %d", code)
	}
	if !strings.Contains(soft, "libx264") || !strings.Contains(soft, "scale=iw/2:ih") {
		t.Errorf("half-SBS software encoding should squeeze through libx264:\n%s", soft)
	}

	half, _, code := capture(t, "--dry-run", "--encoder", "nvenc", "--gpu-api", "ffmpeg", "--layout", "half",
		"--input", "/in/a.iso", "--output", "/out/a.mkv", "--temp", "/tmp/w")
	if code != 0 {
		t.Fatalf("half-SBS with an ffmpeg encoder should work, exit = %d", code)
	}
	if !strings.Contains(half, "scale=iw/2:ih") {
		t.Errorf("half-SBS must squeeze the stacked pair:\n%s", half)
	}
}

// The eye swap is the decoder's: it works with every encoder and is never a
// filter.
func TestSwapWorksWithEitherEncoder(t *testing.T) {
	soft, _, code := capture(t, "--dry-run", "--encoder", "software", "--swap-lr",
		"--input", "/in/a.iso", "--output", "/out/a.mkv", "--temp", "/tmp/w")
	if code != 0 {
		t.Fatalf("--swap-lr with software encoding should work, exit = %d", code)
	}
	if !strings.Contains(soft, "-swap") || !strings.Contains(soft, "x264 --demuxer y4m") {
		t.Errorf("--swap-lr should be decoded swapped and encoded by x264 itself:\n%s", soft)
	}
	out, _, code := capture(t, "--dry-run", "--encoder", "nvenc", "--swap-lr",
		"--input", "/in/a.iso", "--output", "/out/a.mkv", "--temp", "/tmp/w")
	if code != 0 {
		t.Fatalf("--swap-lr with nvenc should work, exit = %d", code)
	}
	if !strings.Contains(out, "-swap") || strings.Contains(out, "hstack") {
		t.Errorf("the swap should be the decoder's, not a filter:\n%s", out)
	}
}

// Bad configuration exits 2 with usage, not a panic or a partial run.
func TestInvalidOptionsExitTwo(t *testing.T) {
	cases := [][]string{
		{"--dry-run", "--encoder", "x264", "--output", "/out/a.mkv"},                         // no input
		{"--dry-run", "--encoder", "x264", "--input", "/in/a.iso"},                           // no output
		{"--dry-run", "--encoder", "x264", "--input", "/in/a.iso", "--output", "/out/a.mp4"}, // not mkv
		{"--dry-run", "--encoder", "x264", "--input", "/in/a.iso", "--output", "/out/a.mkv", "--crf", "99"},
		{"--dry-run", "--encoder", "x264", "--input", "/in/a.iso", "--output", "/out/a.mkv", "--layout", "sbs"},
		{"--encoder", "nonsense"},
	}
	for _, argv := range cases {
		if _, _, code := capture(t, argv...); code != 2 {
			t.Errorf("%v: exit = %d, want 2", argv, code)
		}
	}
}

// --check is a preflight, so a missing tool must be a non-zero exit a script
// can branch on, and the output must name what is missing.
func TestCheckReportsAndExitsNonZeroWhenIncomplete(t *testing.T) {
	out, _, code := capture(t, "--check")
	if !strings.Contains(out, "platform:") {
		t.Errorf("check should report the platform, got:\n%s", out)
	}
	if strings.Contains(out, "MISSING") && code == 0 {
		t.Error("exit must be non-zero while tools are missing")
	}
	if !strings.Contains(out, "MISSING") && code != 0 {
		t.Error("exit must be zero when every tool is present")
	}
}

func TestUnknownDemuxerIsRefused(t *testing.T) {
	_, errOut, code := capture(t, "--dry-run", "--demuxer", "ffmpeg", "--input", "/in/a.iso", "--output", "/out/a.mkv")
	if code != 2 || !strings.Contains(errOut, "demuxer") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

// A built-in remux runs nothing external, and --check says so.
func TestCheckForABuiltinRemux(t *testing.T) {
	out, _, code := capture(t, "--check", "--remux")
	if code != 0 || !strings.Contains(out, "no external tools needed") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestUnknownMuxerIsRefused(t *testing.T) {
	_, errOut, code := capture(t, "--dry-run", "--muxer", "ffmpeg", "--input", "/in/a.iso", "--output", "/out/a.mkv")
	if code != 2 || !strings.Contains(errOut, "muxer") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}
