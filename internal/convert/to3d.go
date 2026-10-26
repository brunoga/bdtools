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

// depthNet is a published depth network --depth-model names, downloaded
// into the user's cache the first time (checked against its digest).
type depthNet struct {
	name, file, url, sha256, size, licence string
}

// depthNets are the networks --depth-model names: Video Depth Anything
// Small (Apache-2.0, the default), and Base and Large, whose licence
// allows non-commercial use only.
var depthNets = map[string]depthNet{
	"small": {"Video Depth Anything Small", "video_depth_anything_vits.pth",
		"https://huggingface.co/depth-anything/Video-Depth-Anything-Small/resolve/main/video_depth_anything_vits.pth",
		"13379300b739e659f076a59d52e9801bd8d38c541a7e71f73bbca4dcfb013609", "116 MB", ""},
	"base": {"Video Depth Anything Base", "video_depth_anything_vitb.pth",
		"https://huggingface.co/depth-anything/Video-Depth-Anything-Base/resolve/main/video_depth_anything_vitb.pth",
		"775e578e8f9431ec0496514aa466bd0a1f67c28d0f518267809f35a43c04329b", "458 MB", "CC-BY-NC-4.0"},
	"large": {"Video Depth Anything Large", "video_depth_anything_vitl.pth",
		"https://huggingface.co/depth-anything/Video-Depth-Anything-Large/resolve/main/video_depth_anything_vitl.pth",
		"43df27c6b396042ba34ff7b798ab279f64d204d2e86d7a373968f8fa36d0e6fa", "1.5 GB", "CC-BY-NC-4.0"},
}

// to3DState is a conversion's converter, and its subtitles' depth.
type to3DState struct {
	conv *stereo.Converter
	subs float64 // the subtitles' offset now, in pixels
	// the picture whose depth is under way
	pend      *stereo.Pending
	pendPic   picture
	pendFrame *mvc.Frame
}

// subsFall is how the subtitles' depth follows the picture's farther:
// slowly (pixels a frame), so they do not shake; nearer, it follows at
// once (they are never behind what is in front of them).
const subsFall = 0.2

