package convert

// --3d-grade: a 2D film converted as it plays and served as a live 3D
// stream, to a player at the 3D screen (Kodi on a box at a projector, a
// headset's browser), with a web page to play, pause, step and seek it and
// to turn its depth and screen while it shows: a hand's grading, shot by
// shot, written as a --3d-shots file for the conversion proper. The
// depth network runs as the film plays (faster than it on a GPU); a
// paused picture keeps its depth and is made again at each turn of a
// knob, so the eyes see the change at once.

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/brunoga/bdtools/internal/bdmv"
	"github.com/brunoga/bdtools/internal/depth"
	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/mp4"
	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

//go:embed grade3d.html
var grade3DPage []byte

//go:embed grade3d_quest.html
var grade3DQuestPage []byte

const (
	// gradeQP is the live stream's quantiser: a good picture at a rate a
	// home network carries.
	gradeQP = 20
	// gradeGOP is its keyframe interval in frames: how long a player
	// joining waits for a picture it can start from.
	gradeGOP = 24
	// gradeSnap is how long after a cut (or a stretch's start) a turn of
	// a knob still counts from it: a hand reacts to a shot a moment late.
	gradeSnap = 2 * time.Second
	// gradeRamp is how long, in seconds, a change made within a shot (no
	// cut near) eases in over in the conversion (shot3D.Ramp).
	gradeRamp = 1.0
	// gradeJoinWait is how long a player's packets may wait on a slow
	// network before it is dropped.
	gradeJoinWait = 256
)

// gradeKnobs is what a hand sets for a stretch of the film.
type gradeKnobs struct {
	Depth  float64 `json:"depth"`
	Screen float64 `json:"screen"`
	Flat   bool    `json:"flat"`
}

// gradeWant is what the page asked for, read by the player at each
// picture.
type gradeWant struct {
	playing bool
	seek    *time.Duration // a seek asked, nil when none
	steps   int            // pictures to step through while paused
	regrade bool           // the grade changed: the held picture made again
}

// liveSource is a film read in places for a live grading.
type liveSource struct {
	video  Track
	length time.Duration
	feed   sampleFeed
	clock  func(pts int64) time.Duration // the film's time of a picture
	close  func()
}

// grader is a live grading under way.
type grader struct {
	r     *Runner
	src   liveSource
	model *depth.Model
	conv  *stereo.Converter
	num   int // the frame rate
	den   int
	frame time.Duration // a picture's time

	// what the page and the player share
	mu      sync.Mutex
	want    gradeWant
	wake    chan struct{}
	shots   []shot3D
	cuts    []time.Duration
	pos     time.Duration // the picture shown
	near    float64       // its disparity's extremes, percent of the width
	far     float64
	saved   bool
	clients map[*gradeClient]struct{}
	events  map[chan struct{}]struct{}
	ver     int
	addr    string

	ref *gradeRef // the film's own 3D, when given

	// the stream
	enc    gpu.Encoder
	params []byte // its parameter sets, for a player joining late
	width  int    // the stream's picture
	height int
	outPTS int64
	next   time.Time // when the next picture goes out
	last   [2]*mvc.Frame
	target time.Duration
	seeks  int
}

// gradeClient is a player reading the stream: its frames go through
// sink into its connection's queue.
type gradeClient struct {
	ch   chan []byte
	sink func(au []byte, pts int64, key bool) error
	dead bool
}

// tsSink is a player of the transport stream.
func (c *gradeClient) tsSink(params []byte, _, _ int) {
	live := &bdmv.Live{W: chanWriter{c.ch}, Coding: bdmv.CodingAVC, FromKey: true, Params: params}
	c.sink = func(au []byte, pts int64, _ bool) error {
		_, err := live.Frame(au, pts)
		return err
	}
}

// mp4Sink is a player of the fragmented MP4 (a browser's media source):
// the header from the parameter sets, then the frames from the first
// keyframe.
func (c *gradeClient) mp4Sink(params []byte, width, height int) {
	live := &mp4.Live{W: chanWriter{c.ch}}
	started := false
	c.sink = func(au []byte, pts int64, key bool) error {
		if _, p := bdmv.AnnexBKey(bdmv.CodingAVC, au); p != nil {
			params = p
		}
		if !started {
			if !key {
				return nil
			}
			sps, pps, ok := avcParams(params)
			if !ok {
				return nil // no parameter sets yet
			}
			if err := live.Init(sps, pps, width, height); err != nil {
				return err
			}
			started = true
		}
		return live.Frame(au, pts, key)
	}
}

