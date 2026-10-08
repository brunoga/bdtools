package convert

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/brunoga/bdtools/internal/dovi"
	"github.com/brunoga/bdtools/internal/gpu"
)

// Dolby Vision profile 7 out: the full enhancement layer kept as a layer.
//
// The base layer is encoded as any picture is; the enhancement layer, half
// its size, on a second encoder session set up alike, so that the two
// streams' pictures are coded in the same order with the same types (as
// Dolby Vision requires, and the mux checks). The source's RPUs go out
// unchanged.
//
// What the enhancement layer holds depends on the method:
//
//   - reencode: the source's enhancement layer, re-encoded. It was made
//     for the source's base layer, so the encoding error of ours passes
//     through the composition uncorrected.
//   - keep: an enhancement layer rebuilt for our base layer, as Dolby's
//     encoder makes one. The base layer's bitstream is decoded again as it
//     is written, and each picture's enhancement layer is what brings that
//     decoded base layer, through the RPU, back to the picture the source's
//     layers compose to (dovi.Composer.Rebuild): the composition corrects
//     the base layer's encoding error, within the enhancement layer's
//     resolution and quantisation.

// elCRFOffset is how much finer than the base layer the enhancement layer
// is quantised by default. Its residual is a few codes either side of its
// offset, which the base layer's quantiser would mostly flatten: on a 4K
// FEL clip, each 6 steps finer brought the composition 1.5-2 dB nearer the
// source's, for an enhancement layer about 3 times the size.
const elCRFOffset = 6

// felLayers is a gpuSink's enhancement layer side, for one segment.
type felLayers struct {
	mode   FEL
	el     gpu.Encoder
	elFile *os.File
	elPath string
	w, h   int // the enhancement layer's size

	// keep: the base layer's bitstream (one access unit a write), decoded
	// again; the source's layers wait by picture number until their base
	// layer comes back.
	loop    gpu.Decoder
	written [][]byte
	held    map[int]*heldLayers
	in, out int // pictures put, and come back from the loop
	// The rebuilding and the enhancement layer's encoding run on a
	// goroutine of their own, in order, while the base layer goes on:
	// jobs feeds it, back returns what it is done with, failed gets its
	// error, done closes when it has stopped.
	jobs   chan *heldLayers
	back   chan *heldLayers
	failed chan error
	done   chan struct{}
	free   []*heldLayers
	built  *dovi.Picture
	rpu    []byte
	comp   *dovi.Composer
	err    error
}

type heldLayers struct {
	bl, el *dovi.Picture
	rpu    []byte
	got    *dovi.Picture // the base layer as the encoder made it
}

// rebuildAhead is how many pictures can wait for the rebuilding goroutine.
const rebuildAhead = 2

// elSegmentPath is the enhancement layer's file for a base layer segment.
func elSegmentPath(path string) string {
	return filepath.Join(filepath.Dir(path), "el-"+strings.TrimPrefix(filepath.Base(path), "video-"))
}

// openLayers starts the enhancement layer's encoder (and for keep, the
// base layer's decode loop) for a segment whose base layer goes to blPath.
// It returns the writer the base layer's encoder writes to.
func openLayers(mode FEL, kind gpu.Kind, cfg gpu.Config, elQP int, blPath string, bl io.Writer) (*felLayers, io.Writer, error) {
	l := &felLayers{mode: mode, elPath: elSegmentPath(blPath), w: cfg.Width / 2, h: cfg.Height / 2}
	f, err := os.Create(l.elPath) //nolint:gosec // our work directory
	if err != nil {
		return nil, nil, err
	}
	l.elFile = f
	ecfg := cfg
	ecfg.Width, ecfg.Height = l.w, l.h
	ecfg.QP = elQP
	if l.el, err = gpu.Open(kind, ecfg, f); err != nil {
		l.abort()
		return nil, nil, fmt.Errorf("opening the enhancement layer's encoder: %w", err)
	}
	if mode != FELKeep {
		return l, bl, nil
	}
	l.held = map[int]*heldLayers{}
	if l.loop, err = gpu.OpenDecoder(kind, gpu.DecodeConfig{Codec: gpu.DecodeHEVC}, l.decoded); err != nil {
		l.abort()
		return nil, nil, fmt.Errorf("opening the decoder of the encoded base layer: %w", err)
	}
	l.jobs, l.back = make(chan *heldLayers, rebuildAhead), make(chan *heldLayers, rebuildAhead+felBuffers+felLead+64)
	l.failed, l.done = make(chan error, 1), make(chan struct{})
	go func() {
		defer close(l.done)
		ok := true
		for h := range l.jobs {
			if ok {
				if err := l.rebuild(h); err != nil {
					l.failed <- err
					ok = false
				}
			}
			l.back <- h
		}
	}()
	return l, io.MultiWriter(bl, (*accessUnits)(&l.written)), nil
}

