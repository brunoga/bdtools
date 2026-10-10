package convert

import (
	"github.com/brunoga/bdtools/internal/dovi"
	"github.com/brunoga/bdtools/internal/gpu"
)

// A Dolby Vision full enhancement layer (FEL), composed into the picture.
//
// The enhancement layer (a disc's PID 0x1015, or the NAL units Matroska
// carries behind type 63 headers) decodes on a decoder of its own, a step
// ahead of the base layer, and each of its pictures waits by timestamp for
// the base layer's. The composition (see dovi.Composer) is the picture the
// enhancement layer is meant to give, coded like the HDR10 base layer: so
// it is encoded as HDR10, and its RPU, converted to profile 8.1, carries
// the identity mapping (dovi.RPU.ToProfile81) that makes it that picture.
//
// To keep the layers apart instead (--dv-fel keep or reencode, profile 7
// out), the pictures pass through: the base layer copied, with its
// enhancement layer picture alongside for the encoder (see fellayers.go).

type felComposer struct {
	dec    gpu.Decoder
	report Reporter
	els    map[int64]*dovi.Picture // decoded enhancement layer pictures by timestamp
	free   []*dovi.Picture
	// outs are the composed pictures' buffers not in use: a picture is
	// encoded while the next decodes and composes.
	outs chan *dovi.Picture
	// The encoding side, once the first picture is composed: queue feeds
	// it, failed gets its error, done closes when it has stopped.
	queue  chan composedPicture
	failed chan error
	done   chan struct{}
	closed bool

	// pass hands the layers on rather than composing them; elBack returns
	// enhancement layer pictures the encoding side is done with.
	pass   bool
	elBack chan *dovi.Picture

	composed, missing, unusable int
	err                         error // the first composition failure
}

func newFELComposer(open decoderOpener, report Reporter, pass bool) (*felComposer, error) {
	f := &felComposer{report: report, els: map[int64]*dovi.Picture{}, outs: make(chan *dovi.Picture, felBuffers),
		pass: pass, elBack: make(chan *dovi.Picture, felBuffers+felLead+8)}
	for range felBuffers {
		f.outs <- nil // made at the picture's size on first use
	}
	dec, err := open(gpu.DecodeConfig{Codec: gpu.DecodeHEVC, LowDelay: true}, f.keep)
	if err != nil {
		return nil, err
	}
	f.dec = dec
	return f, nil
}

// keep copies an enhancement layer picture out of the decoder.
func (f *felComposer) keep(p *gpu.DecodedPicture) error {
	if p.Depth != 10 {
		return nil
	}
	for more := true; more; {
		select {
		case e := <-f.elBack:
			f.free = append(f.free, e)
		default:
			more = false
		}
	}
	var el *dovi.Picture
	if n := len(f.free); n > 0 && f.free[n-1].Width == p.Width && f.free[n-1].Height == p.Height {
		el, f.free = f.free[n-1], f.free[:n-1]
	} else {
		el = newPicture(p.Width, p.Height)
	}
	row := 2 * p.Width
	for y := range p.Height {
		copy(el.Y[y*el.Pitch:y*el.Pitch+row], p.Y[y*p.Pitch:])
	}
	for y := range p.Height / 2 {
		copy(el.UV[y*el.Pitch:y*el.Pitch+row], p.UV[y*p.Pitch:])
	}
	f.els[p.PTS] = el
	return nil
}

func newPicture(w, h int) *dovi.Picture {
	return &dovi.Picture{Width: w, Height: h, Y: make([]byte, 2*w*h), UV: make([]byte, w*h), Pitch: 2 * w}
}

// felBuffers is how many composed pictures can be on their way: one being
// encoded, one waiting, one being composed.
const felBuffers = 3

type composedPicture struct {
	pic picture
	buf *dovi.Picture
	el  *dovi.Picture // passed on, to come back by elBack
}

