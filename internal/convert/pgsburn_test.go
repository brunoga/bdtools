package convert

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/mvc"
)

// feedSet gives a display set to a burner, segment by segment, at pts.
func feedSet(b *pgsBurner, pts int64, ds []byte) {
	for len(ds) >= 3 {
		n := 3 + int(binary.BigEndian.Uint16(ds[1:]))
		b.feed(pts, ds[:n])
		ds = ds[n:]
	}
}

// A subtitle burned in shows from its display set's time, in each eye
// apart by the offset (the left eye's to the right), and goes with the
// set that clears it; at 10 bits too.
func TestBurnSubtitles(t *testing.T) {
	const w, h = 1920, 1080
	const x, y, ow, oh = 900, 950, 40, 10
	pix := make([]byte, ow*oh)
	for i := range pix {
		pix[i] = 1 // opaque white (Y 235)
	}
	for _, depth := range []int{8, 10} {
		b := newPGSBurner()
		feedSet(b, 90000, displaySet(x, y, ow, oh, pix)) // at 1 s
		feedSet(b, 180000, clearSet())                   // an empty composition at 2 s
		bps := 1
		if depth > 8 {
			bps = 2
		}
		newPic := func() *gpu.Picture {
			p := &gpu.Picture{Y: make([]byte, w*h*bps), UV: make([]byte, w*h/2*bps), Pitch: w * bps, Depth: depth}
			return p
		}
		luma := func(p *gpu.Picture, px, py int) int {
			if bps == 2 {
				return int(binary.LittleEndian.Uint16(p.Y[py*p.Pitch+2*px:])) >> 8
			}
			return int(p.Y[py*p.Pitch+px])
		}
		const off = 6
		l, r := newPic(), newPic()
		b.draw(l, r, w, h, 500*time.Millisecond, off)
		if luma(l, x+off, y) != 0 {
			t.Fatalf("%d-bit: drawn before its time", depth)
		}
		b.draw(l, r, w, h, 1500*time.Millisecond, off)
		if got := luma(l, x+off, y); got != 235 {
			t.Errorf("%d-bit: left eye at x+offset is %d, want 235", depth, got)
		}
		if got := luma(l, x+off-1, y); got != 0 {
			t.Errorf("%d-bit: left eye before x+offset is %d, want 0", depth, got)
		}
		if got := luma(r, x-off, y); got != 235 {
			t.Errorf("%d-bit: right eye at x-offset is %d, want 235", depth, got)
		}
		if got := luma(r, x-off+ow, y); got != 0 {
			t.Errorf("%d-bit: right eye past the object is %d, want 0", depth, got)
		}
		l2, r2 := newPic(), newPic()
		b.draw(l2, r2, w, h, 2500*time.Millisecond, off)
		if luma(l2, x+off, y) != 0 || luma(r2, x-off, y) != 0 {
			t.Errorf("%d-bit: still drawn after the set that clears it", depth)
		}
	}
}

// A subtitle burned into a frame holding both eyes lands in each eye's
// part: the right half of a side-by-side frame, the lower half of an
// over-under one, squeezed with a half layout.
func TestBurnFrameEyes(t *testing.T) {
	const x, y, ow, oh = 900, 950, 40, 10
	pix := make([]byte, ow*oh)
	for i := range pix {
		pix[i] = 1
	}
	for _, tc := range []struct {
		layout         Layout
		fw, fh         int
		lx, ly, rx, ry int
	}{
		{LayoutFullSBS, 3840, 1080, x, y, 1920 + x, y},
		{LayoutHalfSBS, 1920, 1080, x / 2, y, 960 + x/2, y},
		{LayoutFullOU, 1920, 2160, x, y, x, 1080 + y},
		{LayoutHalfOU, 1920, 1080, x, y / 2, x, 540 + y/2},
	} {
		r := NewRunner(CurrentGOOS, DefaultOptions(), nil)
		r.burn = newPGSBurner()
		feedSet(r.burn, 0, displaySet(x, y, ow, oh, pix))
		r.timeline = func(pts int64) time.Duration { return time.Second }
		dst := &gpu.Picture{Y: make([]byte, tc.fw*tc.fh), UV: make([]byte, tc.fw*tc.fh/2), Pitch: tc.fw, Depth: 8}
		eye := testFrame(1920, 1080, 0)
		r.burnFrame(dst, picture{stereo: &mvc.StereoFrame{Base: eye, Dependent: eye}}, true, tc.layout)
		if dst.Y[tc.ly*tc.fw+tc.lx] != 235 || dst.Y[tc.ry*tc.fw+tc.rx] != 235 {
			t.Errorf("%s: left eye %d at (%d,%d), right eye %d at (%d,%d), want 235", tc.layout,
				dst.Y[tc.ly*tc.fw+tc.lx], tc.lx, tc.ly, dst.Y[tc.ry*tc.fw+tc.rx], tc.rx, tc.ry)
		}
	}
}
