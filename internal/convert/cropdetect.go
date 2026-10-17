package convert

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/mkv"
	"github.com/brunoga/bdtools/m2ts"
)

// --crop auto: the black bars found by looking at the film in places. At
// cropSamples places spread over the video (by its bytes), the decoder
// the conversion uses decodes from the first keyframe on until
// cropPictures pictures have come out; each picture's bars are the rows
// and columns at its edges that are black throughout. What is black in
// every picture that shows something is cropped: a film whose shape
// changes (IMAX scenes) keeps its largest picture, and a fade to black
// does not count.

const (
	cropSamples  = 12
	cropPictures = 3
	// cropAUs is how many access units a place may take before it gives
	// up (a keyframe far off, a decoder slow to give pictures out).
	cropAUs = 240
	// cropMinPictures is how many pictures must have shown something for
	// the bars to be trusted.
	cropMinPictures = 4
)

// errEnoughPictures stops a place's decoding.
var errEnoughPictures = errors.New("enough pictures")

// sampleFeed gives the access units at a place in the video, frac (0 to 1)
// of the way through it, from a keyframe on; done releases what it holds.
type sampleFeed func(frac float64) (next func() (base, dep []byte, pts int64, err error), done func(), err error)

// detectCrop finds the black bars to crop (see above).
func (r *Runner) detectCrop(video Track, feed sampleFeed) (Crop, error) {
	quiet := r.Report
	r.Report = func(string, ...any) {}
	defer func() { r.Report = quiet }()
	var best Crop
	seen, places := 0, 0
	first := true
	for i := range cropSamples {
		next, done, err := feed((float64(i) + 0.5) / cropSamples)
		if err != nil {
			continue
		}
		src, err := r.pictures(video, next)
		if err != nil {
			done()
			return Crop{}, err
		}
		got := 0
		// A place that does not decode adds nothing: the others decide.
		_ = src.run(func(p picture) error {
			pic := p.gpu
			if pic == nil {
				pic = flattenBase(p)
			}
			if c, ok := blackBars(pic); ok {
				if first {
					best, first = c, false
				}
				best = Crop{Top: min(best.Top, c.Top), Bottom: min(best.Bottom, c.Bottom),
					Left: min(best.Left, c.Left), Right: min(best.Right, c.Right)}
				seen++
			}
			if got++; got >= cropPictures {
				return errEnoughPictures
			}
			return nil
		})
		done()
		if got > 0 {
			places++
		}
	}
	r.Report = quiet
	if seen < cropMinPictures {
		r.Report.Report("--crop auto: %d pictures of %d places showed something, too few to trust: not cropped", seen, places)
		return Crop{}, nil
	}
	// Even (4:2:0 chroma goes in pairs), up: a column of the picture off
	// shows less than a line of the bar left.
	even := func(v int) int { return (v + 1) &^ 1 }
	c := Crop{Top: even(best.Top), Bottom: even(best.Bottom), Left: even(best.Left), Right: even(best.Right)}
	if c.IsZero() {
		r.Report.Report("--crop auto: no black bars (%d pictures at %d places)", seen, places)
	} else {
		r.Report.Report("--crop auto: black bars top %d, bottom %d, left %d, right %d (%d pictures at %d places)",
			c.Top, c.Bottom, c.Left, c.Right, seen, places)
	}
	return c, nil
}

// flattenBase gives a stereo frame's base view as NV12.
func flattenBase(p picture) *gpu.DecodedPicture {
	var t picTransform
	return t.flatten(p)
}

// blackBars measures a picture's bars; ok is false for a picture that
// shows (next to) nothing, black or a fade.
func blackBars(p *gpu.DecodedPicture) (Crop, bool) {
	w, h, d := p.Width, p.Height, p.Depth
	if w < 32 || h < 32 {
		return Crop{}, false
	}
	bps := 1
	if d > 8 {
		bps = 2
	}
	sample := func(x, y int) int {
		if bps == 1 {
			return int(p.Y[y*p.Pitch+x])
		}
		return int(binary.LittleEndian.Uint16(p.Y[y*p.Pitch+2*x:]) >> 6)
	}
	black := 16 << (d - 8)
	if p.Color.FullRange {
		black = 0
	}
	// A bar is black: on average within 6 (8-bit steps) of it, and at
	// most one sample in a hundred 24 above it (the coding's ringing from
	// a bright edge next to the bar reaches into it). A disc's bars are
	// black to the code; a dark scene is not, throughout a row, at every
	// place.
	meanMax, peakMax := black+6<<(d-8), black+24<<(d-8)
	isBlack := func(n int, at func(i int) int) bool {
		sum, over := 0, 0
		for i := range n {
			v := at(i)
			if v > peakMax {
				if over++; over > n/100 {
					return false
				}
			}
			sum += v
		}
		return sum <= meanMax*n
	}
	row := func(y int) bool { return isBlack(w, func(x int) int { return sample(x, y) }) }
	col := func(x int) bool { return isBlack(h, func(y int) int { return sample(x, y) }) }
	var c Crop
	for c.Top < h && row(c.Top) {
		c.Top++
	}
	for c.Bottom < h-c.Top && row(h-1-c.Bottom) {
		c.Bottom++
	}
	if h-c.Top-c.Bottom < h/8 {
		return Crop{}, false
	}
	for c.Left < w && col(c.Left) {
		c.Left++
	}
	for c.Right < w-c.Left && col(w-1-c.Right) {
		c.Right++
	}
	if w-c.Left-c.Right < w/8 {
		return Crop{}, false
	}
	return c, true
}