// avcParams is the SPS and PPS among parameter sets (Annex B).
func avcParams(params []byte) (sps, pps []byte, ok bool) {
	for _, nal := range mp4.SplitAnnexB(params) {
		switch nal[0] & 0x1f {
		case 7:
			sps = nal
		case 8:
			pps = nal
		}
	}
	return sps, pps, sps != nil && pps != nil
}

// errSeek is show's: the film is wanted somewhere else.
type errSeek struct{ to time.Duration }

func (e *errSeek) Error() string { return "seek to " + clock(e.to) }

// grade3D is --3d-grade: the film served and graded until the context ends.
func (r *Runner) grade3D(ctx context.Context, src liveSource) error {
	defer src.close()
	defer r.closeTransform()
	m, err := r.depthModel()
	if err != nil {
		return err
	}
	c, err := r.newConverter3D(m, true)
	if err != nil {
		return err
	}
	c.KeepDepth = true
	g := &grader{r: r, src: src, model: m, conv: c, wake: make(chan struct{}, 1),
		clients: map[*gradeClient]struct{}{}, events: map[chan struct{}]struct{}{}, saved: true}
	c.Shot = g.shotShape
	switch {
	case r.framedIn != "":
		// a 3D file: its left eye is the picture converted, its pair the
		// reference, on one clock
		if r.Opts.Reference3D != "" {
			return errors.New("--3d-reference: the input is 3D itself, and its own reference")
		}
		g.ref = &gradeRef{g: g, r: r, self: true, layout: r.framedIn, swap: r.framedSwap, aligned: true}
		r.Report.Report("--3d-grade: a 3D file (%s): its left eye converted, its own pair the reference", r.framedIn)
	case r.Opts.Reference3D != "":
		if g.ref, err = r.openReference(ctx, g); err != nil {
			return err
		}
		defer g.ref.stop()
		defer g.ref.src.close()
		r.Report.Report("--3d-grade: the film's own 3D to compare with: %s (%s)", r.Opts.Reference3D, g.ref.layout)
	}
	if r.Opts.Shots3D != "" {
		if g.shots, err = readShots3D(r.Opts.Shots3D); err != nil {
			return err
		}
		r.Report.Report("--3d-grade: %d stretches to start from, %s", len(g.shots), r.Opts.Shots3D)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", r.Opts.Grade3D)
	if err != nil {
		return fmt.Errorf("--3d-grade: %w", err)
	}
	g.addr = reachable(ln.Addr().String())
	srv := &http.Server{Handler: g.handler(), ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	r.Report.Report("--3d-grade: the controls at http://%s/, the 3D stream at http://%s/stream.ts (full side by side); the grade goes to %s",
		g.addr, g.addr, r.Opts.Output)
	err = g.play(ctx)
	g.mu.Lock()
	saveErr := g.saveLocked()
	g.mu.Unlock()
	if g.enc != nil {
		_ = g.enc.Close()
	}
	shut, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = srv.Shutdown(shut)
	if err == nil || errors.Is(err, context.Canceled) {
		err = saveErr
	}
	if err == nil {
		r.Report.Report("--3d-grade: %d stretches in %s", len(g.shots), r.Opts.Output)
	}
	return err
}

// play reads the film from where it is wanted, picture after picture,
// until the context ends.
func (g *grader) play(ctx context.Context) error {
	pos := g.r.Opts.Trim.Start
	for {
		if ctx.Err() != nil {
			return nil
		}
		g.conv.Restart()
		next, done, err := g.seek(pos)
		if err != nil {
			return err
		}
		g.target = pos
		var src pictureSource
		if next != nil {
			if src, err = g.pictures(next); err != nil {
				done()
				return err
			}
			err = src.run(func(p picture) error { return g.picture(ctx, p, src) })
		}
		done()
		var sk *errSeek
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.As(err, &sk):
			pos = sk.to
		case err == nil || errors.Is(err, io.EOF):
			// the film's end (or nothing decodes there): hold until a seek
			g.mu.Lock()
			g.want.playing = false
			g.mu.Unlock()
			g.changed()
			if pos, err = g.waitSeek(ctx); err != nil {
				return err
			}
		default:
			return err
		}
	}
}

// pictures is the decoder of the film from a place: what it says of
// itself said once, not at every seek.
func (g *grader) pictures(next func() ([]byte, []byte, int64, error)) (pictureSource, error) {
	if g.seeks++; g.seeks > 1 {
		quiet := g.r.Report
		g.r.Report = nil
		defer func() { g.r.Report = quiet }()
	}
	return g.r.pictures(g.src.video, next)
}

// waitSeek holds (the stream kept alive with the last pair shown) until
// the page asks for a place.
func (g *grader) waitSeek(ctx context.Context) (time.Duration, error) {
	for {
		g.mu.Lock()
		s := g.want.seek
		g.want.seek, g.want.steps = nil, 0
		g.mu.Unlock()
		if s != nil {
			return *s, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-g.wake:
		case <-time.After(time.Until(g.next)):
			if g.last[0] != nil {
				if err := g.emit(ctx, g.last[0], g.last[1]); err != nil {
					return 0, err
				}
			} else {
				g.next = time.Now().Add(time.Second)
			}
		}
	}
}

// seek gives the film's access units from the keyframe before pos.
func (g *grader) seek(pos time.Duration) (next func() ([]byte, []byte, int64, error), done func(), err error) {
	return g.src.seek(pos, g.frameOr())
}

// seek gives a source's access units from the keyframe before pos (by
// its bytes: a place is tried, the time there read, the place moved);
// frame is a picture's time. A nil next means nothing can be read there.
func (s *liveSource) seek(pos, frame time.Duration) (next func() ([]byte, []byte, int64, error), done func(), err error) {
	length := s.length
	if length <= 0 {
		n, d, err := s.feed(0)
		return n, d, err
	}
	pos = min(max(pos, 0), length-frame)
	const aim = 2 * time.Second
	frac := pos.Seconds() / length.Seconds()
	for try := 0; ; try++ {
		frac = min(max(frac, 0), 0.999)
		n, d, err := s.feed(frac)
		if err != nil {
			return nil, func() {}, err
		}
		base, dep, pts, err := n()
		if err != nil {
			d()
			if frac > 0 && try < 6 {
				frac -= 0.02 // the file's end: back off
				continue
			}
			return nil, func() {}, nil
		}
		t := s.clock(pts)
		if try >= 5 || (t <= pos && pos-t <= 4*aim) || (frac <= 0 && t > pos) {
			first := true
			return func() ([]byte, []byte, int64, error) {
				if first {
					first = false
					return base, dep, pts, nil
				}
				return n()
			}, d, nil
		}
		d()
		frac += (pos - aim - t).Seconds() / length.Seconds()
	}
}

// frameOr is a picture's time, a film's when not yet known.
func (g *grader) frameOr() time.Duration {
	if g.frame > 0 {
		return g.frame
	}
	return time.Second * 1001 / 24000
}

// picture takes a decoded picture: skipped on the way to a seek's target,
// else shown.
func (g *grader) picture(ctx context.Context, p picture, src pictureSource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := g.r.transform(p, src)
	if err != nil {
		return err
	}
	t := g.src.clock(p.pts)
	if g.num == 0 {
		sn, sd := src.frameRate()
		num, den, _ := pictureRate(sn, sd, g.r.rateNum, g.r.rateDen)
		if num <= 0 {
			num, den = 24000, 1001
		}
		g.num, g.den = num, den
		g.frame = time.Duration(int64(time.Second) * int64(den) / int64(num))
	}
	if t+g.frame/2 < g.target {
		return nil
	}
	if g.ref != nil && g.ref.self {
		l, rr := splitFramed(p, g.ref.layout)
		if g.ref.swap {
			l, rr = rr, l
		}
		l.PTS, rr.PTS = p.pts, p.pts
		g.ref.setSelf(l, rr, t)
		return g.show(ctx, l, t)
	}
	return g.show(ctx, pictureFrame8(p), t)
}

// show makes a picture's pair at the grade of its time and sends it, then
// holds while the film is paused: the pair made again at each change of
// the grade, sent again at each picture's time to keep the stream alive.
// It returns an errSeek when the film is wanted elsewhere.
func (g *grader) show(ctx context.Context, f *mvc.Frame, t time.Duration) error {
	cuts := g.conv.Cuts()
	e, err := g.conv.Start(f).Wait()
	if err != nil {
		return fmt.Errorf("--3d-grade: the depth: %w", err)
	}
	if g.conv.Cuts() > cuts {
		g.noteCut(t)
	}
	l, rr, s := g.conv.Eyes(e)
	g.conv.Window(l, rr, s)
	g.setPos(t, e)
	ours := [2]*mvc.Frame{l, rr}
	shown := g.refOrOurs(ctx, t, ours)
	if err := g.emit(ctx, shown[0], shown[1]); err != nil {
		return err
	}
	for {
		if g.ref != nil && g.ref.alignAsked() {
			g.ref.align(ctx, f, t)
			g.changed()
		}
		g.mu.Lock()
		w := g.want
		g.want.regrade = false
		switch {
		case w.seek != nil:
			g.want.seek = nil
			g.want.steps = 0
		case !w.playing && w.steps > 0:
			g.want.steps--
		}
		g.mu.Unlock()
		switch {
		case w.seek != nil:
			return &errSeek{*w.seek}
		case w.playing, w.steps > 0:
			return nil
		case w.regrade:
			k := g.knobsAt(t)
			ours[0], ours[1], _ = g.conv.Grade(e, g.shape(k), k.Flat)
			g.setPos(t, e)
			shown = g.refOrOurs(ctx, t, ours)
			if err := g.emit(ctx, shown[0], shown[1]); err != nil {
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-g.wake:
		case <-time.After(time.Until(g.next)):
			if err := g.emit(ctx, shown[0], shown[1]); err != nil {
				return err
			}
		}
	}
}

// refOrOurs is the pair to show at a time of the film by the view asked:
// ours; the reference's when it has one there; mix, the right eye a
// blend of the two right eyes (where the depths agree it fuses, where
// not it doubles by the difference); diff, both eyes the difference of
// the two right eyes, grey where they agree.
func (g *grader) refOrOurs(ctx context.Context, t time.Duration, ours [2]*mvc.Frame) [2]*mvc.Frame {
	if g.ref == nil {
		return ours
	}
	view := g.ref.view()
	if view == viewOurs {
		g.ref.setShowing(false)
		return ours
	}
	l, r, ok := g.ref.at(ctx, t, g.frameOr())
	if !ok || l.Width != ours[0].Width || l.Height != ours[0].Height {
		g.ref.setShowing(false)
		return ours
	}
	g.ref.setShowing(true)
	switch view {
	case viewMix:
		return [2]*mvc.Frame{ours[0], blendFrames(r, ours[1])}
	case viewDiff:
		d := diffFrames(r, ours[1])
		return [2]*mvc.Frame{d, d}
	}
	return [2]*mvc.Frame{l, r}
}

// blendFrames is two pictures of a size half each.
func blendFrames(a, b *mvc.Frame) *mvc.Frame {
	w, h := a.Width, a.Height
	cw, ch := (w+1)/2, (h+1)/2
	out := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: cw, PTS: a.PTS,
		Y: make([]byte, w*h), Cb: make([]byte, cw*ch), Cr: make([]byte, cw*ch)}
	mix := func(dst []byte, pa, pb []byte, sa, sb, pw, ph, stride int) {
		for y := range ph {
			ra, rb, ro := pa[y*sa:y*sa+pw], pb[y*sb:y*sb+pw], dst[y*stride:(y+1)*stride]
			for x := range pw {
				ro[x] = byte((int(ra[x]) + int(rb[x]) + 1) >> 1)
			}
		}
	}
	mix(out.Y, a.Y, b.Y, a.StrideY, b.StrideY, w, h, w)
	mix(out.Cb, a.Cb, b.Cb, a.StrideC, b.StrideC, cw, ch, cw)
	mix(out.Cr, a.Cr, b.Cr, a.StrideC, b.StrideC, cw, ch, cw)
	return out
}

// diffFrames is the difference of two pictures' luma, four times and
// about mid grey, the chroma neutral.
func diffFrames(a, b *mvc.Frame) *mvc.Frame {
	w, h := a.Width, a.Height
	cw, ch := (w+1)/2, (h+1)/2
	out := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: cw, PTS: a.PTS,
		Y: make([]byte, w*h), Cb: make([]byte, cw*ch), Cr: make([]byte, cw*ch)}
	for y := range h {
		ra, rb, ro := a.Y[y*a.StrideY:y*a.StrideY+w], b.Y[y*b.StrideY:y*b.StrideY+w], out.Y[y*w:(y+1)*w]
		for x := range w {
			ro[x] = byte(min(max(128+4*(int(ra[x])-int(rb[x])), 16), 235))
		}
	}
	for i := range out.Cb {
		out.Cb[i], out.Cr[i] = 128, 128
	}
	return out
}

