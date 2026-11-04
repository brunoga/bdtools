package convert

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/brunoga/bdtools/internal/hdr"
	"github.com/brunoga/bdtools/mvc"
)

func TestPictureHolesMissing(t *testing.T) {
	const frame = 40 * time.Millisecond
	var h pictureHoles
	for _, c := range []struct {
		at   time.Duration
		want int
	}{
		{0, 0},                                              // the first picture
		{40 * time.Millisecond, 0},                          // in step
		{100 * time.Millisecond, 0},                         // one and a half pictures on: unevenness
		{160 * time.Millisecond, 0},                         // the same again
		{280 * time.Millisecond, 2},                         // three pictures on: two lost
		{280*time.Millisecond + 3*time.Second, 74},          // three seconds on: 74 lost
		{320*time.Millisecond + 3*time.Second + maxHole, 0}, // too long to be a loss
		{0, 0}, // backwards
	} {
		if got := h.missing(c.at, frame); got != c.want {
			t.Errorf("missing(%v) = %d, want %d", c.at, got, c.want)
		}
	}
}

// holedPictures gives n pictures at 25 a second, leaving out those from
// lost on for the next k.
type holedPictures struct{ n, lost, k int }

func (s *holedPictures) run(each func(picture) error) error {
	f := &mvc.Frame{Width: 16, Height: 16}
	for i := range s.n {
		if i >= s.lost && i < s.lost+s.k {
			continue
		}
		if err := each(picture{stereo: &mvc.StereoFrame{Base: f, Dependent: f}, pts: int64(i) * 3600}); err != nil {
			return err
		}
	}
	return nil
}

func (s *holedPictures) frameRate() (int, int) { return 25, 1 }
func (s *holedPictures) errors() int           { return 0 }
func (s *holedPictures) dependentFrames() int  { return s.n }
func (s *holedPictures) hdrStatic() hdr.Static { return hdr.Static{} }

// A source that lost pictures is encoded with the picture after the hole
// standing in for them, each at the timestamp it would have had: the
// picture keeps the source's timeline, and the sound beside it its step.
func TestEncodeFillsHoles(t *testing.T) {
	var lines []string
	r := NewRunner(CurrentGOOS, opts("linux", func(o *Options) { o.Input = bluray("disc.iso") }), func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
	w, err := openWork(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sink := &fakeSink{}
	r.work, r.sink = w, sink
	r.timeline = ticks90k
	if _, err := r.decodeAndEncode(context.Background(), &holedPictures{n: 20, lost: 8, k: 5}, nil); err != nil {
		t.Fatal(err)
	}
	if len(sink.pts) != 20 {
		t.Fatalf("%d pictures encoded, want 20", len(sink.pts))
	}
	for i, p := range sink.pts {
		if p != int64(i)*3600 {
			t.Fatalf("picture %d at %d, want %d", i, p, i*3600)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "lost 5 pictures at 320ms") {
		t.Errorf("no word of the hole:\n%s", strings.Join(lines, "\n"))
	}
}
