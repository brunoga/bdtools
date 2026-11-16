package convert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/sam"
	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// Painting an object in --3d-grade: the page gives hints on the picture
// it holds (a box around the object, points on it and off it), SAM 2
// (internal/sam) makes the object's mask from them at once, and tracking
// follows it through its shot, back to the cut before and on to the cut
// after. The object's push then moves its disparity (objects3d.go).

// samNet is SAM 2.1's tiny model (Meta, Apache-2.0), downloaded into the
// user's cache the first time an object is painted.
var samNet = depthNet{"SAM 2.1 tiny", "sam2.1_hiera_tiny.pt",
	"https://dl.fbaipublicfiles.com/segment_anything_2/092824/sam2.1_hiera_tiny.pt",
	"7402e0d864fa82708a20fbd15bc84245c2f26dff0eb43a4b5b93452deb34be69", "156 MB", ""}

const (
	trackBack  = 10 * time.Second // how far back of the painted picture tracking goes at most
	trackAhead = 30 * time.Second // and on
	trackGone  = 2 * time.Second  // how long the object may be gone before tracking stops
	// trackFrames is the tracker's shot length: the painted picture is
	// trackFrames/2 in it, the pictures before and after either side.
	trackFrames = 1 << 21
)

// samModel is SAM 2 on a backend, downloaded the first time.
func (r *Runner) samModel(be nn.Backend) (*sam.Model, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("--3d-grade has no cache directory for the object finder: %w", err)
	}
	file := filepath.Join(dir, "bdtools", samNet.file)
	if _, err := os.Stat(file); err != nil {
		r.Report.Report("--3d-grade: downloading the object finder (%s, %s) to %s", samNet.name, samNet.size, file)
		if err := download(samNet.url, file, samNet.sha256); err != nil {
			return nil, fmt.Errorf("--3d-grade could not download the object finder: %w", err)
		}
	}
	m, err := sam.Load(file)
	if err != nil {
		return nil, err
	}
	m.Use(be)
	return m, nil
}

// gradePaint is an object being painted: the picture it is painted on,
// the hints and the mask they make.
type gradePaint struct {
	Obj    int          `json:"obj"`    // the object's number, 0 a new one
	At     float64      `json:"at"`     // the picture's time, seconds
	Box    []float64    `json:"box"`    // x0, y0, x1, y1 over the picture, 0 to 1 (nil: none)
	Points [][3]float64 `json:"points"` // x, y over the picture (0 to 1), and 1 on the object or 0 off it
	Found  bool         `json:"found"`  // the hints make a mask
	at     time.Duration
	mask   []byte // ObjectSide squared, 0 or 255
}

// gradeObject is an object as the page sees it.
type gradeObject struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Push  float64 `json:"push"`
	Masks int     `json:"masks"`
}

// gradeTracking is a tracking under way, for the page.
type gradeTracking struct {
	Done int     `json:"done"`
	At   float64 `json:"at"` // the picture tracked last, seconds
}

// errTrack is show's: the object painted is to be tracked.
type errTrack struct{}

func (errTrack) Error() string { return "track the object painted" }

