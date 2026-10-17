package convert

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brunoga/bdtools/internal/dovi"
	"github.com/brunoga/bdtools/internal/hdr"
	"github.com/brunoga/bdtools/internal/mkv"
)

func TestPQ(t *testing.T) {
	for _, l := range []float64{0.01, 1, 100, 203, 1000, 4000, 10000} {
		if got := pqToNits(nitsToPQ(l)); math.Abs(got-l) > l*1e-9+1e-9 {
			t.Errorf("%v cd/m² round trips to %v", l, got)
		}
	}
	if e := nitsToPQ(100); math.Abs(e-0.5081) > 0.0005 {
		t.Errorf("PQ(100) = %v, want 0.5081", e)
	}
}

// BT.2390's curve: the identity below its knee, the source's peak to the
// target's, rising throughout.
func TestEETF(t *testing.T) {
	from, to := nitsToPQ(1000), nitsToPQ(sdrWhite)
	if got := eetf(from, from, to); math.Abs(got-to) > 1e-9 {
		t.Errorf("the peak goes to %v, want %v", got, to)
	}
	if e := nitsToPQ(20); eetf(e, from, to) != e {
		t.Error("a dark value moved")
	}
	prev := -1.0
	for i := range 1001 {
		v := eetf(float64(i)/1000*from, from, to)
		if v < prev {
			t.Fatalf("falls at %d", i)
		}
		prev = v
	}
	if e := nitsToPQ(150); eetf(e, nitsToPQ(150), to) != e {
		t.Error("a source within the target is not left as it is")
	}
}

// pqPicture is a P010 picture of one BT.2020 colour given as linear
// light (cd/m²) per component.
func pqPicture(w, h int, r, g, b float64) *pictureColour {
	rp, gp, bp := nitsToPQ(r), nitsToPQ(g), nitsToPQ(b)
	y := 0.2627*rp + 0.6780*gp + 0.0593*bp
	cb, cr := (bp-y)/1.8814, (rp-y)/1.4746
	code := func(v, off, rng float64) int { return int(math.Round(64*off/16 + rng*v)) }
	return &pictureColour{
		Y: code(y, 16, 876), Cb: code(cb, 128, 896), Cr: code(cr, 128, 896), w: w, h: h}
}

type pictureColour struct {
	Y, Cb, Cr int
	w, h      int
}

func (c *pictureColour) picture() picture {
	p := testPicture(c.w, c.h, 10, func(x, y int) int { return c.Y }, func(x, y, ch int) int {
		if ch == 0 {
			return c.Cb
		}
		return c.Cr
	})
	p.Color.Transfer = 16
	return picture{gpu: p}
}

// A grey stays grey; HDR's reference white comes out near SDR's, a darker
// grey as it was, and a highlight below white.
func TestToneMapGreys(t *testing.T) {
	tm := newToneMapper(16, hdr.Static{Light: &hdr.LightLevel{MaxCLL: 1000}})
	for _, c := range []struct {
		nits     float64
		lo, hi   int // 10-bit luma out
		describe string
	}{
		{20, 4 * (16 + int(219*math.Pow(20.0/203, 1/2.4)) - 2), 4 * (16 + int(219*math.Pow(20.0/203, 1/2.4)) + 2), "dark grey, as it was"},
		// Just past the knee for a 1000 cd/m² peak: a little under white.
		{203, 4 * 205, 4 * 225, "reference white, a little under SDR white"},
		{1000, 4 * 234, 4 * 236, "the peak, white"},
	} {
		src := pqPicture(64, 32, c.nits, c.nits, c.nits).picture()
		tm.pictureMeta(src)
		out := tm.apply(src.gpu)
		y, cb, cr := at(out, out.Y, 33*1+5), at(out, out.UV, 2*7), at(out, out.UV, 2*7+1)
		if y < c.lo || y > c.hi || cb < 510 || cb > 514 || cr < 510 || cr > 514 {
			t.Errorf("%s (%v cd/m²): Y %d (want %d to %d), Cb %d, Cr %d", c.describe, c.nits, y, c.lo, c.hi, cb, cr)
		}
		if out.Color.Transfer != 1 || out.Color.Primaries != 1 || out.Color.Matrix != 1 {
			t.Errorf("colour %+v, want BT.709", out.Color)
		}
	}
}

