package convert

import (
	"bytes"
	"os/exec"
	"testing"
)

// The access unit splitter cuts an HEVC stream, written in pieces of any
// size, into its pictures: as many as the stream has, each starting at its
// access unit delimiter, together the whole stream.
func TestAUSplitter(t *testing.T) {
	if _, err := LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	es, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=1", //nolint:gosec // test
		"-c:v", "libx265", "-x265-params", "log-level=error:aud=1:bframes=3", "-f", "hevc", "-").Output()
	if err != nil {
		t.Skipf("making the stream: %v", err)
	}
	for _, size := range []int{1, 3, 7, 100, 4096, len(es)} {
		var a auSplitter
		for b := es; len(b) > 0; {
			n := min(size, len(b))
			if _, err := a.Write(b[:n]); err != nil {
				t.Fatal(err)
			}
			b = b[n:]
		}
		a.close()
		aus := a.take()
		if len(aus) != 24 {
			t.Fatalf("writes of %d: %d access units, want 24", size, len(aus))
		}
		for i, au := range aus {
			if !bytes.HasPrefix(au, []byte{0, 0, 0, 1, 35 << 1}) && !bytes.HasPrefix(au, []byte{0, 0, 1, 35 << 1}) {
				t.Fatalf("writes of %d: access unit %d starts % x", size, i, au[:min(8, len(au))])
			}
		}
		if !bytes.Equal(bytes.Join(aus, nil), es) {
			t.Fatalf("writes of %d: the access units are not the stream", size)
		}
	}
}
