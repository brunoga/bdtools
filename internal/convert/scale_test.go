package convert

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/brunoga/bdtools/internal/gpu"
)

// testPicture is a w x h NV12 (depth 8) or P010 picture whose samples
// come from fy (luma) and fc (chroma, channel 0 Cb, 1 Cr) by position.
func testPicture(w, h, depth int, fy func(x, y int) int, fc func(x, y, c int) int) *gpu.DecodedPicture {
	bps := 1
	if depth > 8 {
		bps = 2
	}
	p := &gpu.DecodedPicture{Width: w, Height: h, Depth: depth, Pitch: w * bps,
		Y: make([]byte, w*h*bps), UV: make([]byte, w*h/2*bps)}
	put := func(b []byte, i, v int) {
		if bps == 1 {
			b[i] = byte(v)
		} else {
			binary.LittleEndian.PutUint16(b[2*i:], uint16(v<<6)) //nolint:gosec // a sample
		}
	}
	for y := range h {
		for x := range w {
			put(p.Y, y*w+x, fy(x, y))
		}
	}
	for y := range h / 2 {
		for x := range w / 2 {
			put(p.UV, y*w+2*x, fc(x, y, 0))
			put(p.UV, y*w+2*x+1, fc(x, y, 1))
		}
	}
	return p
}

func at(p *gpu.DecodedPicture, plane []byte, i int) int {
	if p.Depth > 8 {
		return int(binary.LittleEndian.Uint16(plane[2*i:]) >> 6)
	}
	return int(plane[i])
}

// A flat picture stays flat, at every ratio, up or down.
func TestScaleKeepsFlat(t *testing.T) {
	for _, depth := range []int{8, 10} {
		for _, sz := range [][4]int{{64, 48, 32, 24}, {64, 48, 30, 18}, {64, 48, 128, 96}, {96, 64, 64, 36}, {50, 40, 50, 40}} {
			src := testPicture(sz[0], sz[1], depth, func(x, y int) int { return 100 << (depth - 8) },
				func(x, y, c int) int { return (60 + 80*c) << (depth - 8) })
			out := newScaler(sz[0], sz[1], sz[2], sz[3]).scale(src)
			if out.Width != sz[2] || out.Height != sz[3] {
				t.Fatalf("%v: %dx%d out", sz, out.Width, out.Height)
			}
			for i := range sz[2] * sz[3] {
				if v := at(out, out.Y, i); v != 100<<(depth-8) {
					t.Fatalf("%d-bit %v: luma %d at %d", depth, sz, v, i)
				}
			}
			for i := range sz[2] * sz[3] / 2 {
				if v, want := at(out, out.UV, i), (60+80*(i%2))<<(depth-8); v != want {
					t.Fatalf("%d-bit %v: chroma %d at %d, want %d", depth, sz, v, i, want)
				}
			}
		}
	}
}

// A linear ramp stays a ramp (away from the edges, where the filter
// repeats the border): luma by centred positions, chroma by 4:2:0's, the
// columns sited on the even luma column and the rows between two luma
// rows. A chroma plane scaled as if centred would be off by a quarter of
// a chroma sample horizontally.
func TestScaleRampsAndSiting(t *testing.T) {
	const w, h, ow, oh = 256, 128, 96, 48
	src := testPicture(w, h, 10,
		func(x, y int) int { return 100 + 2*x + y },
		func(x, y, c int) int {
			// Cb by the chroma column's luma position, Cr by the row's.
			if c == 0 {
				return 100 + 2*(2*x)
			}
			return 100 + 4*(2*y) + 2
		})
	out := newScaler(w, h, ow, oh).scale(src)
	r := float64(w) / ow
	for y := 8; y < oh-8; y++ {
		for x := 8; x < ow-8; x++ {
			sx, sy := (float64(x)+0.5)*r-0.5, (float64(y)+0.5)*r-0.5
			if got, want := float64(at(out, out.Y, y*ow+x)), 100+2*sx+sy; math.Abs(got-want) > 1 {
				t.Fatalf("luma (%d,%d) = %v, want %.1f", x, y, got, want)
			}
		}
	}
	for y := 4; y < oh/2-4; y++ {
		for x := 4; x < ow/2-4; x++ {
			// The output chroma sample's luma position, in the source.
			lx := (float64(2*x)+0.5)*r - 0.5
			ly := (float64(2*y)+1)*r - 0.5
			if got, want := float64(at(out, out.UV, y*ow+2*x)), 100+2*lx; math.Abs(got-want) > 1 {
				t.Fatalf("Cb (%d,%d) = %v, want %.1f", x, y, got, want)
			}
			// Cr's ramp is 4 a luma row from the chroma row's position,
			// which is half a row below its first luma row.
			if got, want := float64(at(out, out.UV, y*ow+2*x+1)), 100+4*(ly-0.5)+2; math.Abs(got-want) > 1 {
				t.Fatalf("Cr (%d,%d) = %v, want %.1f", x, y, got, want)
			}
		}
	}
}

