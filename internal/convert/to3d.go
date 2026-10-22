package convert

// 2D to 3D: a 2D source's pictures made stereo pairs (internal/stereo, the
// depth by internal/depth's video depth network) and put out as a 3D
// source's are, in any of the 3D layouts.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/brunoga/bdtools/internal/depth"
	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// The depth network used unless --depth-model names another: Video Depth
// Anything Small (Apache-2.0), its published checkpoint.
const (
	vdaSmallURL    = "https://huggingface.co/depth-anything/Video-Depth-Anything-Small/resolve/main/video_depth_anything_vits.pth"
	vdaSmallSHA256 = "13379300b739e659f076a59d52e9801bd8d38c541a7e71f73bbca4dcfb013609"
	vdaSmallFile   = "video_depth_anything_vits.pth"
)

// to3DState is a conversion's converter, and its subtitles' depth.
type to3DState struct {
	conv *stereo.Converter
	subs float64 // the subtitles' offset now, in pixels
}

// subsFall is how the subtitles' depth follows the picture's farther:
// slowly (pixels a frame), so they do not shake; nearer, it follows at
// once (they are never behind what is in front of them).
const subsFall = 0.2

// subtitleOffset is where a subtitle over a picture of disparity d (w
// wide) should sit, as an offset (pixels each eye moves, positive toward
// the viewer): just in front of the nearest of the lower half of the
// picture, where subtitles are (its 99th percentile, the odd pixel left
// out), at the screen when all of it is behind.
func subtitleOffset(d []float32, w int) float64 {
	h := len(d) / w
	var near []float32
	for y := h / 2; y < h; y += 4 {
		for x := 0; x < w; x += 4 {
			near = append(near, d[y*w+x])
		}
	}
	if len(near) == 0 {
		return 0
	}
	slices.Sort(near)
	p := float64(near[len(near)-1-len(near)/100])
	if p <= 0 {
		return 0
	}
	return p/2 + 1
}

// useTo3D turns a 2D source into a 3D conversion when --to-3d asks.
func (r *Runner) useTo3D(threeD bool) error {
	if !r.Opts.To3D {
		return nil
	}
	if threeD || r.framedIn != "" {
		return errors.New("--to-3d makes a 2D source 3D: this one is 3D already")
	}
	r.flat, r.Opts.TwoD, r.to3D = false, false, true
	return nil
}

// to3DKey is what of --to-3d changes the pictures (for resuming).
func (o Options) to3DKey() [3]float64 {
	if !o.To3D {
		return [3]float64{}
	}
	w := 1.0
	if o.NoWindow3D {
		w = 2
	}
	return [3]float64{w, o.Depth3D, o.Screen3D}
}

// refineNone is Refine3D's for no refinement.
const refineNone = "none"

// refine3DKey is the refinement's weights, for resuming.
func (o Options) refine3DKey() string {
	if !o.To3D {
		return ""
	}
	return o.Refine3D
}

// shape3D is the depth's shape for the options.
func (o Options) shape3D() stereo.Shape {
	s := stereo.DefaultShape
	if o.Refine3D != refineNone {
		s = stereo.RefinedShape
	}
	if o.Depth3D > 0 {
		s.Scale *= o.Depth3D
		s.Offset *= o.Depth3D
	}
	s.Offset += o.Screen3D / 100
	return s
}

// hdrPicture reports whether a picture is HDR (PQ or HLG, or Dolby
// Vision's).
func hdrPicture(p picture) bool {
	if g := p.gpu; g != nil && (g.Color.Transfer == 16 || g.Color.Transfer == 18) {
		return true
	}
	return len(p.rpu) > 0
}

// convert3D makes a picture a stereo pair.
func (r *Runner) convert3D(p picture) (picture, error) {
	if r.conv == nil {
		m, err := r.depthModel()
		if err != nil {
			return p, err
		}
		r.conv = &to3DState{conv: stereo.NewConverter(m, r.Opts.shape3D())}
		r.conv.conv.NoWindow = r.Opts.NoWindow3D
		switch f := r.Opts.Refine3D; f {
		case refineNone:
		case "":
			r.conv.conv.Refine = stereo.BuiltinRefine()
			r.Report.Report("--to-3d: the depth refined (the built-in weights)")
		default:
			w, err := stereo.ReadRefine(f)
			if err != nil {
				return p, fmt.Errorf("--3d-refine: %w", err)
			}
			r.conv.conv.Refine = w
			r.Report.Report("--to-3d: the depth refined by %s", f)
		}
		s := r.Opts.shape3D()
		r.Report.Report("--to-3d: disparity %.3f%% of the width a unit of depth, %+.2f%% at depth 0 (the farthest)",
			100*s.Scale, 100*s.Offset)
	}
	f := pictureFrame8(p)
	_, d, err := r.conv.conv.Next(f)
	if err != nil {
		return p, fmt.Errorf("--to-3d: the depth: %w", err)
	}
	l, rr := stereo.Pair(f, d)
	r.conv.conv.Window(l, rr, d)
	if r.depth != nil && r.timeline != nil {
		// the subtitles' depth, as a disc's offset sequence 0 gives it
		t := subtitleOffset(d, f.Width)
		s := &r.conv.subs
		if t > *s {
			*s = t
		} else {
			*s = max(t, *s-subsFall)
		}
		v := int8(min(math.Ceil(*s), 127))
		r.depth.add(r.timeline(p.pts), offsetGOP{pts: p.pts, offsets: [][]int8{{v}}})
	}
	l.PTS, rr.PTS = p.pts, p.pts
	return picture{stereo: &mvc.StereoFrame{Base: l, Dependent: rr}, pts: p.pts}, nil
}

