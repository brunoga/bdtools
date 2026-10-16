package convert

import (
	"fmt"
	"io"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/mvc"
)

// builtinAVC is the H.264 encoder in this program (the one that codes
// MVC's left eye) as a gpu.Encoder: the pictures drawn as NV12, taken
// into its planes. 8-bit, High profile.
type builtinAVC struct {
	enc    *mvc.BaseEncoder
	w      io.Writer
	cfg    gpu.Config
	pic    []byte
	cb, cr []byte // the chroma planes, before padding
	cw, ch int    // the coded size: whole macroblocks
	n      int64
	err    error
}

// avcLevel is the lowest level whose frame size (MaxFS, in macroblocks)
// takes w x h at up to 30 pictures a second.
func avcLevel(w, h int) int {
	mbs := ((w + 15) / 16) * ((h + 15) / 16)
	switch {
	case mbs <= 8192:
		return 41
	case mbs <= 22080:
		return 50
	}
	return 51
}

func openBuiltinAVC(cfg gpu.Config, w io.Writer) (gpu.Encoder, error) {
	if cfg.Codec != gpu.H264 {
		return nil, fmt.Errorf("the built-in H.264 encoder codes H.264, not %v", cfg.Codec)
	}
	if cfg.BitDepth > 8 {
		return nil, fmt.Errorf("the built-in H.264 encoder codes 8 bits")
	}
	o := mvc.BaseOptions{Width: cfg.Width, Height: cfg.Height, QP: cfg.QP, Keyint: cfg.GOP, BFrames: 3,
		Level: avcLevel(cfg.Width, cfg.Height), FPSNum: cfg.FPSNum, FPSDen: cfg.FPSDen}
	if c := cfg.Color; c != nil {
		o.Color = &mvc.ColorInfo{Primaries: c.Primaries, Transfer: c.Transfer, Matrix: c.Matrix, FullRange: c.FullRange}
	}
	enc, err := mvc.NewBaseEncoder(o)
	if err != nil {
		return nil, err
	}
	wd, h := cfg.Width, cfg.Height
	cw, ch := (wd+15)&^15, (h+15)&^15
	return &builtinAVC{enc: enc, w: w, cfg: cfg, pic: make([]byte, wd*h*3/2), cb: make([]byte, wd*h/4), cr: make([]byte, wd*h/4),
		cw: cw, ch: ch}, nil
}

func (b *builtinAVC) Encode(fill func(*gpu.Picture)) error {
	if b.err != nil {
		return b.err
	}
	w, h := b.cfg.Width, b.cfg.Height
	fill(&gpu.Picture{Y: b.pic[:w*h], UV: b.pic[w*h:], Pitch: w, Depth: 8})
	uv := b.pic[w*h:]
	for i := range w * h / 4 {
		b.cb[i], b.cr[i] = uv[2*i], uv[2*i+1]
	}
	// The encoder holds a source until its picture is coded: a new one
	// each time, padded to whole macroblocks.
	cw, ch := b.cw, b.ch
	src := &mvc.DepSource{Y: make([]byte, cw*ch), Cb: make([]byte, cw*ch/4), Cr: make([]byte, cw*ch/4), StrideY: cw, StrideC: cw / 2}
	copyPadded(src.Y, cw, ch, b.pic[:w*h], w, w, h)
	copyPadded(src.Cb, cw/2, ch/2, b.cb, w/2, w/2, h/2)
	copyPadded(src.Cr, cw/2, ch/2, b.cr, w/2, w/2, h/2)
	aus, err := b.enc.Encode(src, b.n)
	b.n++
	if err != nil {
		b.err = err
		return err
	}
	return b.write(aus)
}

func (b *builtinAVC) write(aus [][]byte) error {
	for _, au := range aus {
		if _, err := b.w.Write(au); err != nil {
			b.err = err
			return err
		}
	}
	return nil
}

// Close codes the pictures waiting and writes the rest of the stream.
func (b *builtinAVC) Close() error {
	if b.err != nil {
		return b.err
	}
	aus, err := b.enc.Flush()
	if werr := b.write(aus); err == nil {
		err = werr
	}
	b.err = err
	return err
}
