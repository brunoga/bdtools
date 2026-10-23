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

// shape3D is the depth's shape for the options and the network's size
// (its own scale of depth: depth.Model.Size).
func (o Options) shape3D(size string) stereo.Shape {
	s := stereo.ShapeFor(size, o.Refine3D != refineNone)
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

// convert3D makes a picture a stereo pair: its depth now (on the GPU),
// the rest (the eyes) by the function it gives, which may run beside the
// next picture's depth. The functions must be called in order.
func (r *Runner) convert3D(p picture) (func() (picture, error), error) {
	if r.conv == nil {
		m, err := r.depthModel()
		if err != nil {
			return nil, err
		}
		r.conv = &to3DState{conv: stereo.NewConverter(m, r.Opts.shape3D(m.Size()))}
		r.conv.conv.NoWindow = r.Opts.NoWindow3D
		switch f := r.Opts.Refine3D; f {
		case refineNone:
		case "":
			r.conv.conv.Refine = stereo.BuiltinRefine()
			r.Report.Report("--to-3d: the depth refined (the built-in weights)")
		default:
			w, err := stereo.ReadRefine(f)
			if err != nil {
				return nil, fmt.Errorf("--3d-refine: %w", err)
			}
			r.conv.conv.Refine = w
			r.Report.Report("--to-3d: the depth refined by %s", f)
		}
		s := r.Opts.shape3D(m.Size())
		r.Report.Report("--to-3d: disparity %.3f%% of the width a unit of depth, %+.2f%% at depth 0 (the farthest)",
			100*s.Scale, 100*s.Offset)
	}
	f := pictureFrame8(p)
	e, err := r.conv.conv.Estimate(f)
	if err != nil {
		return nil, fmt.Errorf("--to-3d: the depth: %w", err)
	}
	return func() (picture, error) { return r.pair3D(p, f, e), nil }, nil
}

// pair3D is the rest of convert3D: the picture's depth shaped, the eyes
// made from it.
func (r *Runner) pair3D(p picture, f *mvc.Frame, e *stereo.Estimate) picture {
	l, rr, d := r.conv.conv.Eyes(e)
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