// A BT.2020 green outside BT.709 comes out green, within range, not
// clipped to some other hue.
func TestToneMapGamut(t *testing.T) {
	tm := newToneMapper(16, hdr.Static{})
	src := pqPicture(64, 32, 1, 150, 1).picture()
	out := tm.apply(src.gpu)
	y, cb, cr := at(out, out.Y, 70), at(out, out.UV, 20), at(out, out.UV, 21)
	if y < 64 || y > 940 || cb >= 512 || cr >= 512 || cb < 64 || cr < 64 {
		t.Errorf("green: Y %d Cb %d Cr %d", y, cb, cr)
	}
}

// HDR10+'s scene peak, and the curve following a picture's metadata.
func TestHDR10PlusPeak(t *testing.T) {
	// application_version 1, one window, a target display of 400, no
	// actual peak luminance, maxscl 4000.0, 6000.0 and 2500.0 cd/m².
	var bits []bool
	put := func(v uint64, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, v>>uint(i)&1 == 1) //nolint:gosec // test
		}
	}
	put(1, 8)
	put(1, 2)
	put(400, 27)
	put(0, 1)
	put(40000, 17)
	put(60000, 17)
	put(25000, 17)
	payload := []byte{0xb5, 0x00, 0x3c, 0x00, 0x01, 0x04}
	for i := 0; i < len(bits); i += 8 {
		var b byte
		for k := range 8 {
			if i+k < len(bits) && bits[i+k] {
				b |= 0x80 >> k
			}
		}
		payload = append(payload, b)
	}
	n, ok := hdr.HDR10PlusPeak(payload)
	if !ok || n != 6000 {
		t.Fatalf("peak %v %v, want 6000", n, ok)
	}
	tm := newToneMapper(16, hdr.Static{Light: &hdr.LightLevel{MaxCLL: 1000}})
	tm.pictureMeta(picture{hdr10Plus: payload})
	if tm.peak != 6000 {
		t.Errorf("curve from %v, want 6000", tm.peak)
	}
	tm.pictureMeta(picture{})
	if tm.peak != 1000 {
		t.Errorf("curve from %v without metadata, want the MaxCLL", tm.peak)
	}
}

// --tonemap on a Dolby Vision film: SDR out, BT.709 stated, and nothing
// of HDR left: no Dolby Vision configuration, RPU or enhancement layer,
// no HDR10 metadata. Skips without ffmpeg and x265 (to make the source).
func TestRunnerToneMaps(t *testing.T) {
	fs := makeFELSource(t, 10, 0)
	out := filepath.Join(t.TempDir(), "out.mkv")
	o := DefaultOptions()
	o.Input, o.Output, o.Encoder, o.Codec, o.Decoder = fs.path, out, EncoderBuiltin, CodecH265, DecoderCPU
	o.ToneMap, o.CRF = true, 30
	var lines []string
	if err := NewRunner(CurrentGOOS, o, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }).Run(t.Context()); err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
	}
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "tone mapping HDR10 (PQ) to SDR") }) {
		t.Errorf("no tone mapping reported:\n%s", strings.Join(lines, "\n"))
	}
	f, err := os.Open(out) //nolint:gosec // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rd, err := mkv.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	v := rd.Tracks[0]
	if v.Transfer != 1 || len(v.BlockAdditions) != 0 {
		t.Errorf("transfer %d, %d block addition mappings: want BT.709 and none", v.Transfer, len(v.BlockAdditions))
	}
	frames := 0
	for {
		p, err := rd.Next()
		if err != nil {
			break
		}
		frames++
		for b := p.Data; len(b) >= 4; {
			n := int(binary.BigEndian.Uint32(b))
			switch typ := b[4] >> 1 & 0x3f; typ {
			case dovi.NALRPU, dovi.NALEL:
				t.Fatalf("a Dolby Vision NAL unit (%d) in frame %d", typ, frames)
			case 39: // prefix SEI: mastering display (137), light level (144)
				if s := b[6]; s == 137 || s == 144 {
					t.Fatalf("HDR10 metadata (SEI %d) in frame %d", s, frames)
				}
			}
			b = b[4+n:]
		}
	}
	if frames == 0 {
		t.Error("no frames")
	}
}
