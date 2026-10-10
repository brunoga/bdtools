package convert

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/brunoga/bdtools/internal/dovi"
	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/hdr"
	"github.com/brunoga/bdtools/mvc"
)

// picture is one decoded picture on its way to an encoder: a stereo pair
// from the MVC decoder (only its base view for a 2D conversion), or a 2D
// picture from a GPU decoder.
type picture struct {
	stereo *mvc.StereoFrame
	gpu    *gpu.DecodedPicture
	pts    int64
	// hdr10Plus is the picture's HDR10+ metadata (a T.35 payload), nil
	// when it has none.
	hdr10Plus []byte
	// rpu is the picture's Dolby Vision RPU NAL unit as the source has it,
	// nil when there is none.
	rpu []byte
	// el is the picture's Dolby Vision enhancement layer picture, when the
	// layers are kept apart (--dv-fel keep or reencode); valid, like the
	// picture, during the call that delivers it.
	el *dovi.Picture
	// layered says the layers are kept apart, for a picture whose source
	// has no enhancement layer picture too (a clip's first, cut from what
	// they referred to): its el is nil, and a neutral one goes in its place.
	layered bool
}

// size is the picture's (one view's) size and sample depth.
func (p picture) size() (w, h, depth int) {
	if p.gpu != nil {
		return p.gpu.Width, p.gpu.Height, p.gpu.Depth
	}
	return p.stereo.Base.Width, p.stereo.Base.Height, 8
}

// pictureSource decodes a source's video, giving its pictures in display
// order.
type pictureSource interface {
	run(each func(picture) error) error
	// frameRate is the stream's, once a picture has come out; 0 when it
	// does not say.
	frameRate() (num, den int)
	// errors counts the access units that decoded with errors (concealed).
	errors() int
	// stereo reports whether pictures carry a dependent view, after run.
	dependentFrames() int
	// hdrStatic is the stream's static HDR metadata, once pictures have
	// come out.
	hdrStatic() hdr.Static
}

// mvcPictures decodes with this repository's H.264/MVC decoder.
type mvcPictures struct {
	dec    *mvc.Decoder
	src    mvc.Source
	report Reporter
	errs   int
	st     mvc.Stats
}

func newMVCPictures(src mvc.Source, threads int, report Reporter) *mvcPictures {
	return &mvcPictures{dec: mvc.NewDecoder(mvc.Options{Threads: threads}), src: src, report: report}
}

func (m *mvcPictures) run(each func(picture) error) error {
	var err error
	m.st, err = m.dec.DecodeStream(m.src, mvc.DecodeOptions{
		OnError: func(err error) {
			// A damaged access unit is concealed and the decode goes on; a
			// few are worth a line, a flood is not.
			m.errs++
			if m.errs <= 5 {
				m.report.Report("warning: %v", err)
			}
		},
	}, func(sf *mvc.StereoFrame) error { return each(picture{stereo: sf, pts: sf.Base.PTS}) })
	return err
}

func (m *mvcPictures) frameRate() (int, int) { return m.dec.FrameRate() }
func (m *mvcPictures) errors() int           { return m.errs }
func (m *mvcPictures) dependentFrames() int  { return m.st.DependentFrames }
func (m *mvcPictures) hdrStatic() hdr.Static { return hdr.Static{} }

// gpuPictures decodes on a GPU, fed access units by a demuxer. On the way
// it reads an HEVC stream's HDR metadata: the static once, HDR10+ for each
// picture.
type gpuPictures struct {
	open     decoderOpener
	codec    gpu.VideoCodec
	next     func() (base, dep []byte, pts int64, err error)
	num, den int
	static   hdr.Static
	dynamic  map[int64]dynamicMD // by timestamp, until the picture comes out
	report   Reporter
	// composeFEL composes a Dolby Vision full enhancement layer into the
	// pictures, or with passFEL passes it on beside them; fel does it, once
	// the first RPU has said there is one.
	composeFEL bool
	passFEL    bool
	felChecked bool
	fel        *felComposer
}

