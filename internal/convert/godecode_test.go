package convert

import (
	"errors"
	"testing"

	"github.com/brunoga/bdtools/internal/gpu"
)

// fakeDecoder gives a picture for every access unit, or fails at one.
type fakeDecoder struct {
	failAt  int
	n       int
	picture func(*gpu.DecodedPicture) error
	got     []int64
}

func (f *fakeDecoder) Decode(_ []byte, pts int64) error {
	f.n++
	if f.n == f.failAt {
		return errors.New("malfunction")
	}
	f.got = append(f.got, pts)
	return f.picture(&gpu.DecodedPicture{PTS: pts})
}
func (f *fakeDecoder) Flush() error { return nil }
func (f *fakeDecoder) Close() error { return nil }

// A GPU decoder failing before its first picture is replaced by the
// decoder here, which gets every access unit; failing after, it fails.
func TestGPUFallback(t *testing.T) {
	var shown []int64
	picture := func(p *gpu.DecodedPicture) error { shown = append(shown, p.PTS); return nil }
	soft := &fakeDecoder{}
	g := &gpuFallback{report: func(string, ...any) {}}
	wrap := func(p *gpu.DecodedPicture) error { g.shown = true; return picture(p) }
	g.dec = &fakeDecoder{failAt: 1, picture: wrap}
	g.open = func() (gpu.Decoder, error) { soft.picture = wrap; return soft, nil }
	for pts := range int64(3) {
		if err := g.Decode([]byte{1}, pts); err != nil {
			t.Fatal(err)
		}
	}
	if len(shown) != 3 || len(soft.got) != 3 {
		t.Errorf("shown %v, the decoder here got %v", shown, soft.got)
	}

	g = &gpuFallback{report: func(string, ...any) {}}
	wrap = func(p *gpu.DecodedPicture) error { g.shown = true; return nil }
	g.dec = &fakeDecoder{failAt: 2, picture: wrap}
	g.open = func() (gpu.Decoder, error) { return &fakeDecoder{picture: wrap}, nil }
	if err := g.Decode(nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := g.Decode(nil, 1); err == nil {
		t.Error("a failure after the first picture taken over")
	}
}

// auOf is an access unit of the NAL units given (their first bytes).
func auOf(nals ...[]byte) []byte {
	var au []byte
	for _, n := range nals {
		au = append(au, 0, 0, 0, 1)
		au = append(au, n...)
	}
	return au
}

// recDecoder records what it is fed and gives a picture for every access
// unit, or fails at one.
type recDecoder struct {
	fakeDecoder
	aus [][]byte
}

func (r *recDecoder) Decode(au []byte, pts int64) error {
	r.aus = append(r.aus, au)
	return r.fakeDecoder.Decode(au, pts)
}

// A GPU decoder failing mid-stream (H.264, HEVC) is replaced by the
// decoder here from the last IDR, every parameter set before it, the
// pictures already out not given again.
func TestGPUFallbackMidStream(t *testing.T) {
	for _, c := range []struct {
		codec            gpu.VideoCodec
		sps, pps, idr, p []byte
	}{
		{gpu.DecodeH264, []byte{0x67, 1}, []byte{0x68, 2}, []byte{0x65, 3}, []byte{0x41, 4}},
		{gpu.DecodeHEVC, []byte{0x42, 1, 1}, []byte{0x44, 1, 2}, []byte{0x26, 1, 3}, []byte{0x02, 1, 4}},
	} {
		for _, failAt := range []int{3, 9, 12} {
			var shown []int64
			picture := func(p *gpu.DecodedPicture) error { shown = append(shown, p.PTS); return nil }
			soft := &recDecoder{}
			var reports int
			g, err := withFallback(c.codec, picture,
				func(p func(*gpu.DecodedPicture) error) (gpu.Decoder, error) {
					return &fakeDecoder{failAt: failAt, picture: p}, nil
				},
				func(p func(*gpu.DecodedPicture) error) (gpu.Decoder, error) { soft.picture = p; return soft, nil },
				func(string, ...any) { reports++ })
			if err != nil {
				t.Fatal(err)
			}
			// an IDR every 5 access units, the parameter sets with the first
			for pts := range int64(14) {
				au := auOf(c.p)
				switch {
				case pts == 0:
					au = auOf(c.sps, c.pps, c.idr)
				case pts%5 == 0:
					au = auOf(c.idr)
				}
				if err := g.Decode(au, pts); err != nil {
					t.Fatal(err)
				}
			}
			if len(shown) != 14 {
				t.Fatalf("%v fail at %d: shown %v", c.codec, failAt, shown)
			}
			for i, v := range shown {
				if v != int64(i) {
					t.Fatalf("%v fail at %d: shown %v", c.codec, failAt, shown)
				}
			}
			idr := int64((failAt - 1) / 5 * 5)
			if reports != 1 || len(soft.aus) == 0 || soft.got[0] != idr {
				t.Fatalf("%v fail at %d: %d reports, the decoder here got %v", c.codec, failAt, reports, soft.got)
			}
			if want := auOf(c.sps, c.pps, c.idr); idr > 0 && string(soft.aus[0]) != string(want) {
				t.Errorf("%v fail at %d: restarted with % x, want % x", c.codec, failAt, soft.aus[0], want)
			}
		}
	}
}
