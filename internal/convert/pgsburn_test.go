package convert

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/gpu"
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