// dynamicMD is a picture's own metadata, held from its access unit until
// the decoder gives the picture out.
type dynamicMD struct {
	hdr10Plus, rpu []byte
}

func (g *gpuPictures) run(each func(picture) error) error {
	g.dynamic = map[int64]dynamicMD{}
	dec, err := g.open(gpu.DecodeConfig{Codec: g.codec}, func(p *gpu.DecodedPicture) error {
		if g.num == 0 && p.FrameRateNum > 0 && p.FrameRateDen > 0 {
			g.num, g.den = p.FrameRateNum, p.FrameRateDen
		}
		pic := picture{gpu: p, pts: p.PTS}
		if d, ok := g.dynamic[p.PTS]; ok {
			pic.hdr10Plus, pic.rpu = d.hdr10Plus, d.rpu
			delete(g.dynamic, p.PTS)
		}
		if g.fel != nil {
			return g.fel.put(pic, each)
		}
		return each(pic)
	})
	if err != nil {
		return err
	}
	defer func() { _ = dec.Close() }()
	defer func() {
		if g.fel != nil {
			g.fel.stop()
			g.fel.close()
		}
	}()
	// With an enhancement layer, the base layer is decoded felLead access
	// units behind it: its decoder may hold pictures back longer (in a
	// stream whose layers are coded differently), and each base layer
	// picture needs its enhancement layer picture there when it comes out.
	type heldAU struct {
		au  []byte
		pts int64
	}
	var held []heldAU
	for {
		base, dep, pts, err := g.next()
		if errors.Is(err, io.EOF) {
			if g.fel != nil {
				if err := g.fel.dec.Flush(); err != nil {
					return err
				}
				for _, h := range held {
					if err := dec.Decode(h.au, h.pts); err != nil {
						return err
					}
				}
			}
			if err := dec.Flush(); err != nil {
				return err
			}
			if g.fel != nil {
				return g.fel.finish()
			}
			return nil
		}
		if err != nil {
			return err
		}
		if len(base) == 0 {
			continue
		}
		if g.codec == gpu.DecodeHEVC {
			md := hdr.ParseHEVC(base)
			if g.static.Mastering == nil {
				g.static.Mastering = md.Static.Mastering
			}
			if g.static.Light == nil {
				g.static.Light = md.Static.Light
			}
			// Dolby Vision's RPU: in the enhancement layer's access unit
			// on a disc, in the picture's own in Matroska.
			rpu := dovi.FindRPU(dep)
			if rpu == nil {
				rpu = dovi.FindRPU(base)
			}
			if md.HDR10Plus != nil || rpu != nil {
				g.dynamic[pts] = dynamicMD{hdr10Plus: md.HDR10Plus, rpu: rpu}
			}
			if rpu != nil && g.composeFEL && !g.felChecked {
				g.felChecked = true
				if u, err := dovi.ParseNAL(rpu); err == nil && u.FEL() {
					if g.fel, err = newFELComposer(g.open, g.report, g.passFEL); err != nil {
						g.report.Report("warning: the Dolby Vision full enhancement layer cannot be decoded: %v", err)
					} else if g.passFEL {
						g.report.Report("Dolby Vision full enhancement layer: keeping it as a layer of its own")
					} else {
						g.report.Report("Dolby Vision full enhancement layer: composing it into the picture")
					}
				}
			}
			if g.fel != nil {
				el := dep
				if el == nil {
					el = dovi.SplitEL(base) // Matroska's single track
				}
				if len(el) > 0 {
					if err := g.fel.dec.Decode(el, pts); err != nil {
						// Go on with the base layer alone.
						g.report.Report("warning: the enhancement layer stopped decoding (%v): the base layer goes on without it", err)
						if err := g.fel.finish(); err != nil {
							return err
						}
						g.fel.close()
						g.fel = nil
						for _, h := range held {
							if err := dec.Decode(h.au, h.pts); err != nil {
								return err
							}
						}
						held = nil
					}
				}
			}
		}
		if g.fel != nil {
			held = append(held, heldAU{base, pts})
			if len(held) <= felLead {
				continue
			}
			base, pts = held[0].au, held[0].pts
			held = held[1:]
		}
		if err := dec.Decode(base, pts); err != nil {
			return err
		}
	}
}

