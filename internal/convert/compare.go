package convert

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/vmaf"
)

// Comparing a conversion with its source (--compare): both decoded here
// (the decoders a conversion uses, a Dolby Vision film's layers composed),
// their luma at the deeper of their depths and the output at the source's
// size if it is not, measured picture by picture as libvmaf measures:
// PSNR, SSIM and VMAF (internal/vmaf), its 4K model above 1080 lines.

// CompareResult is a comparison's measures over the pictures compared.
type CompareResult struct {
	Pictures         int
	PSNR, SSIM, VMAF vmaf.Pooled
	Scores           []vmaf.Score
	Model            string
}

// decodeVideo decodes a source's video (its 2D picture, or a 3D one's
// base view), giving each picture to fn.
func (r *Runner) decodeVideo(ctx context.Context, path string, fn func(*gpu.DecodedPicture) error) error {
	r.flat = true
	r.Opts.TwoD = true
	var (
		next  func() ([]byte, []byte, int64, error)
		video Track
		done  func()
	)
	if isMatroska(path) || isMP4(path) {
		src, tracks, err := probeMatroska(ctx, path)
		if err != nil {
			return err
		}
		sel, err := SelectTracks2D(tracks)
		if err != nil {
			return err
		}
		g := newMkvDemux(src, Selection{Base: sel.Base}, "", r.Report)
		if err := g.start(); err != nil {
			return err
		}
		next, video, done = g.Next, sel.Base, func() { _ = g.f.Close() }
	} else {
		src, err := resolveGo(path, r.Opts.Playlist, true, r.Report)
		if err != nil {
			return err
		}
		tracks, err := probeGo(ctx, src)
		if err != nil {
			return err
		}
		sel, err := SelectTracks2D(tracks)
		if err != nil {
			return err
		}
		tmp, err := os.MkdirTemp(r.Opts.TempDir, "bdtools-compare-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		g := newGoDemux(src, Selection{Base: sel.Base}, tmp, r.Report)
		if err := g.start(); err != nil {
			return err
		}
		next, video, done = g.Next, sel.Base, func() { g.closeClip() }
	}
	defer done()
	pics, err := r.pictures(video, next)
	if err != nil {
		return err
	}
	return pics.run(func(p picture) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		d := p.gpu
		if d == nil {
			var t picTransform
			d = t.flatten(p)
		}
		return fn(d)
	})
}

// lumaFrame is a decoded picture's luma at depth bits.
func lumaFrame(p *gpu.DecodedPicture, depth int) vmaf.Frame {
	f := vmaf.Frame{W: p.Width, H: p.Height, BPC: depth, Y: make([]uint16, p.Width*p.Height)}
	for y := range p.Height {
		row := p.Y[y*p.Pitch:]
		for x := range p.Width {
			var v uint16
			if p.Depth > 8 {
				v = binary.LittleEndian.Uint16(row[2*x:]) >> 6
			} else {
				v = uint16(row[x])
			}
			f.Y[y*p.Width+x] = v << uint(depth-max(p.Depth, 8)) //nolint:gosec // depths
		}
	}
	return f
}

// errCompared stops a decode once its pictures are compared.
var errCompared = errors.New("compared")

// Compare measures the video of dis against ref's, a picture of ref
// against one of dis from the start, after skip pictures of ref; with
// every above 1, a picture in every that many.
func Compare(ctx context.Context, goos string, o Options, ref, dis string, skip, every int, report Reporter) (CompareResult, error) {
	if every < 1 {
		every = 1
	}
	type pic struct {
		p   *gpu.DecodedPicture
		err error
	}
	decode := func(path string) <-chan pic {
		out := make(chan pic, 4)
		go func() {
			defer close(out)
			r := NewRunner(goos, o, func(string, ...any) {})
			err := r.decodeVideo(ctx, path, func(p *gpu.DecodedPicture) error {
				c := *p // valid only during the call: a copy
				c.Y = append([]byte(nil), p.Y[:p.Pitch*p.Height]...)
				c.UV = nil
				select {
				case out <- pic{p: &c}:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			if err != nil && !errors.Is(err, errCompared) && !errors.Is(err, context.Canceled) {
				out <- pic{err: fmt.Errorf("%s: %w", path, err)}
			}
		}()
		return out
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	refs, diss := decode(ref), decode(dis)
	for range skip {
		if p, ok := <-refs; !ok || p.err != nil {
			return CompareResult{}, errors.Join(io.ErrUnexpectedEOF, p.err)
		}
	}
	var res CompareResult
	var sc *vmaf.Scorer
	var sc2 *scaler
	k := 0
	for {
		rp, ok1 := <-refs
		dp, ok2 := <-diss
		if rp.err != nil {
			return res, rp.err
		}
		if dp.err != nil {
			return res, dp.err
		}
		if !ok1 || !ok2 {
			break
		}
		if k++; (k-1)%every != 0 {
			continue
		}
		r, d := rp.p, dp.p
		depth := max(r.Depth, d.Depth, 8)
		if sc == nil {
			var err error
			if sc, err = vmaf.NewScorer(r.Height); err != nil {
				return res, err
			}
			res.Model = "vmaf_v0.6.1"
			if r.Height > 1080 {
				res.Model = "vmaf_4k_v0.6.1"
			}
			if d.Width != r.Width || d.Height != r.Height {
				report.Report("--compare: the output is %dx%d, the source %dx%d: the output scaled up to it", d.Width, d.Height, r.Width, r.Height)
			}
		}
		if d.Width != r.Width || d.Height != r.Height {
			if sc2 == nil {
				sc2 = newScaler(d.Width, d.Height, r.Width, r.Height)
			}
			d.UV = make([]byte, d.Pitch*d.Height/2)
			d = sc2.scale(d)
		}
		sc.Add(lumaFrame(r, depth), lumaFrame(d, depth))
		res.Pictures++
		if res.Pictures%1000 == 0 {
			report.Report("--compare: %d pictures", res.Pictures)
		}
	}
	cancel()
	if sc == nil {
		return res, errors.New("--compare: no pictures to compare")
	}
	res.Scores = sc.Scores()
	var p, s, v []float64
	for _, x := range res.Scores {
		p, s, v = append(p, x.PSNR), append(s, x.SSIM), append(v, x.VMAF)
	}
	res.PSNR, res.SSIM, res.VMAF = vmaf.Pool(p), vmaf.Pool(s), vmaf.Pool(v)
	return res, nil
}