// resyncer is a container reader that can go on from a byte offset.
type resyncer interface {
	Resync(r io.ReadSeeker, off int64) error
}

// keyStart is a container reader whose video starts at a keyframe.
type keyStart struct {
	frameReader
	video   uint64
	started bool
	aus     int
}

func (k *keyStart) Next() (mkv.Packet, error) {
	for {
		p, err := k.frameReader.Next()
		if err != nil || p.Track != k.video {
			return p, err
		}
		if !k.started && !p.Keyframe {
			continue
		}
		k.started = true
		if k.aus++; k.aus > cropAUs {
			return mkv.Packet{}, io.EOF
		}
		return p, nil
	}
}

// matroskaFeed samples a Matroska or MP4 source's video.
func (r *Runner) matroskaFeed(src *mkvSource, sel Selection) (sampleFeed, func(), error) {
	g := newMkvDemux(src, Selection{Base: sel.Base}, "", r.Report)
	if err := g.start(); err != nil {
		return nil, nil, err
	}
	closeAll := func() { _ = g.f.Close() }
	inner := g.r
	rs, ok := inner.frameReader.(resyncer)
	st, err := g.f.Stat()
	if !ok || err != nil {
		closeAll()
		return nil, nil, errors.New("this source cannot be read in places")
	}
	params := g.params
	feed := func(frac float64) (func() ([]byte, []byte, int64, error), func(), error) {
		if err := rs.Resync(g.f, int64(frac*float64(st.Size()))); err != nil {
			return nil, nil, err
		}
		c := *inner
		c.frameReader = &keyStart{frameReader: inner.frameReader, video: g.video}
		g.r = &c
		g.params = params
		clear(g.deps)
		return g.Next, func() {}, nil
	}
	return feed, closeAll, nil
}

// discFeed samples a disc title's video, its clips one after another by
// their sizes.
func (r *Runner) discFeed(src *goSource, sel Selection) (sampleFeed, func(), error) {
	sizes := make([]int64, len(src.clips))
	var total int64
	for i, c := range src.clips {
		f, size, err := c.open()
		if err != nil {
			return nil, nil, err
		}
		_ = f.Close()
		sizes[i] = size
		total += size
	}
	pid := uint16(sel.Base.ID) //nolint:gosec // a PID
	hevc := sel.Base.StreamID == "V_MPEGH/ISO/HEVC"
	key := func(au []byte) bool {
		switch sel.Base.StreamID {
		case "V_MPEG2":
			return bytes.Contains(au, []byte{0, 0, 1, 0xb3})
		case "V_MS/VFW/FOURCC":
			return bytes.Contains(au, []byte{0, 0, 1, 0x0f})
		}
		return hasParams(au, hevc)
	}
	feed := func(frac float64) (func() ([]byte, []byte, int64, error), func(), error) {
		at := int64(frac * float64(total))
		i := 0
		for i < len(sizes)-1 && at >= sizes[i] {
			at -= sizes[i]
			i++
		}
		f, _, err := src.clips[i].open()
		if err != nil {
			return nil, nil, err
		}
		if _, err := f.Seek(at-at%192, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		rd := m2ts.NewReader(f)
		rd.Select(pid)
		started, aus := false, 0
		next := func() ([]byte, []byte, int64, error) {
			for {
				p, err := rd.Next()
				if err != nil {
					return nil, nil, 0, io.EOF
				}
				if p.PID != pid {
					continue
				}
				if !started && !key(p.Payload) {
					continue
				}
				started = true
				if aus++; aus > cropAUs {
					return nil, nil, 0, io.EOF
				}
				return append([]byte(nil), p.Payload...), nil, p.PTS, nil
			}
		}
		return next, func() { _ = f.Close() }, nil
	}
	return feed, func() {}, nil
}

// resolveCrop settles --crop auto before the conversion, from feed.
func (r *Runner) resolveCrop(video Track, feed sampleFeed, done func(), err error) error {
	if !r.Opts.Crop.Auto {
		return nil
	}
	if err != nil {
		r.Report.Report("--crop auto: %v: not cropped", err)
		r.Opts.Crop = Crop{}
		return nil
	}
	defer done()
	c, err := r.detectCrop(video, feed)
	if err != nil {
		return fmt.Errorf("--crop auto: %w", err)
	}
	r.Opts.Crop = c
	return nil
}