// pictureFrame8 copies a picture (NV12, P010 or planar) as an 8-bit 4:2:0 frame.
func pictureFrame8(p picture) *mvc.Frame {
	fp := planesOf(p)
	w, h := fp.w, fp.h
	cw, ch := (w+1)/2, (h+1)/2
	f := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: cw, PTS: p.pts,
		Y: make([]byte, w*h), Cb: make([]byte, cw*ch), Cr: make([]byte, cw*ch)}
	var wg sync.WaitGroup
	for pl, dst := range [3][]byte{f.Y, f.Cb, f.Cr} {
		pw, ph := w, h
		if pl > 0 {
			pw, ph = cw, ch
		}
		wg.Go(func() {
			buf := make([]byte, pw)
			for y := range ph {
				copy(dst[y*pw:(y+1)*pw], fp.row(pl, y, 0, pw, buf))
			}
		})
	}
	wg.Wait()
	return f
}

// depthModel loads the depth network (on the GPU when there is a CUDA
// one, else on the CPU): --depth-model's checkpoint, else Video Depth
// Anything Small's, downloaded into the user's cache the first time
// (checked against its known digest).
func (r *Runner) depthModel() (*depth.Model, error) {
	m, err := r.loadDepthModel()
	if err != nil {
		return nil, err
	}
	if g, err := nn.NewGPU(); err == nil {
		m.Use(g)
		r.Report.Report("--to-3d: the depth on the GPU (%s)", g.Name())
	} else {
		r.Report.Report("--to-3d: the depth on the CPU (slow: no CUDA GPU, %v)", err)
	}
	return m, nil
}

func (r *Runner) loadDepthModel() (*depth.Model, error) {
	if f := r.Opts.DepthModel; f != "" {
		return depth.Load(f)
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("--to-3d: no cache directory for the depth model (%w): name one with --depth-model", err)
	}
	file := filepath.Join(dir, "bdtools", vdaSmallFile)
	if _, err := os.Stat(file); err != nil {
		r.Report.Report("--to-3d: downloading the depth model (Video Depth Anything Small, 116 MB) to %s", file)
		if err := download(vdaSmallURL, file, vdaSmallSHA256); err != nil {
			return nil, fmt.Errorf("--to-3d: the depth model: %w (or name a copy with --depth-model)", err)
		}
	}
	return depth.Load(file)
}

// download fetches url into file through a .part file, kept only when its
// SHA-256 is want.
func download(url, file, want string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return err
	}
	resp, err := http.Get(url) //nolint:gosec,noctx // a fixed URL
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	part := file + ".part"
	out, err := os.Create(part) //nolint:gosec // the cache's file
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), resp.Body)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = errors.New("the download is not the file it should be (its SHA-256 differs)")
	}
	if err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, file)
}

// DecodeFrames decodes a video's pictures (its first video track, as
// --compare does) as 8-bit 4:2:0 frames, one after another, for tools
// that look at the pictures themselves (the 2D to 3D benchmark). With
// keep, only the pictures it keeps (by their time from the first
// picture) are handed to fn; return ErrDecodeDone from fn to stop early.
func DecodeFrames(ctx context.Context, goos string, o Options, path string, keep func(time.Duration) bool, fn func(*mvc.Frame) error) error {
	r := NewRunner(goos, o, func(string, ...any) {})
	first, known := int64(0), false
	err := r.decodeVideo(ctx, path, func(p *gpu.DecodedPicture) error {
		if !known {
			first, known = p.PTS, true
		}
		if keep != nil && !keep(ticks90k(p.PTS-first)) {
			return nil
		}
		return fn(pictureFrame8(picture{gpu: p, pts: p.PTS}))
	})
	if errors.Is(err, ErrDecodeDone) {
		return nil
	}
	return err
}

// ErrDecodeDone stops DecodeFrames without an error, returned by its fn.
var ErrDecodeDone = errors.New("decode done")