// emit encodes a pair into the stream, at the stream's pace.
func (g *grader) emit(ctx context.Context, l, rr *mvc.Frame) error {
	if g.enc == nil {
		cfg := gpu.Config{Codec: gpu.H264, Width: 2 * l.Width, Height: l.Height, FPSNum: g.num, FPSDen: g.den,
			QP: gradeQP, GOP: gradeGOP, LowLatency: true}
		enc, err := gpu.Open(gpu.NVENC, cfg, gradeWriter{g})
		if err != nil {
			return fmt.Errorf("--3d-grade: the stream's encoder (NVENC): %w", err)
		}
		g.enc = enc
		g.next = time.Now()
		g.mu.Lock()
		g.width, g.height = cfg.Width, cfg.Height
		g.mu.Unlock()
	}
	if d := time.Until(g.next); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	} else if -d > time.Second/2 {
		g.next = time.Now() // behind: no burst to catch up
	}
	g.next = g.next.Add(g.frame)
	g.last = [2]*mvc.Frame{l, rr}
	sf := &mvc.StereoFrame{Base: l, Dependent: rr}
	return g.enc.Encode(func(dst *gpu.Picture) { drawFramed(dst, sf, false, LayoutFullSBS) })
}

// gradeWriter takes the encoder's access units into the stream.
type gradeWriter struct{ g *grader }