// paintCommand takes a paint, track, object or select command (with g.mu
// held). It reports whether it knows the op.
func (g *grader) paintCommand(c gradeCommand) (bool, error) {
	switch c.Op {
	case "paint":
		if c.Box != nil && (len(c.Box) != 4 || c.Box[2] <= c.Box[0] || c.Box[3] <= c.Box[1]) {
			return true, errors.New("a box is x0, y0, x1, y1, from 0 to 1, its corners apart")
		}
		for _, v := range append(append([]float64(nil), c.Box...), flatten(c.Points)...) {
			if v < 0 || v > 1 {
				return true, errors.New("hints are over the picture, from 0 to 1")
			}
		}
		if c.I != 0 && g.objectIndex(c.I) < 0 {
			return true, fmt.Errorf("there is no object %d", c.I)
		}
		g.want.playing = false
		g.paint = &gradePaint{Obj: c.I, Box: c.Box, Points: c.Points, At: g.pos.Seconds(), at: g.pos}
		g.selected = c.I
		g.want.paint = true
	case "unpaint":
		g.paint = nil
		g.want.regrade = true
	case "track":
		if g.paint == nil || !g.paint.Found {
			return true, errors.New("paint an object first: its hints make no mask")
		}
		if g.tracking != nil {
			return true, errors.New("an object is being tracked already")
		}
		g.want.playing = false
		g.want.track = true
	case "object":
		i := g.objectIndex(c.I)
		if i < 0 {
			return true, fmt.Errorf("there is no object %d", c.I)
		}
		switch {
		case c.Delete:
			delete(g.masks, c.I)
			g.objects = append(g.objects[:i], g.objects[i+1:]...)
			if g.selected == c.I {
				g.selected = 0
			}
		case c.Push != nil:
			if *c.Push < -5 || *c.Push > 5 {
				return true, errors.New("an object's push is -5 to 5 percent of the width")
			}
			g.objects[i].Push = *c.Push
		}
		g.saved = false
		_ = g.saveLocked()
		g.want.regrade = true
	case "select":
		if c.I != 0 && g.objectIndex(c.I) < 0 {
			return true, fmt.Errorf("there is no object %d", c.I)
		}
		g.selected = c.I
		g.want.regrade = true
	default:
		return false, nil
	}
	g.wakeUp()
	return true, nil
}

func flatten(p [][3]float64) []float64 {
	var out []float64
	for _, v := range p {
		out = append(out, v[0], v[1])
	}
	return out
}

// objectIndex is an object's place in g.objects, -1 for none.
func (g *grader) objectIndex(id int) int {
	for i, o := range g.objects {
		if o.ID == id {
			return i
		}
	}
	return -1
}

// objectsState is the objects for the page.
func (g *grader) objectsState() []gradeObject {
	out := []gradeObject{}
	for _, o := range g.objects {
		n := 0
		if tr := g.masks[o.ID]; tr != nil {
			n = len(tr.at)
		}
		out = append(out, gradeObject{ID: o.ID, Start: o.Start.Seconds(), End: o.End.Seconds(), Push: o.Push, Masks: n})
	}
	return out
}

// sam is SAM 2 on the converter's backend, loaded the first time.
func (g *grader) samNet() (*sam.Model, error) {
	if g.sam == nil {
		m, err := g.r.samModel(g.conv.Model.Backend())
		if err != nil {
			return nil, err
		}
		g.sam = m
	}
	return g.sam, nil
}

// encodeKept is a picture's features, kept on the backend (freeKept
// gives them back).
func (g *grader) encodeKept(p sam.Picture) *sam.Features {
	be := g.sam.Backend()
	f := g.sam.Encode(be.Upload(p.Tensor()))
	for _, t := range []*nn.Tensor{f.Vision, f.High0, f.High1} {
		be.Keep(t)
	}
	f.Stages = [4]*nn.Tensor{}
	return f
}

func (g *grader) freeKept(f *sam.Features) {
	if f == nil {
		return
	}
	be := g.sam.Backend()
	for _, t := range []*nn.Tensor{f.Vision, f.High0, f.High1} {
		be.Free(t)
	}
}

// hints is a paint's hints in the network's frame.
func (p *gradePaint) hints() (*sam.Box, []sam.Point) {
	var box *sam.Box
	if p.Box != nil {
		box = &sam.Box{X0: float32(p.Box[0] * sam.Size), Y0: float32(p.Box[1] * sam.Size), X1: float32(p.Box[2] * sam.Size), Y1: float32(p.Box[3] * sam.Size)}
	}
	var pts []sam.Point
	for _, v := range p.Points {
		pts = append(pts, sam.Point{X: float32(v[0] * sam.Size), Y: float32(v[1] * sam.Size), Positive: v[2] > 0})
	}
	return box, pts
}

// binaryMask is a mask's logits as an object's mask: 255 where over 0.
func binaryMask(lg []float32) []byte {
	m := make([]byte, len(lg))
	for i, v := range lg {
		if v > 0 {
			m[i] = 255
		}
	}
	return m
}