func TestParseCropAndScale(t *testing.T) {
	if c, err := ParseCrop("140:140:0:0"); err != nil || c != (Crop{Top: 140, Bottom: 140}) {
		t.Errorf("ParseCrop: %v, %v", c, err)
	}
	for _, bad := range []string{"1:2:3", "141:140:0:0", "a:b:c:d", "-2:0:0:0"} {
		if _, err := ParseCrop(bad); err == nil {
			t.Errorf("ParseCrop(%q) took it", bad)
		}
	}
	if s, err := ParseScale("1080p"); err != nil || s != (Scale{1920, 1080}) {
		t.Errorf("ParseScale: %v, %v", s, err)
	}
	if s, err := ParseScale("1280x536"); err != nil || s != (Scale{1280, 536}) {
		t.Errorf("ParseScale: %v, %v", s, err)
	}
	if _, err := ParseScale("big"); err == nil {
		t.Error("ParseScale took big")
	}
	for _, c := range []struct {
		box          Scale
		w, h, ow, oh int
	}{
		{Scale{1920, 1080}, 3840, 2160, 1920, 1080},
		{Scale{1920, 1080}, 3840, 1600, 1920, 800},
		{Scale{1920, 1080}, 1920, 1080, 1920, 1080}, // fits: as it is
		{Scale{1920, 1080}, 1280, 720, 1280, 720},   // never larger
		{Scale{1280, 720}, 1920, 804, 1280, 536},
		{Scale{1920, 1080}, 1440, 1080, 1440, 1080},
	} {
		if ow, oh := c.box.fit(c.w, c.h); ow != c.ow || oh != c.oh {
			t.Errorf("%v fitting %dx%d: %dx%d, want %dx%d", c.box, c.w, c.h, ow, oh, c.ow, c.oh)
		}
	}
}

// Cropping takes the edges off; a picture of the size the box gives is
// cropped only.
func TestPicTransformCrops(t *testing.T) {
	src := testPicture(64, 48, 8, func(x, y int) int { return x + 2*y }, func(x, y, c int) int { return 50 + x + 3*y + 100*c })
	tr, err := newPicTransform(Crop{Top: 4, Bottom: 8, Left: 6, Right: 2}, Scale{}, 64, 48)
	if err != nil {
		t.Fatal(err)
	}
	q, err := tr.apply(picture{gpu: src})
	if err != nil {
		t.Fatal(err)
	}
	out := q.gpu
	if out.Width != 56 || out.Height != 36 {
		t.Fatalf("%dx%d", out.Width, out.Height)
	}
	for y := range 36 {
		for x := range 56 {
			if v := at(out, out.Y[y*out.Pitch:], x); v != (x+6)+2*(y+4) {
				t.Fatalf("luma (%d,%d) = %d", x, y, v)
			}
		}
	}
	for y := range 18 {
		for x := range 28 {
			row := out.UV[y*out.Pitch:]
			if v := at(out, row, 2*x+1); v != 150+(x+3)+3*(y+2) {
				t.Fatalf("Cr (%d,%d) = %d", x, y, v)
			}
		}
	}
	if _, err := newPicTransform(Crop{Top: 30, Bottom: 30}, Scale{}, 64, 48); err == nil {
		t.Error("a crop of everything taken")
	}
}

// A cropped picture's subtitles: the plane cut as the picture is (at the
// plane's scale: a UHD picture's crop is halved on its 1920x1080 plane),
// and a subtitle in the bottom bar moved up into the picture.
func TestPGSCrop(t *testing.T) {
	ds := func(wx, wy, ox, oy int) []byte {
		pcs := []byte{0x07, 0x80, 0x04, 0x38, 0x10, 0, 1, 0x80, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint16(pcs[15:], uint16(ox)) //nolint:gosec // test
		binary.BigEndian.PutUint16(pcs[17:], uint16(oy)) //nolint:gosec // test
		wds := []byte{1, 0, 0, 0, 0, 0, 0x03, 0x84, 0, 100}
		binary.BigEndian.PutUint16(wds[2:], uint16(wx)) //nolint:gosec // test
		binary.BigEndian.PutUint16(wds[4:], uint16(wy)) //nolint:gosec // test
		return appendSegment(appendSegment(appendSegment(nil, segPCS, pcs), segWDS, wds), segEND, nil)
	}
	for _, c := range []struct {
		videoW, videoH int
		crop           Crop
	}{
		{1920, 1080, Crop{Top: 140, Bottom: 140}},
		{3840, 2160, Crop{Top: 280, Bottom: 280}},
	} {
		p := newPGSCrop(nil, c.crop, c.videoW, c.videoH)
		out, err := p.rewrite(ds(500, 950, 510, 960))
		if err != nil {
			t.Fatal(err)
		}
		types, payloads, err := segments(out)
		if err != nil || len(types) != 3 {
			t.Fatalf("%v: %v", types, err)
		}
		pcs, wds := payloads[0], payloads[1]
		if w, h := binary.BigEndian.Uint16(pcs), binary.BigEndian.Uint16(pcs[2:]); w != 1920 || h != 800 {
			t.Errorf("%dx%d crop %v: plane %dx%d, want 1920x800", c.videoW, c.videoH, c.crop, w, h)
		}
		// The window (100 high, at 950) is in the bottom bar: it comes up
		// to the plane's bottom edge, and the object with it.
		if x, y := binary.BigEndian.Uint16(wds[2:]), binary.BigEndian.Uint16(wds[4:]); x != 500 || y != 700 {
			t.Errorf("window at %d,%d, want 500,700", x, y)
		}
		if x, y := binary.BigEndian.Uint16(pcs[15:]), binary.BigEndian.Uint16(pcs[17:]); x != 510 || y != 710 {
			t.Errorf("object at %d,%d, want 510,710", x, y)
		}
		// One inside the picture moves by the cut alone.
		out, _ = p.rewrite(ds(500, 600, 520, 610))
		_, payloads, _ = segments(out)
		if y := binary.BigEndian.Uint16(payloads[1][4:]); y != 460 {
			t.Errorf("window at %d, want 460", y)
		}
		if y := binary.BigEndian.Uint16(payloads[0][17:]); y != 470 {
			t.Errorf("object at %d, want 470", y)
		}
	}
}