// put composes pic (from the base layer decoder, its planes valid only
// during the call) and passes it to each on another goroutine, in order.
// An error of each's comes back from a later put, or from finish.
func (f *felComposer) put(pic picture, each func(picture) error) error {
	if f.queue == nil {
		f.queue = make(chan composedPicture, felBuffers-2)
		f.failed, f.done = make(chan error, 1), make(chan struct{})
		go func() {
			defer close(f.done)
			ok := true
			for c := range f.queue {
				if ok {
					if err := each(c.pic); err != nil {
						f.failed <- err
						ok = false
					}
				}
				f.outs <- c.buf
				if c.el != nil {
					select { // for reuse; the collector has it otherwise
					case f.elBack <- c.el:
					default:
					}
				}
			}
		}()
	}
	select {
	case err := <-f.failed:
		f.stop()
		return err
	default:
	}
	buf := <-f.outs
	p := pic.gpu
	if buf == nil || buf.Width != p.Width || buf.Height != p.Height {
		buf = newPicture(p.Width, p.Height)
	}
	var el *dovi.Picture
	pic.layered = f.pass && p.Depth == 10
	pic.gpu, el = f.compose(buf, p, pic.rpu)
	pic.el = el
	f.queue <- composedPicture{pic, buf, el}
	return nil
}

// stop ends the encoding side, waiting for it.
func (f *felComposer) stop() {
	if f.queue != nil && !f.closed {
		f.closed = true
		close(f.queue)
		<-f.done
	}
}

// finish waits for the pictures on their way, giving each's error.
func (f *felComposer) finish() error {
	f.stop()
	if f.failed == nil {
		return nil
	}
	select {
	case err := <-f.failed:
		return err
	default:
		return nil
	}
}

// compose writes the base layer picture p composed with its enhancement
// layer picture and RPU into out (p as it is when either is missing), and
// gives it as a picture. Passing, it copies p and gives its enhancement
// layer picture (nil when there is none) to go with it.
func (f *felComposer) compose(out *dovi.Picture, p *gpu.DecodedPicture, rpu []byte) (*gpu.DecodedPicture, *dovi.Picture) {
	el := f.els[p.PTS]
	if w, ok := f.dec.(interface{ Wait(int64) error }); ok && el == nil {
		// A decoder that gives its pictures when it can (ffmpeg): wait
		// for this one's.
		if err := w.Wait(p.PTS); err == nil {
			el = f.els[p.PTS]
		}
	}
	// Enhancement layer pictures before this one will not be wanted.
	for pts, e := range f.els {
		if pts <= p.PTS {
			delete(f.els, pts)
			if e != el {
				f.free = append(f.free, e)
			}
		}
	}
	var passed *dovi.Picture
	if f.pass && p.Depth == 10 {
		passed, el = el, nil // the encoding side's now; nothing composed
		if passed == nil {
			f.missing++
		} else {
			f.composed++
		}
	}
	if el != nil {
		defer func() { f.free = append(f.free, el) }()
	}
	var err error
	switch {
	case f.pass:
	case el == nil || rpu == nil || p.Depth != 10:
		f.missing++
	default:
		var u *dovi.RPU
		var c *dovi.Composer
		if u, err = dovi.ParseNAL(rpu); err == nil {
			c, err = dovi.NewComposer(u)
		}
		if err == nil {
			bl := &dovi.Picture{Width: p.Width, Height: p.Height, Y: p.Y, UV: p.UV, Pitch: p.Pitch}
			err = c.Compose(out, bl, el)
		}
		if err != nil {
			if f.unusable++; f.err == nil {
				f.err = err
			}
		}
	}
	if f.pass || el == nil || rpu == nil || p.Depth != 10 || err != nil {
		// The base layer as it is, copied: the decoder's planes do not
		// outlive the call.
		bps := (p.Depth + 7) / 8
		for y := range p.Height {
			copy(out.Y[y*out.Pitch:y*out.Pitch+bps*p.Width], p.Y[y*p.Pitch:])
		}
		for y := range p.Height / 2 {
			copy(out.UV[y*out.Pitch:y*out.Pitch+bps*p.Width], p.UV[y*p.Pitch:])
		}
	} else {
		f.composed++
	}
	q := *p
	q.Y, q.UV, q.Pitch = out.Y, out.UV, out.Pitch
	return &q, passed
}

func (f *felComposer) close() {
	_ = f.dec.Close()
	if f.pass {
		f.report.Report("kept the full enhancement layer of %d pictures", f.composed)
	} else {
		f.report.Report("composed the full enhancement layer into %d pictures", f.composed)
	}
	if f.missing > 0 {
		f.report.Report("warning: %d pictures had no enhancement layer or RPU to compose with: their base layer is kept", f.missing)
	}
	if f.unusable > 0 {
		f.report.Report("warning: %d pictures could not be composed (%v): their base layer is kept", f.unusable, f.err)
	}
}