// paintPreview makes the mask of the object being painted on the picture
// held (f, at t), from its hints. It runs on the player's goroutine.
func (g *grader) paintPreview(f *mvc.Frame, t time.Duration) {
	g.mu.Lock()
	p := g.paint
	g.mu.Unlock()
	if p == nil {
		return
	}
	mask, err := g.paintMask(f, t, p)
	if err != nil {
		g.r.Report.Report("--3d-grade could not find the object: %v", err)
	}
	g.mu.Lock()
	if g.paint == p {
		p.mask, p.Found = mask, mask != nil
	}
	g.mu.Unlock()
	g.changed()
}

func (g *grader) paintMask(f *mvc.Frame, t time.Duration, p *gradePaint) ([]byte, error) {
	m, err := g.samNet()
	if err != nil {
		return nil, err
	}
	box, pts := p.hints()
	if box == nil && len(pts) == 0 {
		return nil, nil
	}
	if g.paintFeats == nil || g.paintFeatsAt != t {
		g.freeKept(g.paintFeats)
		g.paintFeats = g.encodeKept(sam.NewPicture(f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC))
		g.paintFeatsAt = t
	}
	tr := m.NewTracker(trackFrames)
	defer tr.Close()
	defer m.Backend().Release()
	mk, obj, err := tr.Prompt(trackFrames/2, g.paintFeats, box, pts)
	if err != nil || obj <= 0 {
		return nil, err
	}
	return binaryMask(mk.Logits), nil
}

// overlay is the mask shown over the picture at t: the object being
// painted on it, else the object selected.
func (g *grader) overlay(t time.Duration) []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p := g.paint; p != nil && p.mask != nil && absDur(p.at-t) < g.frameOr()/2 {
		return p.mask
	}
	if g.selected != 0 {
		if tr := g.masks[g.selected]; tr != nil {
			return tr.near(t, g.frameOr()/2)
		}
	}
	return nil
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// tint shows a mask over pictures, in place (stereo.TintMask).
func tint(mask []byte, frames ...*mvc.Frame) {
	for _, f := range frames {
		stereo.TintMask(mask, f)
	}
}

