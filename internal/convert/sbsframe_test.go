package convert

import (
	"testing"
	"time"

	"github.com/brunoga/bdtools/mvc"
)

func TestSideBySideFrame(t *testing.T) {
	eye := func(v byte, stride int) *mvc.Frame {
		f := &mvc.Frame{Width: 4, Height: 2, StrideY: stride, StrideC: stride / 2,
			Y: make([]byte, stride*2), Cb: make([]byte, stride/2), Cr: make([]byte, stride/2)}
		for i := range f.Y {
			f.Y[i] = v
		}
		for i := range f.Cb {
			f.Cb[i], f.Cr[i] = v+1, v+2
		}
		return f
	}
	f := sideBySideFrame(eye(10, 6), eye(20, 4), 90000)
	if f.Width != 8 || f.Height != 2 || f.StrideY != 8 || f.StrideC != 4 || f.PTS != 90000 {
		t.Fatalf("frame %dx%d strides %d %d pts %d", f.Width, f.Height, f.StrideY, f.StrideC, f.PTS)
	}
	for y := range 2 {
		for x := range 8 {
			want := byte(10)
			if x >= 4 {
				want = 20
			}
			if got := f.Y[y*f.StrideY+x]; got != want {
				t.Fatalf("Y[%d,%d] = %d, want %d", x, y, got, want)
			}
		}
	}
	for x := range 4 {
		want := byte(11)
		if x >= 2 {
			want = 21
		}
		if f.Cb[x] != want || f.Cr[x] != want+1 {
			t.Fatalf("chroma[%d] = %d %d, want %d %d", x, f.Cb[x], f.Cr[x], want, want+1)
		}
	}
}

func TestPictureTime(t *testing.T) {
	// Without a timeline, time runs from the first picture.
	if got := pictureTime(nil, 90000, 90000*3); got != 2*time.Second {
		t.Errorf("no timeline: %v, want 2s", got)
	}
	// With one, a tagged timestamp (clip 1, at its own clock's 90000) is
	// where the demuxer's timeline puts it, not hours away.
	tl := func(pts int64) time.Duration {
		if pts>>40 != 1 || pts&(1<<40-1) != 90000 {
			t.Errorf("timeline given %#x", pts)
		}
		return 10 * time.Minute
	}
	if got := pictureTime(tl, 0, 1<<40|90000); got != 10*time.Minute {
		t.Errorf("timeline: %v, want 10m", got)
	}
}