// subtitleOffset is where a subtitle over a picture should sit, as an
// offset (pixels each eye moves, positive toward the viewer), from its
// disparity's samples of the lower half, where subtitles are (low, sorted
// in place): just in front of the nearest of them (their 99th
// percentile, the odd pixel left out), at the screen when all of it is
// behind.
func subtitleOffset(low []float32) float64 {
	if len(low) == 0 {
		return 0
	}
	slices.Sort(low)
	p := float64(low[len(low)-1-len(low)/100])
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
func (o Options) to3DKey() [4]float64 {
	if !o.To3D {
		return [4]float64{}
	}
	w := 1.0
	if o.NoWindow3D {
		w = 2
	}
	return [4]float64{w, o.depth3D(), o.Screen3D, o.ScreenSize3D}
}

// DefaultDepth3D is --3d-depth's default: half again the depth that scores
// best against the 3D discs' (stereo.ShapeFor's). The discs' disparity
// spans 1.7 to 2.2 times that one's (5th to 95th percentile, on two films
// held out of its choice): a score against the disc's right eye punishes
// depth in the wrong place more than too little of it, so its best is
// flatter than the discs.
const DefaultDepth3D = 1.5

// depth3D is the depth's strength asked for, DefaultDepth3D when not.
func (o Options) depth3D() float64 {
	if o.Depth3D > 0 {
		return o.Depth3D
	}
	return DefaultDepth3D
}

// refineNone is Refine3D's for no refinement.
const refineNone = "none"

// depthModelKey is the depth network, for resuming.
func (o Options) depthModelKey() string {
	if !o.To3D {
		return ""
	}
	return o.DepthModel
}

// refine3DKey is the refinement's weights, for resuming.
func (o Options) refine3DKey() string {
	if !o.To3D {
		return ""
	}
	return o.Refine3D
}

// tune3DKey is the depth network's tail, for resuming (the built-in one
// named by its version, so a conversion begun before it began untuned).
func (o Options) tune3DKey() string {
	if !o.To3D {
		return ""
	}
	if o.Tune3D == "" {
		return "builtin1"
	}
	return o.Tune3D
}

// tuned3D reports whether the network (of its size: depth.Model.Size)
// takes a tail of its own: Tune3D's file, else the built-in one, which
// is Small's.
func (o Options) tuned3D(size string) bool {
	return o.Tune3D != refineNone && (o.Tune3D != "" || size == "small")
}

// refine3D is the refinement's weights asked for (Refine3D's), the
// built-in ones by default unless the network is tuned (whose depth they
// were not learnt on, and refining it loses), "" for none.
func (o Options) refine3D(size string) string {
	switch {
	case o.Refine3D == refineNone, o.Refine3D == "" && o.tuned3D(size):
		return ""
	case o.Refine3D == "":
		return "builtin"
	}
	return o.Refine3D
}

// shape3D is the depth's shape for the options and the network's size
// (its own scale of depth: depth.Model.Size).
func (o Options) shape3D(size string) stereo.Shape {
	s := stereo.ShapeFor(size, o.refine3D(size) != "", o.tuned3D(size))
	s.Scale *= o.depth3D()
	s.Offset *= o.depth3D()
	s.Offset += o.Screen3D / 100
	if o.ScreenSize3D > 0 {
		s.Far = stereo.ScreenFar(o.ScreenSize3D)
	}
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

// convert3D makes a picture a stereo pair: its depth begun now (on the
// GPU, beside the next picture's decoding), the rest (the eyes) by a
// function, which may run beside the next picture's depth. It gives the
// picture before's function (nil for the first), once that one's depth is
// made; wait3D gives the last's. The functions must be called in order.
func (r *Runner) convert3D(p picture) (func() (picture, error), error) {
	if r.conv == nil {
		m, err := r.depthModel()
		if err != nil {
			return nil, err
		}
		c, err := r.newConverter3D(m, true)
		if err != nil {
			return nil, err
		}
		r.conv = &to3DState{conv: c}
		s := r.Opts.shape3D(m.Size())
		r.Report.Report("--to-3d: disparity %.3f%% of the width a unit of depth, %+.2f%% at depth 0 (the farthest)",
			100*s.Scale, 100*s.Offset)
	}
	f := pictureFrame8(p)
	before, err := r.wait3D()
	if err != nil {
		return nil, err
	}
	r.conv.pend, r.conv.pendPic, r.conv.pendFrame = r.conv.conv.Start(f), p, f
	return before, nil
}

// newConverter3D is a converter of the depth model m as the options ask,
// what refines its depth reported when report.
func (r *Runner) newConverter3D(m *depth.Model, report bool) (*stereo.Converter, error) {
	c := stereo.NewConverter(m, r.Opts.shape3D(m.Size()))
	c.NoWindow = r.Opts.NoWindow3D
	say := func(format string, args ...any) {
		if report {
			r.Report.Report(format, args...)
		}
	}
	switch f := r.Opts.refine3D(m.Size()); f {
	case "":
	case "builtin":
		c.Refine = stereo.BuiltinRefine()
		say("--to-3d: the depth refined (the built-in weights)")
	default:
		w, err := stereo.ReadRefine(f)
		if err != nil {
			return nil, fmt.Errorf("--3d-refine: %w", err)
		}
		c.Refine = w
		say("--to-3d: the depth refined by %s", f)
	}
	return c, nil
}

// wait3D is the rest of the picture convert3D began last, once its depth
// is made (nil when there is none).
func (r *Runner) wait3D() (func() (picture, error), error) {
	if r.conv == nil || r.conv.pend == nil {
		return nil, nil
	}
	e, err := r.conv.pend.Wait()
	p, f := r.conv.pendPic, r.conv.pendFrame
	r.conv.pend, r.conv.pendPic, r.conv.pendFrame = nil, picture{}, nil
	if err != nil {
		return nil, fmt.Errorf("--to-3d: the depth: %w", err)
	}
	return func() (picture, error) { return r.pair3D(p, f, e), nil }, nil
}

// pair3D is the rest of convert3D: the picture's depth shaped, the eyes
// made from it.
func (r *Runner) pair3D(p picture, f *mvc.Frame, e *stereo.Estimate) picture {
	l, rr, s := r.conv.conv.Eyes(e)
	r.conv.conv.Window(l, rr, s)
	if r.depth != nil && r.timeline != nil {
		// the subtitles' depth, as a disc's offset sequence 0 gives it
		t := subtitleOffset(s.Low)
		sub := &r.conv.subs
		if t > *sub {
			*sub = t
		} else {
			*sub = max(t, *sub-subsFall)
		}
		v := int8(min(math.Ceil(*sub), 127))
		r.depth.add(r.timeline(p.pts), offsetGOP{pts: p.pts, offsets: [][]int8{{v}}})
	}
	l.PTS, rr.PTS = p.pts, p.pts
	return picture{stereo: &mvc.StereoFrame{Base: l, Dependent: rr}, pts: p.pts}
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
	if r.Opts.tuned3D(m.Size()) {
		if f := r.Opts.Tune3D; f != "" {
			w, err := depth.ReadTail(f)
			if err == nil {
				err = m.SetTail(w)
			}
			if err != nil {
				return nil, fmt.Errorf("--3d-tune: %w", err)
			}
			r.Report.Report("--to-3d: the depth network tuned by %s", f)
		} else {
			if err := m.SetTail(depth.BuiltinTail()); err != nil {
				return nil, err
			}
			r.Report.Report("--to-3d: the depth network tuned to the 3D discs (the built-in weights)")
		}
	}
	if g, err := nn.NewGPU(); err == nil {
		g.Half = m.HalfSafe() // the tensor cores: a third faster
		m.Use(g)
		r.Report.Report("--to-3d: the depth on the GPU (%s)", g.Name())
	} else {
		r.Report.Report("--to-3d: the depth on the CPU (slow: no CUDA GPU, %v)", err)
	}
	return m, nil
}

func (r *Runner) loadDepthModel() (*depth.Model, error) {
	f := r.Opts.DepthModel
	if f == "" {
		f = "small"
	}
	n, ok := depthNets[f]
	if !ok {
		return depth.Load(f)
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("--to-3d: no cache directory for the depth model (%w): name a file with --depth-model", err)
	}
	file := filepath.Join(dir, "bdtools", n.file)
	if n.licence != "" {
		r.Report.Report("--to-3d: %s is licensed %s: for non-commercial use only", n.name, n.licence)
	}
	if _, err := os.Stat(file); err != nil {
		r.Report.Report("--to-3d: downloading the depth model (%s, %s) to %s", n.name, n.size, file)
		if err := download(n.url, file, n.sha256); err != nil {
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