// felLead is how many access units the enhancement layer decodes ahead of
// the base layer.
const felLead = 16

func (g *gpuPictures) frameRate() (int, int) { return normalRate(g.num, g.den) }

// normalRate is a decoder's frame rate as video states it: reduced (NVDEC
// gives 23.976 fps as 96000/4004), and when it is not exactly a standard
// rate, the standard one it is within 0.2% of, if any.
func normalRate(num, den int) (int, int) {
	if num <= 0 || den <= 0 {
		return 0, 0
	}
	a, b := num, den
	for b != 0 {
		a, b = b, a%b
	}
	num, den = num/a, den/a
	for _, r := range standardRates {
		if r[0] == num && r[1] == den {
			return num, den
		}
	}
	if n, d := rateFromDuration(time.Duration(int64(time.Second) * int64(den) / int64(num))); n > 0 {
		return n, d
	}
	return num, den
}
func (g *gpuPictures) errors() int           { return 0 }
func (g *gpuPictures) dependentFrames() int  { return 0 }
func (g *gpuPictures) hdrStatic() hdr.Static { return g.static }

// drawFlat draws a 2D picture into an encoder's picture, converting between
// 8 and 10 bits when the two differ.
func drawFlat(dst *gpu.Picture, p picture) {
	w, h, _ := p.size()
	if p.gpu != nil {
		src := p.gpu
		for y := range h {
			copySamples(dst.Y[y*dst.Pitch:], dst.Depth, src.Y[y*src.Pitch:], src.Depth, w)
		}
		for y := range h / 2 {
			copySamples(dst.UV[y*dst.Pitch:], dst.Depth, src.UV[y*src.Pitch:], src.Depth, w)
		}
		return
	}
	// The H.264 decoder's planar 8-bit view: luma as is, chroma interleaved.
	f := p.stereo.Base
	for y := range h {
		copySamples(dst.Y[y*dst.Pitch:], dst.Depth, f.Y[y*f.StrideY:], 8, w)
	}
	cw := w / 2
	row := make([]byte, w)
	for y := range h / 2 {
		u, v := f.Cb[y*f.StrideC:y*f.StrideC+cw], f.Cr[y*f.StrideC:y*f.StrideC+cw]
		for x := range cw {
			row[2*x], row[2*x+1] = u[x], v[x]
		}
		copySamples(dst.UV[y*dst.Pitch:], dst.Depth, row, 8, w)
	}
}

// copySamples copies n samples between rows of NV12 (depth 8, a byte each)
// or P010 (two bytes, the value in the top ten bits), converting the depth:
// up by four times the value, down with rounding.
func copySamples(dst []byte, dstDepth int, src []byte, srcDepth, n int) {
	dst10, src10 := dstDepth > 8, srcDepth > 8
	switch {
	case dst10 == src10 && !dst10:
		copy(dst[:n], src[:n])
	case dst10 == src10:
		copy(dst[:2*n], src[:2*n])
	case dst10:
		widen(dst[:2*n], src[:n])
	default:
		for i := range n {
			v := (int(binary.LittleEndian.Uint16(src[2*i:])>>6) + 2) >> 2
			dst[i] = byte(min(v, 255))
		}
	}
}

// flatY4M writes 2D pictures as Y4M at a given depth.
type flatY4M struct {
	w        *bufio.Writer
	depth    int
	num, den int
	header   bool
	row      []byte
}

func newFlatY4M(w io.Writer, depth, num, den int) *flatY4M {
	return &flatY4M{w: bufio.NewWriterSize(w, 8<<20), depth: max(depth, 8), num: num, den: den}
}

