package convert

import (
	"fmt"
	"io"

	"github.com/brunoga/bdtools/internal/av1"
	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/hevc"
)

// builtinAV1 is the AV1 encoder in this program as a gpu.Encoder: the
// pictures drawn as NV12 or P010, taken into its planes. Its q index is
// --crf's as the GPU encoders and SVT-AV1 take it (gpu.AV1QIndex).
type builtinAV1 struct {
	enc *av1.Encoder
	w   io.Writer
	cfg gpu.Config
	pic []byte
	nv  hevc.Source
	src av1.Source
	err error
}

func openBuiltinAV1(cfg gpu.Config, w io.Writer) (gpu.Encoder, error) {
	if cfg.Codec != gpu.AV1 {
		return nil, fmt.Errorf("the built-in AV1 encoder codes AV1, not %v", cfg.Codec)
	}
	depth := max(cfg.BitDepth, 8)
	o := av1.EncoderOptions{Width: cfg.Width, Height: cfg.Height, BitDepth: depth, QIndex: gpu.AV1QIndex(cfg.QP),
		FPSNum: cfg.FPSNum, FPSDen: cfg.FPSDen, Keyint: cfg.GOP, TileColsLog2: -1, TileRowsLog2: -1}
	if c := cfg.Color; c != nil {
		o.Primaries, o.Transfer, o.Matrix, o.FullRange = c.Primaries, c.Transfer, c.Matrix, c.FullRange
	}
	enc, err := av1.NewEncoder(o)
	if err != nil {
		return nil, err
	}
	bps := 1
	if depth > 8 {
		bps = 2
	}
	wd, h := cfg.Width, cfg.Height
	b := &builtinAV1{enc: enc, w: w, cfg: cfg, pic: make([]byte, wd*h*bps*3/2)}
	b.nv = hevc.Source{Y: make([]uint16, wd*h), Cb: make([]uint16, wd*h/4), Cr: make([]uint16, wd*h/4), StrideY: wd, StrideC: wd / 2}
	b.src = av1.Source{Y: b.nv.Y, Cb: b.nv.Cb, Cr: b.nv.Cr, StrideY: wd, StrideC: wd / 2}
	return b, nil
}

func (b *builtinAV1) Encode(fill func(*gpu.Picture)) error {
	if b.err != nil {
		return b.err
	}
	w, h := b.cfg.Width, b.cfg.Height
	depth := max(b.cfg.BitDepth, 8)
	bps := 1
	if depth > 8 {
		bps = 2
	}
	ys := w * h * bps
	fill(&gpu.Picture{Y: b.pic[:ys], UV: b.pic[ys:], Pitch: w * bps, Depth: depth})
	unpackNV(&b.nv, b.pic, w, h, bps)
	tu, err := b.enc.Encode(&b.src)
	if err == nil {
		_, err = b.w.Write(tu)
	}
	b.err = err
	return err
}

// Close finishes the stream: every picture is coded as it comes.
func (b *builtinAV1) Close() error { return b.err }
