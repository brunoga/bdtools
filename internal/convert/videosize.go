package convert

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/brunoga/bdtools/internal/gpu"
)

// A target size (--video-size): the CRF that gives the video at most
// about that many bytes, found before the conversion by encoding a few seconds at
// several places with the conversion's own encoder and settings, at two
// CRFs. A place's first picture is an IDR picture, which a film has one
// of a GOP; the places' IDR pictures and the rest are counted apart,
// then put together as the film has them. The size goes with the CRF as
// an exponential (each 6 steps about halves it), which the two give.

const (
	sizePlaces   = 10
	sizePictures = 48
	sizeCRFStep  = 6
	// sizeSettle is how many pictures after a place's IDR picture are
	// not counted: an encoder spends more on them, as it settles.
	sizeSettle = 12
)

// ParseSize reads a size: bytes, or with K, M, G or T (powers of 1000;
// KiB, MiB, GiB and TiB of 1024).
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := float64(1)
	for _, u := range []struct {
		suffix string
		m      float64
	}{{"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3},
		{"T", 1e12}, {"G", 1e9}, {"M", 1e6}, {"K", 1e3}, {"B", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), u.m
			break
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%q: want a size (4.5G, 700M, 3GiB)", s)
	}
	return int64(v * mult), nil
}

// size formats a size.
func size(n float64) string {
	if n >= 1e9 {
		return fmt.Sprintf("%.2f GB", n/1e9)
	}
	return fmt.Sprintf("%.1f MB", n/1e6)
}

// auSizes splits an H.264 or HEVC elementary stream into its access
// units' sizes (a picture's first slice starts one; MVC's and MV-HEVC's
// second view go with the first).
func auSizes(b []byte, hevc bool) []int {
	var sizes []int
	last := -1
	for i := 0; i+4 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		h := b[i+3:]
		first := false
		if hevc {
			typ, layer := h[0]>>1&0x3f, (h[0]&1)<<5|h[1]>>3
			first = typ < 32 && layer == 0 && len(h) > 2 && h[2]&0x80 != 0
		} else {
			typ := h[0] & 0x1f
			first = (typ == 1 || typ == 5) && len(h) > 1 && h[1]&0x80 != 0
		}
		if first {
			if last >= 0 {
				sizes = append(sizes, i-last)
			}
			last = i
		}
		i += 2
	}
	if last >= 0 {
		sizes = append(sizes, len(b)-last)
	}
	return sizes
}

// sizeSample is what the places encoded at a CRF came to: their IDR
// pictures' mean size, the others'.

type sizeSample struct {
	idr, other float64
}

// filmSize is a film's size from the samples: an IDR picture a GOP.
func (s sizeSample) filmSize(pictures float64) float64 {
	idrs := math.Ceil(pictures / gpu.DefaultGOP)
	return idrs*s.idr + (pictures-idrs)*s.other
}