func (w gradeWriter) Write(au []byte) (int, error) {
	g := w.g
	pts := 90000 + g.outPTS
	g.outPTS += 90000 * int64(g.den) / int64(g.num)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, params := bdmv.AnnexBKey(bdmv.CodingAVC, au); params != nil {
		g.params = params
	}
	key, _ := bdmv.AnnexBKey(bdmv.CodingAVC, au)
	for c := range g.clients {
		if c.dead {
			continue
		}
		if err := c.sink(au, pts, key); err != nil {
			c.dead = true
			close(c.ch)
		}
	}
	return len(au), nil
}

// chanWriter is a client's half of the stream: its packets queued for its
// connection, or dropped with it when it cannot keep up.
type chanWriter struct{ ch chan []byte }

func (w chanWriter) Write(b []byte) (int, error) {
	select {
	case w.ch <- append([]byte(nil), b...):
		return len(b), nil
	default:
		return 0, errors.New("the player is not keeping up")
	}
}

// shotShape is the converter's Shot: the grade at a picture's time.
func (g *grader) shotShape(pts int64) (stereo.Shape, bool) {
	k := g.knobsAt(g.src.clock(pts))
	return g.shape(k), k.Flat
}

// shape is the knobs as the conversion's shape.
func (g *grader) shape(k gradeKnobs) stereo.Shape {
	o := g.r.Opts
	o.Depth3D, o.Screen3D = k.Depth, k.Screen
	return o.shape3D(g.model.Size())
}