func (y *flatY4M) write(p picture) error {
	w, h, _ := p.size()
	if !y.header {
		colour := "C420jpeg"
		if y.depth > 8 {
			colour = "C420p10"
		}
		if _, err := fmt.Fprintf(y.w, "YUV4MPEG2 W%d H%d F%d:%d Ip A1:1 %s\n", w, h, y.num, y.den, colour); err != nil {
			return err
		}
		y.header = true
	}
	if _, err := y.w.WriteString("FRAME\n"); err != nil {
		return err
	}
	// Draw it as NV12/P010 at the output depth, then write the planes.
	bps := 1
	if y.depth > 8 {
		bps = 2
	}
	pic := &gpu.Picture{Y: make([]byte, w*h*bps), UV: make([]byte, w*h/2*bps), Pitch: w * bps, Depth: y.depth}
	drawFlat(pic, p)
	put := func(v []byte) error {
		if bps == 1 {
			_, err := y.w.Write(v)
			return err
		}
		// P010 to planar 10-bit: the value from the top bits.
		if cap(y.row) < len(v) {
			y.row = make([]byte, len(v))
		}
		r := y.row[:len(v)]
		for i := 0; i+1 < len(v); i += 2 {
			binary.LittleEndian.PutUint16(r[i:], binary.LittleEndian.Uint16(v[i:])>>6)
		}
		_, err := y.w.Write(r)
		return err
	}
	if err := put(pic.Y); err != nil {
		return err
	}
	// Split the interleaved chroma into Cb, then Cr.
	cw := w / 2
	for c := range 2 {
		plane := make([]byte, 0, cw*h/2*bps)
		for row := range h / 2 {
			uv := pic.UV[row*pic.Pitch:]
			for x := range cw {
				i := (2*x + c) * bps
				plane = append(plane, uv[i:i+bps]...)
			}
		}
		if err := put(plane); err != nil {
			return err
		}
	}
	return nil
}

func (y *flatY4M) flush() error { return y.w.Flush() }

// frameSize is the encoded frame's size for a picture: one view for a 2D
// conversion, two side by side (or squeezed into one width) for 3D.
func (r *Runner) frameSize(p picture) (w, h int) {
	w, h, _ = p.size()
	if r.flat || r.Opts.Layout == LayoutHalfSBS {
		return w, h
	}
	return 2 * w, h
}

// resolveDepth settles an automatic bit depth from the source's: 10 bits
// from a source of more than 8 (HEVC on an Ultra HD Blu-ray, HDR) when the
// codec can, 8 otherwise.
func (r *Runner) resolveDepth(source int) {
	if r.Opts.BitDepth != 0 {
		return
	}
	r.Opts.BitDepth = 8
	if source > 8 && r.Opts.Codec != CodecH264 {
		r.Opts.BitDepth = 10
	}
}

// standardRates are the frame rates video uses, as fractions.
var standardRates = [][2]int{{24000, 1001}, {24, 1}, {25, 1}, {30000, 1001}, {30, 1}, {48, 1}, {50, 1},
	{60000, 1001}, {60, 1}, {100, 1}, {120000, 1001}, {120, 1}}

// rateFromDuration is the standard frame rate a frame duration is (within
// 0.2%), or 0 when it is none of them.
func rateFromDuration(d time.Duration) (num, den int) {
	if d <= 0 {
		return 0, 0
	}
	fps := float64(time.Second) / float64(d)
	for _, r := range standardRates {
		if want := float64(r[0]) / float64(r[1]); math.Abs(fps-want) < want*0.002 {
			return r[0], r[1]
		}
	}
	return 0, 0
}

// rateFromCode is the frame rate a Blu-ray clip info's frame_rate code
// states.
func rateFromCode(code byte) (num, den int) {
	switch code {
	case 1:
		return 24000, 1001
	case 2:
		return 24, 1
	case 3:
		return 25, 1
	case 4:
		return 30000, 1001
	case 6:
		return 50, 1
	case 7:
		return 60000, 1001
	}
	return 0, 0
}