// accessUnits collects what an encoder writes: one access unit a write.
type accessUnits [][]byte

func (a *accessUnits) Write(b []byte) (int, error) {
	*a = append(*a, bytes.Clone(b))
	return len(b), nil
}

// put takes a picture's layers after its base layer has gone to the
// encoder: reencode encodes its enhancement layer now; keep holds both
// layers until the base layer comes back from the loop.
func (l *felLayers) put(p picture) error {
	if l.mode == FELReencode {
		return l.encode(p.el, p.rpu)
	}
	h := l.hold()
	copyPicture(h.bl, p.gpu)
	if p.el != nil {
		copyLayer(h.el, p.el)
	} else {
		neutral(h.el, p.rpu)
	}
	h.rpu = p.rpu
	l.held[l.in] = h
	l.in++
	return l.drain()
}

// drain decodes what the base layer's encoder has written.
func (l *felLayers) drain() error {
	for len(l.written) > 0 {
		au := l.written[0]
		l.written = l.written[1:]
		if err := l.loop.Decode(au, int64(l.out+len(l.held))); err != nil {
			return fmt.Errorf("decoding the encoded base layer: %w", err)
		}
		if l.err != nil {
			return l.err
		}
	}
	return nil
}

// decoded takes a picture of the base layer as the encoder made it back
// from the loop, and has its enhancement layer rebuilt. Pictures come back
// in display order, which is the order they were put.
func (l *felLayers) decoded(p *gpu.DecodedPicture) error {
	select {
	case err := <-l.failed:
		l.err = err
		return err
	default:
	}
	h := l.held[l.out]
	if h == nil {
		l.err = fmt.Errorf("the encoded base layer gave back picture %d, which was not put", l.out)
		return l.err
	}
	delete(l.held, l.out)
	l.out++
	copyPicture(h.got, p) // the decoder's planes do not outlive the call
	l.jobs <- h
	return nil
}

// rebuild makes a picture's enhancement layer for the base layer as the
// encoder made it, and encodes it. It runs on the rebuilding goroutine.
func (l *felLayers) rebuild(h *heldLayers) error {
	if h.rpu == nil {
		return l.encode(h.el, nil) // nothing to rebuild with
	}
	c, err := l.composer(h.rpu)
	if err == nil {
		if l.built == nil {
			l.built = newPicture(l.w, l.h)
		}
		err = c.Rebuild(l.built, h.bl, h.el, h.got)
	}
	if err != nil {
		return err
	}
	return l.encode(l.built, nil)
}

// stop waits for the rebuilding goroutine to finish what it has, giving its
// error.
func (l *felLayers) stop() error {
	if l.jobs == nil {
		return nil
	}
	close(l.jobs)
	l.jobs = nil
	<-l.done
	select {
	case err := <-l.failed:
		return err
	default:
		return nil
	}
}

// composer is the RPU's composer, kept while the RPU repeats.
func (l *felLayers) composer(rpu []byte) (*dovi.Composer, error) {
	if l.comp != nil && bytes.Equal(rpu, l.rpu) {
		return l.comp, nil
	}
	u, err := dovi.ParseNAL(rpu)
	if err != nil {
		return nil, err
	}
	c, err := dovi.NewComposer(u)
	if err != nil {
		return nil, err
	}
	l.comp, l.rpu = c, rpu
	return c, nil
}