// defaults are the command's knobs, where no stretch says otherwise.
func (g *grader) defaults() gradeKnobs {
	return gradeKnobs{Depth: g.r.Opts.depth3D(), Screen: g.r.Opts.Screen3D}
}

// knobsAt is the grade at a time of the film.
func (g *grader) knobsAt(t time.Duration) gradeKnobs {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.knobsAtLocked(t)
}

func (g *grader) knobsAtLocked(t time.Duration) gradeKnobs {
	k := g.defaults()
	if i := shotAt(g.shots, t); i >= 0 {
		s := g.shots[i]
		if s.Depth > 0 {
			k.Depth = s.Depth
		}
		if s.Screen != nil {
			k.Screen = *s.Screen
		}
		k.Flat = s.Flat
	}
	return k
}

// setPos records the picture shown and its disparity's extremes.
func (g *grader) setPos(t time.Duration, e *stereo.Estimate) {
	k := g.knobsAt(t)
	near, far := depthExtremes(e.Depth(), g.shape(k), k.Flat)
	g.mu.Lock()
	g.pos, g.near, g.far = t, near, far
	g.mu.Unlock()
	g.changed()
}

// depthExtremes is where a depth's nearest and farthest come out under a
// shape, in percent of the width (positive in front of the screen): its
// 99th and 1st percentiles.
func depthExtremes(d []float32, s stereo.Shape, flat bool) (near, far float64) {
	if len(d) == 0 || flat {
		return 0, 0
	}
	sample := make([]float32, 0, len(d)/7+1)
	for i := 0; i < len(d); i += 7 {
		sample = append(sample, d[i])
	}
	slices.Sort(sample)
	at := func(p float64) float64 {
		v := float64(sample[min(int(p*float64(len(sample))), len(sample)-1)])
		return 100 * (s.Scale*v + s.Offset)
	}
	return at(0.99), at(0.01)
}

