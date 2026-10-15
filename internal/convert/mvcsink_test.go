package convert

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/brunoga/bdtools/mvc"
)

// A conversion that stops while the right eye's coding is waiting for
// the left eye's next access unit aborts the sink; the abort is not a
// failure of the sink's own, so it reports none and the stop's reason
// reaches the user.
func TestMVCSinkAbortReportsNoFailureOfItsOwn(t *testing.T) {
	o := DefaultOptions()
	o.Encoder, o.Codec, o.Layout, o.CRF = EncoderBuiltin, CodecH264, LayoutMVC, 25
	s := &mvcSink{r: NewRunner(CurrentGOOS, o, nil), ctx: context.Background(), builtin: true}
	const w, h = 64, 48
	frame := func() *mvc.Frame {
		return &mvc.Frame{Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4),
			StrideY: w, StrideC: w / 2, Width: w, Height: h}
	}
	p := picture{stereo: &mvc.StereoFrame{Base: frame(), Dependent: frame()}}
	if err := s.start(filepath.Join(t.TempDir(), "video-0000.264"), p, 24, 1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.put(p); err != nil {
			t.Fatal(err)
		}
	}
	// A disc's last pictures come without a dependent view: they are 2D.
	if err := s.put(picture{stereo: &mvc.StereoFrame{Base: frame()}}); err != nil {
		t.Fatalf("a picture without a dependent view: %v", err)
	}
	if err := s.abort(); err != nil {
		t.Errorf("abort reported %q, want nothing: the sink did not fail, the conversion did", err)
	}
	if s.failed != nil && !errors.Is(s.failed, errAborted) {
		t.Errorf("the right eye's coding failed with %v", s.failed)
	}
}