// estimateCRF finds the CRF for --video-size, for a film of so many
// pictures, from its first picture's settings (the run's options, now
// resolved).
func (r *Runner) estimateCRF(ctx context.Context, video Track, feed sampleFeed, pictures float64, num, den int) (int, error) {
	quiet := r.Report
	r.Report = func(string, ...any) {}
	defer func() { r.Report = quiet }()
	crf0 := r.Opts.CRF
	defer func() { r.Opts.CRF = crf0 }()
	dir, err := os.MkdirTemp(r.Opts.TempDir, "bdtools-size-")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// The film's size at a CRF, from the places encoded at it.
	measure := func(crf int) (float64, error) {
		r.Opts.CRF = crf
		var idr, other []int
		for i := range sizePlaces {
			sizes, err := r.encodeSample(ctx, video, feed, (float64(i)+0.5)/sizePlaces, filepath.Join(dir, fmt.Sprintf("c%d-%d", crf, i)), num, den)
			if err != nil {
				return 0, err
			}
			if len(sizes) > sizeSettle+1 {
				idr = append(idr, sizes[0])
				other = append(other, sizes[1+sizeSettle:]...)
			}
		}
		if len(idr) == 0 || len(other) == 0 {
			return 0, errors.New("the places encoded to nothing")
		}
		return sizeSample{mean(idr), mean(other)}.filmSize(pictures), nil
	}
	type point struct {
		crf  int
		size float64
	}
	var pts []point
	for _, c := range []int{max(crf0-sizeCRFStep/2, 0), min(crf0+sizeCRFStep/2, 51)} {
		sz, err := measure(c)
		if err != nil {
			return 0, err
		}
		pts = append(pts, point{c, sz})
	}
	target := float64(r.Opts.VideoSize)
	// log size = a + b crf, through the two points nearest the answer;
	// an answer away from the points measured is measured, and the line
	// drawn again (the slope is not the same everywhere).
	solve := func() float64 {
		sort.Slice(pts, func(i, j int) bool { return pts[i].crf < pts[j].crf })
		p0, p1 := pts[0], pts[1]
		for k := 0; k+1 < len(pts); k++ {
			if pts[k].size >= target && pts[k+1].size <= target {
				p0, p1 = pts[k], pts[k+1]
				break
			}
		}
		if target < pts[len(pts)-1].size {
			p0, p1 = pts[len(pts)-2], pts[len(pts)-1]
		}
		b := (math.Log(p1.size) - math.Log(p0.size)) / float64(p1.crf-p0.crf)
		if b >= 0 || math.IsNaN(b) {
			b = -math.Ln2 / 6 // the usual: 6 steps halve it
		}
		return float64(p0.crf) + (math.Log(target)-math.Log(p0.size))/b
	}
	crf := solve()
	for range 2 {
		c := min(max(int(math.Round(crf)), 0), 51)
		near := false
		for _, p := range pts {
			near = near || math.Abs(float64(p.crf)-crf) < 1.5
		}
		if near {
			break
		}
		sz, err := measure(c)
		if err != nil {
			return 0, err
		}
		pts = append(pts, point{c, sz})
		crf = solve()
	}
	// Up to the next whole CRF: at most the size, rather than as near.
	got := min(max(int(math.Ceil(crf-0.05)), 0), 51)
	r.Report = quiet
	var seen []string
	for _, p := range pts {
		seen = append(seen, fmt.Sprintf("%s at CRF %d", size(p.size), p.crf))
	}
	r.Report.Report("--video-size %s: CRF %d (the places put the film at %s)", size(target), got, strings.Join(seen, ", "))
	return got, nil
}

func mean(v []int) float64 {
	var s float64
	for _, x := range v {
		s += float64(x)
	}
	return s / float64(len(v))
}

// encodeSample encodes sizePictures pictures from a place into path,
// giving its access units' sizes.
func (r *Runner) encodeSample(ctx context.Context, video Track, feed sampleFeed, frac float64, path string, num, den int) ([]int, error) {
	next, done, err := feed(frac)
	if err != nil {
		return nil, nil //nolint:nilerr // a place that cannot be read adds nothing
	}
	defer done()
	src, err := r.pictures(video, next)
	if err != nil {
		return nil, err
	}
	sink, _, err := r.newSink(ctx, nil)
	if err != nil {
		return nil, err
	}
	var xf *picTransform
	if t := r.xform; t != nil {
		// The run's transform, its buffers apart (its picture is waiting).
		c := *t
		c.flat = gpu.DecodedPicture{}
		if t.sc != nil {
			c.sc = newScaler(t.cw, t.ch, t.outW, t.outH)
		}
		if t.tm != nil {
			tm := *t.tm
			tm.out = gpu.DecodedPicture{}
			c.tm = &tm
		}
		xf = &c
	}
	path += r.Opts.Codec.streamExt()
	n := 0
	started := false
	err = src.run(func(p picture) error {
		if xf != nil {
			var err error
			if p, err = xf.apply(p); err != nil {
				return err
			}
		}
		if !started {
			if err := sink.start(path, p, num, den); err != nil {
				return err
			}
			started = true
		}
		if err := sink.put(p); err != nil {
			return err
		}
		if n++; n >= sizePictures {
			return errEnoughPictures
		}
		return nil
	})
	if started {
		if ferr := sink.finish(); ferr != nil && err == nil {
			err = ferr
		}
	}
	if err != nil && !errors.Is(err, errEnoughPictures) {
		return nil, err
	}
	b, err := os.ReadFile(path) //nolint:gosec // the work directory
	if err != nil {
		return nil, err
	}
	return auSizes(b, r.Opts.Codec != CodecH264), nil
}
