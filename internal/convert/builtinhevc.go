package convert

import (
	"fmt"
	"io"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/hevc"
)

// builtinCRFOffset turns --crf into the HEVC encoder's QP: at QP 19 it
// matched x265 medium's CRF 24 in size and PSNR on a film minute (44.80
// dB each, 15.0 MB), and the two track each other a QP for a CRF.
const builtinCRFOffset = 5

// builtinQP is the HEVC encoder's QP for --crf crf.
func builtinQP(crf int) int { return min(max(crf-builtinCRFOffset, 0), 51) }

// builtinHEVC is the HEVC encoder in this program as a gpu.Encoder (a
// gpu.StereoEncoder with two views, MV-HEVC): the pictures drawn as NV12
// or P010, taken into its planes.
type builtinHEVC struct {
	enc *hevc.Encoder
	w   io.Writer
	cfg gpu.Config
	pic [2][]byte
	src [2]hevc.Source
	err error
}

func openBuiltinHEVC(cfg gpu.Config, w io.Writer) (gpu.Encoder, error) {
	if cfg.Codec != gpu.HEVC {
		return nil, fmt.Errorf("the built-in encoder codes HEVC (or MVC), not %v", cfg.Codec)
	}
	depth := max(cfg.BitDepth, 8)
	o := hevc.EncoderOptions{Width: cfg.Width, Height: cfg.Height, BitDepth: depth, QP: builtinQP(cfg.QP),
		FPSNum: cfg.FPSNum, FPSDen: cfg.FPSDen, Keyint: cfg.GOP, BFrames: -1, Views: max(cfg.Views, 1)}
	if c := cfg.Color; c != nil {
		o.Primaries, o.Transfer, o.Matrix, o.FullRange = c.Primaries, c.Transfer, c.Matrix, c.FullRange
	}
	enc, err := hevc.NewEncoder(o)
	if err != nil {
		return nil, err
	}
	bps := 1
	if depth > 8 {
		bps = 2
	}
	wd, h := cfg.Width, cfg.Height
	b := &builtinHEVC{enc: enc, w: w, cfg: cfg}
	for v := range o.Views {
		b.pic[v] = make([]byte, wd*h*bps*3/2)
		b.src[v] = hevc.Source{Y: make([]uint16, wd*h), Cb: make([]uint16, wd*h/4), Cr: make([]uint16, wd*h/4), StrideY: wd, StrideC: wd / 2}
	}
	return b, nil
}

// picture is view v's buffer as a gpu.Picture to draw into.
func (b *builtinHEVC) picture(v int) *gpu.Picture {
	w, h := b.cfg.Width, b.cfg.Height
	depth := max(b.cfg.BitDepth, 8)
	bps := 1
	if depth > 8 {
		bps = 2
	}
	ys := w * h * bps
	return &gpu.Picture{Y: b.pic[v][:ys], UV: b.pic[v][ys:], Pitch: w * bps, Depth: depth}
}

// EncodeStereo draws and codes a picture of two views (opened with
// Views 2).
func (b *builtinHEVC) EncodeStereo(fill func(left, right *gpu.Picture)) error {
	if b.err != nil {
		return b.err
	}
	if b.cfg.Views != 2 {
		return fmt.Errorf("the built-in encoder was opened for one view")
	}
	fill(b.picture(0), b.picture(1))
	for v := range 2 {
		unpackNV(&b.src[v], b.pic[v], b.cfg.Width, b.cfg.Height, b.bps())
	}
	aus, err := b.enc.EncodeStereo(&b.src[0], &b.src[1])
	if err != nil {
		b.err = err
		return err
	}
	return b.write(aus)
}

func (b *builtinHEVC) bps() int {
	if b.cfg.BitDepth > 8 {
		return 2
	}
	return 1
}

func (b *builtinHEVC) Encode(fill func(*gpu.Picture)) error {
	if b.err != nil {
		return b.err
	}
	fill(b.picture(0))
	unpackNV(&b.src[0], b.pic[0], b.cfg.Width, b.cfg.Height, b.bps())
	aus, err := b.enc.Encode(&b.src[0])
	if err != nil {
		b.err = err
		return err
	}
	return b.write(aus)
}

func (b *builtinHEVC) write(aus [][]byte) error {
	for _, au := range aus {
		if _, err := b.w.Write(au); err != nil {
			b.err = err
			return err
		}
	}
	return nil
}

// Close codes the pictures waiting and writes the rest of the stream.
func (b *builtinHEVC) Close() error {
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

// unpackNV takes NV12 (bytes) or P010 (little-endian words, the sample in
// the high bits) into planes of samples.
func unpackNV(dst *hevc.Source, src []byte, w, h, bps int) {
	n := w * h
	if bps == 1 {
		for i, v := range src[:n] {
			dst.Y[i] = uint16(v)
		}
		uv := src[n : n+n/2]
		for i := range n / 4 {
			dst.Cb[i], dst.Cr[i] = uint16(uv[2*i]), uint16(uv[2*i+1])
		}
		return
	}
	for i := range n {
		dst.Y[i] = (uint16(src[2*i]) | uint16(src[2*i+1])<<8) >> 6
	}
	uv := src[2*n : 3*n]
	for i := range n / 4 {
		dst.Cb[i] = (uint16(uv[4*i]) | uint16(uv[4*i+1])<<8) >> 6
		dst.Cr[i] = (uint16(uv[4*i+2]) | uint16(uv[4*i+3])<<8) >> 6
	}
}
