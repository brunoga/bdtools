package convert

// --3d-reference: the film's own 3D beside the conversion in a live
// grading, to switch between at a press: how far the conversion is from
// what the stereographer did. The reference's pictures are read by a
// reader of their own, from the film's time (plus an offset: the two
// files' clocks differ when their discs do), and found again by their
// pictures when asked.

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/brunoga/bdtools/mvc"
)

// gradeRef is the reference and its reader.
type gradeRef struct {
	g      *grader
	r      *Runner // the grader's, quiet: the reader says nothing
	src    liveSource
	layout Layout
	swap   bool

	mu       sync.Mutex
	isOn     bool
	offset   time.Duration // the reference's time less the film's
	aligned  bool
	aligning bool
	alignReq bool
	showing  bool

	// the reader
	cancel func()
	frames chan refFrame
	done   chan struct{}
	from   time.Duration // where the reader started, on the film's clock
	next   *refFrame     // the frame taken from the reader and not yet shown
}

// refFrame is a pair of the reference's at a time of the film.
type refFrame struct {
	l, r *mvc.Frame
	t    time.Duration
}

// openReference opens --3d-reference for a grading.
func (r *Runner) openReference(ctx context.Context, g *grader) (*gradeRef, error) {
	file := r.Opts.Reference3D
	if !isMatroska(file) {
		return nil, fmt.Errorf("--3d-reference: %s: want a Matroska or MP4 file of the film's 3D (side by side or over-under)", file)
	}
	src, tracks, err := probeMatroska(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("--3d-reference: %w", err)
	}
	sel, err := SelectTracks2D(tracks)
	if err != nil {
		return nil, fmt.Errorf("--3d-reference: %w", err)
	}
	quiet := *r
	quiet.Report = nil
	quiet.resolveFramed(sel.Base)
	if quiet.framedIn == "" {
		return nil, fmt.Errorf("--3d-reference: %s does not say its frame holds both eyes (no stereo mode)", file)
	}
	live, err := quiet.matroskaLive(src, sel)
	if err != nil {
		return nil, err
	}
	return &gradeRef{g: g, r: &quiet, src: live, layout: quiet.framedIn, swap: quiet.framedSwap}, nil
}

func (f *gradeRef) on() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.isOn
}

// set switches the reference on or off, and moves its offset when given.
func (f *gradeRef) set(on bool, offset *time.Duration) {
	f.mu.Lock()
	f.isOn = on
	if offset != nil {
		f.offset, f.aligned = *offset, false
		f.stopLocked()
	}
	f.mu.Unlock()
}

func (f *gradeRef) askAlign() {
	f.mu.Lock()
	f.alignReq = true
	f.mu.Unlock()
}

func (f *gradeRef) alignAsked() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alignReq
}

func (f *gradeRef) state() *gradeRefState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &gradeRefState{File: f.r.Opts.Reference3D, On: f.isOn, Offset: f.offset.Seconds(), Aligned: f.aligned, Aligning: f.aligning, Showing: f.showing}
}

// setShowing records whether the reference's pair is the one on screen.
func (f *gradeRef) setShowing(on bool) {
	f.mu.Lock()
	f.showing = on
	f.mu.Unlock()
}

// stop ends the reader.
func (f *gradeRef) stop() {
	f.mu.Lock()
	f.stopLocked()
	f.mu.Unlock()
}

func (f *gradeRef) stopLocked() {
	if f.cancel != nil {
		f.cancel()
		<-f.done
		f.cancel, f.frames, f.done, f.next = nil, nil, nil, nil
	}
}

// start begins a reader at a time of the film.
func (f *gradeRef) start(ctx context.Context, from, frame time.Duration) {
	f.stopLocked()
	rctx, cancel := context.WithCancel(ctx)
	frames, done := make(chan refFrame, 4), make(chan struct{})
	f.cancel, f.frames, f.done, f.from = cancel, frames, done, from
	offset := f.offset
	go func() {
		defer close(done)
		defer close(frames)
		next, finish, err := f.src.seek(from+offset, frame)
		if err != nil || next == nil {
			return
		}
		defer finish()
		src, err := f.r.pictures(f.src.video, next)
		if err != nil {
			return
		}
		_ = src.run(func(p picture) error {
			if rctx.Err() != nil {
				return rctx.Err()
			}
			t := f.src.clock(p.pts) - offset
			if t+frame/2 < from {
				return nil
			}
			l, r := splitFramed(p, f.layout)
			if f.swap {
				l, r = r, l
			}
			select {
			case frames <- refFrame{l, r, t}:
				return nil
			case <-rctx.Done():
				return rctx.Err()
			}
		})
	}()
}

