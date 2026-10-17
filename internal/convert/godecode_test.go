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