// noteCut records a cut at a time of the film (once).
func (g *grader) noteCut(t time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	i := sort.Search(len(g.cuts), func(i int) bool { return g.cuts[i] >= t })
	if i < len(g.cuts) && g.cuts[i]-t < g.frame/2 || i > 0 && t-g.cuts[i-1] < g.frame/2 {
		return
	}
	g.cuts = slices.Insert(g.cuts, i, t)
}

// setKnobs is a turn of the knobs at the picture shown: the stretch
// there takes them, or a new one from the last cut (or the picture,
// when no cut is near) to the next stretch.
func (g *grader) setKnobs(k gradeKnobs) {
	g.mu.Lock()
	defer g.mu.Unlock()
	t := g.pos
	start, ramp := t, gradeRamp
	if i := shotAt(g.shots, t); i >= 0 && t-g.shots[i].Start <= gradeSnap {
		start, ramp = g.shots[i].Start, g.shots[i].Ramp
	} else if j := sort.Search(len(g.cuts), func(i int) bool { return g.cuts[i] > t }); j > 0 && t-g.cuts[j-1] <= gradeSnap {
		start, ramp = g.cuts[j-1], 0
	}
	// the stretch that runs over start ends there
	for i := range g.shots {
		if s := &g.shots[i]; s.Start < start && (s.End == 0 || s.End > start) {
			s.End = start
		}
	}
	i := sort.Search(len(g.shots), func(i int) bool { return g.shots[i].Start >= start })
	if i == len(g.shots) || g.shots[i].Start != start {
		s := shot3D{Start: start, Ramp: ramp}
		if i < len(g.shots) {
			s.End = g.shots[i].Start
		}
		g.shots = slices.Insert(g.shots, i, s)
	}
	screen := k.Screen
	g.shots[i].Depth, g.shots[i].Screen, g.shots[i].Flat = k.Depth, &screen, k.Flat
	g.want.regrade = true
	g.saved = false
	g.wakeUp()
	_ = g.saveLocked()
}

// deleteShot removes stretch i; the one before it runs on to its end.
func (g *grader) deleteShot(i int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if i < 0 || i >= len(g.shots) {
		return
	}
	s := g.shots[i]
	if i > 0 && g.shots[i-1].End == s.Start {
		g.shots[i-1].End = s.End
	}
	g.shots = slices.Delete(g.shots, i, i+1)
	g.want.regrade = true
	g.saved = false
	g.wakeUp()
	_ = g.saveLocked()
}

// saveLocked writes the grade to the output file.
func (g *grader) saveLocked() error {
	if g.saved {
		return nil
	}
	if err := writeShots3D(g.r.Opts.Output, g.shots); err != nil {
		g.r.Report.Report("--3d-grade: writing %s: %v", g.r.Opts.Output, err)
		return err
	}
	g.saved = true
	return nil
}