// encode encodes an enhancement layer picture (nil: a neutral one, that
// adds nothing, for a picture whose source had none).
func (l *felLayers) encode(el *dovi.Picture, rpu []byte) error {
	if el == nil {
		if l.built == nil {
			l.built = newPicture(l.w, l.h)
		}
		neutral(l.built, rpu)
		el = l.built
	}
	if el.Width != l.w || el.Height != l.h {
		return fmt.Errorf("a %dx%d enhancement layer picture for a %dx%d layer", el.Width, el.Height, l.w, l.h)
	}
	return l.el.Encode(func(dst *gpu.Picture) {
		for y := range l.h {
			copy(dst.Y[y*dst.Pitch:y*dst.Pitch+2*l.w], el.Y[y*el.Pitch:])
		}
		for y := range l.h / 2 {
			copy(dst.UV[y*dst.Pitch:y*dst.Pitch+2*l.w], el.UV[y*el.Pitch:])
		}
	})
}

// finish ends the segment after the base layer's encoder has finished
// writing: the last pictures come back from the loop and are encoded.
func (l *felLayers) finish() error {
	var err error
	if l.loop != nil {
		err = l.drain()
		if err == nil {
			err = l.loop.Flush()
		}
		if err == nil {
			err = l.err
		}
		if serr := l.stop(); err == nil {
			err = serr
		}
		_ = l.loop.Close()
		if err == nil && (l.out != l.in || len(l.held) > 0) {
			err = fmt.Errorf("the encoded base layer gave back %d of %d pictures", l.out, l.in)
		}
	}
	if cerr := l.el.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = l.elFile.Sync()
	}
	if cerr := l.elFile.Close(); err == nil {
		err = cerr
	}
	return err
}

func (l *felLayers) abort() {
	_ = l.stop()
	if l.loop != nil {
		_ = l.loop.Close()
	}
	if l.el != nil {
		_ = l.el.Close()
	}
	if l.elFile != nil {
		_ = l.elFile.Close()
	}
	_ = os.Remove(l.elPath)
}

func (l *felLayers) hold() *heldLayers {
	for more := true; more; {
		select {
		case h := <-l.back:
			l.free = append(l.free, h)
		default:
			more = false
		}
	}
	if n := len(l.free); n > 0 {
		h := l.free[n-1]
		l.free = l.free[:n-1]
		return h
	}
	return &heldLayers{bl: &dovi.Picture{}, el: newPicture(l.w, l.h), got: &dovi.Picture{}}
}

// copyPicture copies a decoded 10-bit picture into p, sizing p to it.
func copyPicture(dst *dovi.Picture, src *gpu.DecodedPicture) {
	if dst.Width != src.Width || dst.Height != src.Height || dst.Y == nil {
		*dst = *newPicture(src.Width, src.Height)
	}
	row := 2 * src.Width
	for y := range src.Height {
		copy(dst.Y[y*dst.Pitch:y*dst.Pitch+row], src.Y[y*src.Pitch:])
	}
	for y := range src.Height / 2 {
		copy(dst.UV[y*dst.Pitch:y*dst.Pitch+row], src.UV[y*src.Pitch:])
	}
}

func copyLayer(dst, src *dovi.Picture) {
	if dst.Width != src.Width || dst.Height != src.Height {
		*dst = *newPicture(src.Width, src.Height)
	}
	copy(dst.Y, src.Y)
	copy(dst.UV, src.UV)
}

// neutral fills an enhancement layer picture with the RPU's offsets: a
// residual of nothing (512, the usual offset, without an RPU).
func neutral(p *dovi.Picture, rpu []byte) {
	off := [3]uint64{512, 512, 512}
	if u, err := dovi.ParseNAL(rpu); err == nil && u.Mapping != nil && u.Mapping.NLQ != nil {
		off = u.Mapping.NLQ.Offset
	}
	fill := func(b []byte, v uint64) {
		s := uint16(min(v, 1023)) << 6 //nolint:gosec // a 10-bit code
		b[0], b[1] = byte(s), byte(s>>8)
	}
	for i := 0; i+1 < len(p.Y); i += 2 {
		fill(p.Y[i:], off[0])
	}
	for i := 0; i+3 < len(p.UV); i += 4 {
		fill(p.UV[i:], off[1])
		fill(p.UV[i+2:], off[2])
	}
}

var errLayersMisaligned = errors.New("the enhancement layer's pictures do not line up with the base layer's")