// at is the reference's pair at a time of the film, from the reader
// (started, or started again, when it is not near).
func (f *gradeRef) at(ctx context.Context, t, frame time.Duration) (l, r *mvc.Frame, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.frames == nil || t < f.from-frame/2 || (f.next != nil && t-f.next.t > 2*time.Second) {
		f.start(ctx, t, frame)
	}
	for {
		if f.next == nil {
			select {
			case fr, ok := <-f.frames:
				if !ok {
					return nil, nil, false // the reference's end
				}
				f.next = &fr
			case <-ctx.Done():
				return nil, nil, false
			case <-time.After(3 * time.Second):
				return nil, nil, false // a reader that cannot keep up
			}
		}
		switch d := f.next.t - t; {
		case d < -frame/2:
			f.next = nil // behind: the next
		case d > frame/2:
			return nil, nil, false // ahead of the film: nothing here yet
		default:
			return f.next.l, f.next.r, true
		}
	}
}

// align finds the reference's offset from the film by their pictures:
// the film's picture at t against the reference's eight seconds either
// side of where the offset puts it, the nearest wins.
func (f *gradeRef) align(ctx context.Context, pic *mvc.Frame, t time.Duration) {
	f.mu.Lock()
	f.alignReq, f.aligning = false, true
	f.stopLocked()
	offset := f.offset
	f.mu.Unlock()
	f.g.changed()
	const span = 8 * time.Second
	want := thumbnail(pic)
	from := t + offset - span
	best, bestAt, n := math.MaxFloat64, time.Duration(0), 0
	next, finish, err := f.src.seek(from, f.g.frameOr())
	if err == nil && next != nil {
		src, err := f.r.pictures(f.src.video, next)
		if err == nil {
			_ = src.run(func(p picture) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				tr := f.src.clock(p.pts)
				if tr < from {
					return nil
				}
				if tr > t+offset+span {
					return errEnoughPictures
				}
				l, _ := splitFramed(p, f.layout)
				if d := thumbnailDistance(want, thumbnail(l)); d < best {
					best, bestAt = d, tr
				}
				n++
				return nil
			})
		}
		finish()
	}
	f.mu.Lock()
	if n > 0 {
		f.offset, f.aligned = bestAt-t, true
	}
	f.aligning = false
	f.mu.Unlock()
	if n > 0 {
		f.g.r.Report.Report("--3d-reference: aligned at %s of the film: the reference runs %+.3f s ahead (%d pictures looked at)", clock(t), (bestAt - t).Seconds(), n)
	} else {
		f.g.r.Report.Report("--3d-reference: nothing to align with around %s", clock(t))
	}
}

// thumbnail is a picture's luma at thumbW x thumbH, each cell its mean.
const (
	thumbW = 64
	thumbH = 36
)

func thumbnail(f *mvc.Frame) []float32 {
	out := make([]float32, thumbW*thumbH)
	for ty := range thumbH {
		y0, y1 := ty*f.Height/thumbH, (ty+1)*f.Height/thumbH
		for tx := range thumbW {
			x0, x1 := tx*f.Width/thumbW, (tx+1)*f.Width/thumbW
			var sum, n int
			for y := y0; y < y1; y++ {
				row := f.Y[y*f.StrideY:]
				for x := x0; x < x1; x++ {
					sum += int(row[x])
					n++
				}
			}
			if n > 0 {
				out[ty*thumbW+tx] = float32(sum) / float32(n)
			}
		}
	}
	return out
}

// thumbnailDistance is the mean absolute difference of two thumbnails,
// each less its own mean (an eye of a 3D disc is lit a little differently).
func thumbnailDistance(a, b []float32) float64 {
	var ma, mb float64
	for i := range a {
		ma += float64(a[i])
		mb += float64(b[i])
	}
	ma /= float64(len(a))
	mb /= float64(len(b))
	var d float64
	for i := range a {
		d += math.Abs(float64(a[i]) - ma - float64(b[i]) + mb)
	}
	return d / float64(len(a))
}