// wakeUp has the player look at what is wanted; changed tells the pages.
func (g *grader) wakeUp() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
	g.ver++
	for ch := range g.events {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (g *grader) changed() {
	g.mu.Lock()
	g.wakeUp()
	g.mu.Unlock()
}

// gradeState is what a page sees.
type gradeState struct {
	Ver      int            `json:"ver"`
	T        float64        `json:"t"`
	Length   float64        `json:"length"`
	Frame    float64        `json:"frame"`
	Playing  bool           `json:"playing"`
	Knobs    gradeKnobs     `json:"knobs"`
	Defaults gradeKnobs     `json:"defaults"`
	Stretch  int            `json:"stretch"`
	Shots    []gradeShot    `json:"shots"`
	Cuts     []float64      `json:"cuts"`
	Near     float64        `json:"near"`
	Far      float64        `json:"far"`
	Clients  int            `json:"clients"`
	Saved    bool           `json:"saved"`
	File     string         `json:"file"`
	Width    int            `json:"width"`
	Title    string         `json:"title"`
	Ref      *gradeRefState `json:"ref"`
}

// gradeRefState is the reference's state as a page sees it.
type gradeRefState struct {
	File     string  `json:"file"`
	On       bool    `json:"on"` // the view is not ours
	View     string  `json:"view"`
	Offset   float64 `json:"offset"` // seconds the reference runs ahead of the film
	Aligned  bool    `json:"aligned"`
	Aligning bool    `json:"aligning"`
	Showing  bool    `json:"showing"` // its pair is the one on screen
}

type gradeShot struct {
	Start  float64  `json:"start"`
	End    float64  `json:"end"`
	Depth  float64  `json:"depth"`
	Screen *float64 `json:"screen"`
	Flat   bool     `json:"flat"`
	Ramp   float64  `json:"ramp"`
}

func (g *grader) state() gradeState {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := gradeState{Ver: g.ver, T: g.pos.Seconds(), Length: g.src.length.Seconds(), Frame: g.frameOr().Seconds(),
		Playing: g.want.playing, Knobs: g.knobsAtLocked(g.pos), Defaults: g.defaults(), Stretch: shotAt(g.shots, g.pos),
		Near: g.near, Far: g.far, Saved: g.saved, File: g.r.Opts.Output, Title: g.r.Opts.Input, Width: g.width / 2,
		Shots: []gradeShot{}, Cuts: []float64{}}
	for _, s := range g.shots {
		st.Shots = append(st.Shots, gradeShot{Start: s.Start.Seconds(), End: s.End.Seconds(), Depth: s.Depth, Screen: s.Screen, Flat: s.Flat, Ramp: s.Ramp})
	}
	for _, c := range g.cuts {
		st.Cuts = append(st.Cuts, c.Seconds())
	}
	for c := range g.clients {
		if !c.dead {
			st.Clients++
		}
	}
	if g.ref != nil {
		st.Ref = g.ref.state()
	}
	return st
}

// gradeCommand is what the page asks: op play, pause, toggle, seek (t,
// seconds), step (n pictures, negative back), jump (dir -1/1: the cut
// before/after), knobs (knobs), reset (the stretch to the defaults),
// delete (i: a stretch), save, ref (view: ours, disc, mix or diff; or n
// 1 for disc, 0 for ours), align (the reference aligned to the film by
// their pictures) or refoffset (t: seconds the reference runs ahead).
type gradeCommand struct {
	Op    string      `json:"op"`
	T     float64     `json:"t"`
	N     int         `json:"n"`
	Dir   int         `json:"dir"`
	I     int         `json:"i"`
	Knobs *gradeKnobs `json:"knobs"`
	View  string      `json:"view"`
}

func (g *grader) command(c gradeCommand) error {
	seek := func(t time.Duration) {
		t = max(t, 0)
		g.want.seek = &t
	}
	g.mu.Lock()
	switch c.Op {
	case "play":
		g.want.playing = true
	case "pause":
		g.want.playing = false
	case "toggle":
		g.want.playing = !g.want.playing
	case "seek":
		seek(time.Duration(c.T * float64(time.Second)))
	case "step":
		g.want.playing = false
		if c.N >= 0 {
			g.want.steps += max(c.N, 1)
		} else {
			seek(g.pos + time.Duration(c.N)*g.frameOr())
		}
	case "jump":
		t := g.pos
		if c.Dir < 0 {
			if i := sort.Search(len(g.cuts), func(i int) bool { return g.cuts[i] >= t-g.frameOr()/2 }); i > 0 {
				seek(g.cuts[i-1])
			}
		} else {
			i := sort.Search(len(g.cuts), func(i int) bool { return g.cuts[i] > t+g.frameOr()/2 })
			if i < len(g.cuts) {
				seek(g.cuts[i])
			}
		}
	case "knobs":
		if c.Knobs == nil {
			g.mu.Unlock()
			return errors.New("knobs: none given")
		}
		k := *c.Knobs
		if k.Depth <= 0 || k.Depth > 5 || k.Screen < -5 || k.Screen > 5 {
			g.mu.Unlock()
			return errors.New("knobs: depth above 0 to 5, screen -5 to 5")
		}
		g.mu.Unlock()
		g.setKnobs(k)
		return nil
	case "reset":
		g.mu.Unlock()
		g.setKnobs(g.defaults())
		return nil
	case "delete":
		g.mu.Unlock()
		g.deleteShot(c.I)
		return nil
	case "save":
		err := g.saveLocked()
		g.mu.Unlock()
		return err
	case "ref", "align", "refoffset":
		g.mu.Unlock()
		if g.ref == nil {
			return errors.New("no --3d-reference to compare with")
		}
		switch c.Op {
		case "ref":
			v := viewOurs
			if c.N > 0 {
				v = viewDisc
			}
			if c.View != "" {
				v = c.View
			}
			if v != viewOurs && v != viewDisc && v != viewMix && v != viewDiff {
				return fmt.Errorf("view %q: want ours, disc, mix or diff", v)
			}
			g.ref.set(v, nil)
		case "align":
			g.ref.set(viewDisc, nil)
			g.ref.askAlign()
		case "refoffset":
			off := time.Duration(c.T * float64(time.Second))
			g.ref.set(viewDisc, &off)
		}
		g.mu.Lock()
		g.want.regrade = true
		g.wakeUp()
		g.mu.Unlock()
		return nil
	default:
		g.mu.Unlock()
		return fmt.Errorf("op %q: unknown", c.Op)
	}
	g.wakeUp()
	g.mu.Unlock()
	return nil
}

// handler is the page, its events and commands, and the stream.
func (g *grader) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(grade3DPage)
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(g.state())
	})
	mux.HandleFunc("POST /api", func(w http.ResponseWriter, r *http.Request) {
		var c gradeCommand
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := g.command(c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /events", g.serveEvents)
	mux.HandleFunc("GET /stream.ts", g.serveStream)
	mux.HandleFunc("GET /stream.mp4", g.serveMP4)
	mux.HandleFunc("GET /stream.json", func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		params, width, height := g.params, g.width, g.height
		g.mu.Unlock()
		sps, _, ok := avcParams(params)
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"ready":false}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "mime": `video/mp4; codecs="` + mp4.AVCCodec(sps) + `"`,
			"width": width, "height": height})
	})
	mux.HandleFunc("GET /quest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(grade3DQuestPage)
	})
	return mux
}