// track follows the object painted through its shot: from the cut before
// the painted picture (trackBack at most) to the cut after it (trackAhead
// at most), the pictures before it tracked backwards from it once it is
// reached, either way only until it has been gone for trackGone. The
// object spans the pictures it was found on. Its masks become the object's (a new one's, or replacing the
// painted object's there). It runs on the player's goroutine, showing
// each picture with its mask as it goes.
func (g *grader) track(ctx context.Context) error {
	g.mu.Lock()
	p := g.paint
	g.want.track = false
	if p == nil || p.mask == nil {
		g.mu.Unlock()
		return nil
	}
	t := p.at
	from := max(t-trackBack, 0)
	if i := sort.Search(len(g.cuts), func(i int) bool { return g.cuts[i] > t+g.frameOr()/2 }); i > 0 && g.cuts[i-1] > from {
		from = g.cuts[i-1]
	}
	g.tracking = &gradeTracking{}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.tracking = nil
		g.mu.Unlock()
		g.changed()
	}()
	m, err := g.samNet()
	if err != nil {
		return err
	}
	be := m.Backend()
	box, pts := p.hints()
	tr := m.NewTracker(trackFrames)
	defer tr.Close()
	found := &maskTrack{}
	type before struct {
		at  time.Duration
		pic sam.Picture
		f   *mvc.Frame
	}
	var back []before
	var cuts stereo.CutDetector
	half := g.frameOr() / 2
	reached, k := false, 0
	var gone time.Duration // how long the object has been gone, going forwards
	show := func(f *mvc.Frame, at time.Duration, mask []byte) error {
		found.set(at, mask)
		l, rr := copyFrame(f), copyFrame(f)
		tint(mask, l, rr)
		g.mu.Lock()
		g.tracking.Done++
		g.tracking.At = at.Seconds()
		g.note, g.noteAt = fmt.Sprintf("tracking: %d pictures", g.tracking.Done), time.Now()
		g.mu.Unlock()
		g.changed()
		return g.emit(ctx, l, rr)
	}
	step := func(idx int, pic sam.Picture) ([]byte, error) {
		f := m.Encode(be.Upload(pic.Tensor()))
		mk, obj, err := tr.Track(idx, f)
		be.Release()
		if err != nil {
			return nil, err
		}
		if obj <= 0 {
			return make([]byte, len(mk.Logits)), nil
		}
		return binaryMask(mk.Logits), nil
	}
	errDone := errors.New("the shot's end")
	next, done, err := g.seek(from)
	if err != nil {
		return err
	}
	defer done()
	if next == nil {
		return errors.New("nothing can be read there")
	}
	src, err := g.pictures(next)
	if err != nil {
		return err
	}
	err = src.run(func(pic picture) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		pic, err := g.r.transform(pic, src)
		if err != nil {
			return err
		}
		at := g.src.clock(pic.pts)
		if at+half < from {
			return nil
		}
		f := g.converted(pic)
		cut := cuts.Next(f)
		sp := sam.NewPicture(f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
		switch {
		case at+half < t:
			if cut {
				back = back[:0]
			}
			back = append(back, before{at, sp, f})
			return nil
		case !reached:
			reached = true
			if cut {
				back = nil
			}
			feats := m.Encode(be.Upload(sp.Tensor()))
			mk, _, err := tr.Prompt(trackFrames/2, feats, box, pts)
			be.Release()
			if err != nil {
				return err
			}
			if err := show(f, at, binaryMask(mk.Logits)); err != nil {
				return err
			}
			tr.Reverse = true
			var bgone time.Duration
			for j := len(back) - 1; j >= 0 && bgone < trackGone; j-- {
				mask, err := step(trackFrames/2-(len(back)-j), back[j].pic)
				if err != nil {
					return err
				}
				if err := show(back[j].f, back[j].at, mask); err != nil {
					return err
				}
				bgone = absentFor(mask, bgone, g.frameOr())
				back[j] = before{}
			}
			back, tr.Reverse = nil, false
			return nil
		case cut || at > t+trackAhead || gone >= trackGone:
			return errDone
		}
		k++
		mask, err := step(trackFrames/2+k, sp)
		if err != nil {
			return err
		}
		gone = absentFor(mask, gone, g.frameOr())
		return show(f, at, mask)
	})
	if err != nil && !errors.Is(err, errDone) && !errors.Is(err, io.EOF) {
		return err
	}
	// the object spans the pictures it is on
	first, last := -1, -1
	for i, m := range found.masks {
		if !empty(m) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return errors.New("the object was not found on any picture")
	}
	found.at, found.masks = found.at[first:last+1], found.masks[first:last+1]
	g.mu.Lock()
	defer g.mu.Unlock()
	start, end := found.at[0], found.at[len(found.at)-1]+g.frameOr()
	id := p.Obj
	if i := g.objectIndex(id); i >= 0 {
		g.objects[i].Start, g.objects[i].End = min(g.objects[i].Start, start), max(g.objects[i].End, end)
	} else {
		id = 1
		for _, o := range g.objects {
			id = max(id, o.ID+1)
		}
		g.objects = append(g.objects, object3D{ID: id, Start: start, End: end})
	}
	tr0 := g.masks[id]
	if tr0 == nil {
		tr0 = &maskTrack{}
		g.masks[id] = tr0
	}
	for i, at := range found.at {
		tr0.set(at, found.masks[i])
	}
	g.selected, g.paint = id, nil
	g.saved = false
	_ = g.saveLocked()
	g.r.Report.Report("--3d-grade: object %d tracked through %d pictures, %s to %s", id, len(found.at), clock(start), clock(end))
	return nil
}

// converted is the picture the grader converts of a decoded one: its own
// left eye, for a 3D file graded against itself.
func (g *grader) converted(p picture) *mvc.Frame {
	if g.ref != nil && g.ref.self {
		l, rr := splitFramed(p, g.ref.layout)
		if g.ref.swap {
			l = rr
		}
		return l
	}
	return pictureFrame8(p)
}

// absentFor is how long the object has been gone after a picture of
// frame with its mask, gone before it.
func absentFor(mask []byte, gone, frame time.Duration) time.Duration {
	if empty(mask) {
		return gone + frame
	}
	return 0
}

func empty(mask []byte) bool {
	for _, v := range mask {
		if v != 0 {
			return false
		}
	}
	return true
}
