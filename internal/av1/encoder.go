// Package av1 is an AV1 encoder: 4:2:0 pictures of 8 or 10 bits coded as
// an AV1 stream (OBUs, the low overhead format), each picture's
// reconstruction exactly a decoder's.
package av1

import (
	"errors"
	"fmt"
)

// Source is a picture to code: 4:2:0, a sample a uint16.
type Source struct {
	Y, Cb, Cr        []uint16
	StrideY, StrideC int
}

// EncoderOptions are an encoder's settings.
type EncoderOptions struct {
	Width, Height int
	BitDepth      int // 8 or 10
	// QIndex is the quantizer index, 1..255 (0 codes losslessly).
	QIndex         int
	FPSNum, FPSDen int
	// Primaries, Transfer and Matrix are the colour description the
	// stream states (H.273); 0 for unspecified.
	Primaries, Transfer, Matrix int
	FullRange                   bool
}

// Encoder codes pictures.
type Encoder struct {
	o     EncoderOptions
	seq   seqParams
	n     int
	frame *frameEnc
}

// NewEncoder makes an encoder.
func NewEncoder(o EncoderOptions) (*Encoder, error) {
	if o.Width < 16 || o.Height < 16 || o.Width > 65536 || o.Height > 65536 {
		return nil, fmt.Errorf("av1: %dx%d: no such picture size", o.Width, o.Height)
	}
	if o.BitDepth != 8 && o.BitDepth != 10 {
		return nil, errors.New("av1: 8 or 10 bits")
	}
	if o.QIndex < 0 || o.QIndex > 255 {
		return nil, errors.New("av1: q index 0..255")
	}
	e := &Encoder{o: o}
	e.seq = seqParams{
		width: o.Width, height: o.Height, bitDepth: o.BitDepth,
		primaries: o.Primaries, transfer: o.Transfer, matrix: o.Matrix, fullRange: o.FullRange,
		num: o.FPSNum, den: o.FPSDen,
	}
	return e, nil
}

// SequenceHeader is the sequence header OBU (for a container's
// configuration record).
func (e *Encoder) SequenceHeader() []byte {
	return appendOBU(nil, obuSequenceHeader, e.seq.sequenceHeader())
}

// Encode codes a picture, giving its temporal unit.
func (e *Encoder) Encode(src *Source) ([]byte, error) {
	fp := &frameParams{key: true, orderHint: e.n, adapt: true, qIndex: e.o.QIndex}
	miCols, miRows := 2*((e.o.Width+7)>>3), 2*((e.o.Height+7)>>3)
	fp.tiles = newTiling(miCols, miRows, 0, 0)
	f := e.newFrame(fp, src)
	var tiles [][]byte
	for tr := range fp.tiles.rows() {
		for tc := range fp.tiles.cols() {
			w := newSymbolWriter(fp.adapt)
			t := newTileEnc(f, fp.tiles.rowStarts[tr]*16, min(fp.tiles.rowStarts[tr+1]*16, miRows),
				fp.tiles.colStarts[tc]*16, min(fp.tiles.colStarts[tc+1]*16, miCols), w)
			t.code()
			tiles = append(tiles, w.finish())
		}
	}
	e.frame = f
	e.n++
	payload := append(e.seq.frameHeader(fp), fp.tileGroup(tiles)...)
	tu := appendOBU(nil, obuTemporalDelimiter, nil)
	tu = appendOBU(tu, obuSequenceHeader, e.seq.sequenceHeader())
	return appendOBU(tu, obuFrame, payload), nil
}

// newFrame sets a frame up: its source padded to whole superblocks.
func (e *Encoder) newFrame(fp *frameParams, src *Source) *frameEnc {
	o := e.o
	miCols, miRows := 2*((o.Width+7)>>3), 2*((o.Height+7)>>3)
	f := &frameEnc{seq: &e.seq, fp: fp, bitDepth: o.BitDepth, miCols: miCols, miRows: miRows}
	pw, ph := (miCols+15)/16*64, (miRows+15)/16*64
	for p := range 3 {
		w, h, sw, sh := pw, ph, o.Width, o.Height
		in, stride := src.Y, src.StrideY
		if p > 0 {
			w, h, sw, sh = pw/2, ph/2, (o.Width+1)/2, (o.Height+1)/2
			in, stride = src.Cb, src.StrideC
			if p == 2 {
				in = src.Cr
			}
		}
		f.stride[p] = w
		f.src[p] = make([]uint16, w*h)
		f.rec[p] = make([]uint16, w*h)
		for y := range h {
			row := in[min(y, sh-1)*stride:]
			dst := f.src[p][y*w:]
			copy(dst[:sw], row[:sw])
			for x := sw; x < w; x++ {
				dst[x] = row[sw-1]
			}
		}
	}
	n := miCols * miRows
	f.miSize, f.yMode, f.uvMode = make([]uint8, n), make([]uint8, n), make([]uint8, n)
	f.skip, f.txSize, f.txType = make([]uint8, n), make([]uint8, n), make([]uint8, n)
	f.lossless = fp.lossless()
	bd := (o.BitDepth - 8) >> 1
	qi := func(v int) int { return min(max(v, 0), 255) }
	f.dcQ = [3]int{int(dcQlookup[bd][qi(fp.qIndex+fp.deltaQYDc)]), int(dcQlookup[bd][qi(fp.qIndex+fp.deltaQUVDc)]), int(dcQlookup[bd][qi(fp.qIndex+fp.deltaQUVDc)])}
	f.acQ = [3]int{int(acQlookup[bd][qi(fp.qIndex)]), int(acQlookup[bd][qi(fp.qIndex+fp.deltaQUVAc)]), int(acQlookup[bd][qi(fp.qIndex+fp.deltaQUVAc)])}
	return f
}

// Recon gives the last picture's reconstruction (its planes cropped to
// the picture).
func (e *Encoder) Recon() (y, cb, cr []uint16) {
	f := e.frame
	crop := func(p, w, h int) []uint16 {
		out := make([]uint16, w*h)
		for i := range h {
			copy(out[i*w:(i+1)*w], f.rec[p][i*f.stride[p]:])
		}
		return out
	}
	cw, ch := (e.o.Width+1)/2, (e.o.Height+1)/2
	return crop(0, e.o.Width, e.o.Height), crop(1, cw, ch), crop(2, cw, ch)
}