// serveEvents sends the state as server-sent events, at each change.
func (g *grader) serveEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := make(chan struct{}, 1)
	g.mu.Lock()
	g.events[ch] = struct{}{}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.events, ch)
		g.mu.Unlock()
	}()
	sent := -1
	for {
		st := g.state()
		if st.Ver != sent {
			b, _ := json.Marshal(st)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
			fl.Flush()
			sent = st.Ver
		}
		select {
		case <-r.Context().Done():
			return
		case <-ch:
		case <-time.After(15 * time.Second):
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// serveStream sends the live transport stream, from the next keyframe.
func (g *grader) serveStream(w http.ResponseWriter, r *http.Request) {
	g.serveClient(w, r, "video/MP2T", (*gradeClient).tsSink)
}

// serveMP4 sends the live fragmented MP4, from the next keyframe.
func (g *grader) serveMP4(w http.ResponseWriter, r *http.Request) {
	g.serveClient(w, r, "video/mp4", (*gradeClient).mp4Sink)
}

// serveClient sends the stream in one of its forms.
func (g *grader) serveClient(w http.ResponseWriter, r *http.Request, mime string, sink func(*gradeClient, []byte, int, int)) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", http.StatusInternalServerError)
		return
	}
	c := &gradeClient{ch: make(chan []byte, gradeJoinWait)}
	g.mu.Lock()
	sink(c, g.params, g.width, g.height)
	g.clients[c] = struct{}{}
	g.wakeUp()
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.clients, c)
		g.wakeUp()
		g.mu.Unlock()
	}()
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case b, ok := <-c.ch:
			if !ok {
				return
			}
			if _, err := w.Write(b); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// reachable is a listener's address as another machine reaches it: an
// unspecified host ([::] or 0.0.0.0) replaced by this machine's first
// address that is not loopback.
func reachable(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
		return addr
	}
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		return "localhost:" + port
	}
	for _, a := range ifaces {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return net.JoinHostPort(n.IP.String(), port)
		}
	}
	return "localhost:" + port
}

// matroskaLive is a Matroska or MP4 source read in places for a grading.
func (r *Runner) matroskaLive(src *mkvSource, sel Selection) (liveSource, error) {
	// the file's first block, the clock's zero (as a conversion's)
	g0 := newMkvDemux(src, Selection{Base: sel.Base}, "", r.Report)
	if err := g0.start(); err != nil {
		return liveSource{}, err
	}
	_, _, _, err := g0.Next()
	_ = g0.f.Close()
	if err != nil {
		return liveSource{}, fmt.Errorf("--3d-grade: %w", err)
	}
	t0 := g0.t0
	feed, done, err := r.matroskaFeedN(src, sel, 0)
	if err != nil {
		return liveSource{}, fmt.Errorf("--3d-grade: %w", err)
	}
	return liveSource{video: sel.Base, length: src.duration, feed: feed, close: done,
		clock: func(pts int64) time.Duration { return ticks90k(pts) - t0 }}, nil
}

// discLive is a disc title read in places for a grading.
func (r *Runner) discLive(src *goSource, sel Selection) (liveSource, error) {
	feed, done, err := r.discFeedN(src, sel, 0, true)
	if err != nil {
		return liveSource{}, fmt.Errorf("--3d-grade: %w", err)
	}
	g := newGoDemux(src, sel, "", r.Report)
	return liveSource{video: sel.Base, length: r.length, feed: feed, close: done, clock: g.timeline}, nil
}
