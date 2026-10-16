package convert

import (
	"sync"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/hdr"
	"github.com/brunoga/bdtools/mvc"
)

// A 3D source whose frame holds both eyes (side by side or over-under,
// full or squeezed to half): decoded as a 2D picture, then cut into its
// eyes, each at full size (a half eye scaled back up), so it converts to
// any 3D output as a disc's two views do. Eyes are 8-bit, as the 3D
// outputs take them.

// framedPictures cuts each picture of src into a stereo pair.
type framedPictures struct {
	src    pictureSource
	layout Layout
	swap   bool // the right eye comes first (Matroska's stereo modes 2 and 11)
	// leftOnly gives the left eye alone, as a 2D picture.
	leftOnly bool
	colour   func(gpu.ColorInfo)
	n        int
}

func (f *framedPictures) run(each func(picture) error) error {
	return f.src.run(func(p picture) error {
		if f.n == 0 && p.gpu != nil && f.colour != nil {
			f.colour(p.gpu.Color)
		}
		f.n++
		l, r := splitFramed(p, f.layout)
		if f.swap {
			l, r = r, l
		}
		if f.leftOnly {
			return each(picture{stereo: &mvc.StereoFrame{Base: l}, pts: p.pts})
		}
		return each(picture{stereo: &mvc.StereoFrame{Base: l, Dependent: r}, pts: p.pts})
	})
}

func (f *framedPictures) frameRate() (int, int) { return f.src.frameRate() }
func (f *framedPictures) errors() int           { return f.src.errors() }
func (f *framedPictures) dependentFrames() int {
	if f.leftOnly {
		return 0
	}
	return f.n
}
func (f *framedPictures) hdrStatic() hdr.Static { return f.src.hdrStatic() }

// framePlanes is a decoded picture as 8-bit planes: luma, and chroma
// either planar or interleaved (NV12/P010, its samples' high bytes).
type framePlanes struct {
	w, h          int
	y, cb, cr, uv []byte
	pitch, cpitch int
	step          int // bytes a sample: 1, or 2 (P010)
}

func planesOf(p picture) framePlanes {
	if g := p.gpu; g != nil {
		step := 1
		if g.Depth > 8 {
			step = 2
		}
		return framePlanes{w: g.Width, h: g.Height, y: g.Y, uv: g.UV, pitch: g.Pitch, step: step}
	}
	b := p.stereo.Base
	return framePlanes{w: b.Width, h: b.Height, y: b.Y, cb: b.Cb, cr: b.Cr, pitch: b.StrideY, cpitch: b.StrideC, step: 1}
}

// row gives row y of plane pl (0 luma, 1 Cb, 2 Cr) from x0, n samples, as
// 8-bit samples: the plane's own memory when it is that, else buf.
func (fp *framePlanes) row(pl, y, x0, n int, buf []byte) []byte {
	if pl == 0 {
		r := fp.y[y*fp.pitch:]
		if fp.step == 1 {
			return r[x0 : x0+n]
		}
		for i := range n {
			buf[i] = r[(x0+i)*2+1]
		}
		return buf[:n]
	}
	if fp.uv == nil {
		src := fp.cb
		if pl == 2 {
			src = fp.cr
		}
		return src[y*fp.cpitch+x0 : y*fp.cpitch+x0+n]
	}
	r := fp.uv[y*fp.pitch:]
	off := (pl-1)*fp.step + fp.step - 1
	for i := range n {
		buf[i] = r[(x0+i)*2*fp.step+off]
	}
	return buf[:n]
}

// splitFramed cuts a framed picture into its two eyes, the first one
// (left or top) first.
func splitFramed(p picture, l Layout) (*mvc.Frame, *mvc.Frame) {
	fp := planesOf(p)
	ew, eh := fp.w/2, fp.h // one eye's part of the frame
	if l.overUnder() {
		ew, eh = fp.w, fp.h/2
	}
	ow, oh := ew, eh // the eye's size out
	if l.halved() {
		if l.overUnder() {
			oh *= 2
		} else {
			ow *= 2
		}
	}
	var eyes [2]*mvc.Frame
	var wg sync.WaitGroup
	for e := range 2 {
		f := &mvc.Frame{Width: ow, Height: oh, StrideY: ow, StrideC: ow / 2,
			Y: make([]byte, ow*oh), Cb: make([]byte, ow*oh/4), Cr: make([]byte, ow*oh/4)}
		eyes[e] = f
		for pl, dst := range [3][]byte{f.Y, f.Cb, f.Cr} {
			sub := 1
			if pl > 0 {
				sub = 2
			}
			x0, y0 := e*ew/sub, 0
			if l.overUnder() {
				x0, y0 = 0, e*eh/sub
			}
			wg.Go(func() { cutPlane(&fp, pl, dst, ow/sub, oh/sub, ew/sub, eh/sub, x0, y0, l) })
		}
	}
	wg.Wait()
	return eyes[0], eyes[1]
}

// up interpolates between samples with the 4-tap filter (-1, 9, 9, -1)/16.
func up(a, b, c, d byte) byte {
	v := (-int(a) + 9*int(b) + 9*int(c) - int(d) + 8) >> 4
	return byte(min(max(v, 0), 255))
}

// cutPlane fills a plane of ow x oh from the eye's part (iw x ih at x0,
// y0) of plane pl: copied, or doubled across (side by side) or down
// (over-under).
func cutPlane(fp *framePlanes, pl int, dst []byte, ow, oh, iw, ih, x0, y0 int, l Layout) {
	buf := make([]byte, iw)
	switch {
	case !l.halved():
		for y := range oh {
			copy(dst[y*ow:(y+1)*ow], fp.row(pl, y0+y, x0, iw, buf))
		}
	case !l.overUnder():
		for y := range oh {
			r := fp.row(pl, y0+y, x0, iw, buf)
			d := dst[y*ow : (y+1)*ow]
			for x := range iw {
				d[2*x] = r[x]
				d[2*x+1] = up(r[max(x-1, 0)], r[x], r[min(x+1, iw-1)], r[min(x+2, iw-1)])
			}
		}
	default:
		// The eye's rows, then each one between two of them.
		rows := make([][]byte, ih)
		for k := range ih {
			rows[k] = dst[2*k*ow : (2*k+1)*ow]
			copy(rows[k], fp.row(pl, y0+k, x0, iw, buf))
		}
		for k := range ih {
			a, b, c, d := rows[max(k-1, 0)], rows[k], rows[min(k+1, ih-1)], rows[min(k+2, ih-1)]
			o := dst[(2*k+1)*ow : (2*k+2)*ow]
			for x := range iw {
				o[x] = up(a[x], b[x], c[x], d[x])
			}
		}
	}
}

// InputAuto is the Options.InputLayout that goes by what the source says of its
// frame (a Matroska stereo mode).
const InputAuto Layout = "auto"

// resolveFramed settles whether the 2D-coded video track v holds two
// eyes, and how.
func (r *Runner) resolveFramed(v Track) {
	l, swap := r.Opts.InputLayout, r.Opts.InputSwap
	if l == InputAuto {
		l = ""
		switch v.StereoMode {
		case 1, 11:
			l = LayoutHalfSBS
			if v.Height > 0 && 2*v.Width > 5*v.Height { // wider than two 16:9 eyes squeezed
				l = LayoutFullSBS
			}
			swap = v.StereoMode == 11
		case 2, 3:
			l = LayoutHalfOU
			if v.Height > 0 && 5*v.Width < 6*v.Height {
				l = LayoutFullOU
			}
			swap = v.StereoMode == 2
		}
		if l != "" {
			r.Report.Report("source: the file says its frame holds both eyes (%s); --input-layout 2d takes it as one picture", map[Layout]string{
				LayoutFullSBS: "side by side", LayoutHalfSBS: "half side by side", LayoutFullOU: "over-under", LayoutHalfOU: "half over-under"}[l])
		}
	}
	if !l.framed() {
		return
	}
	r.framedIn, r.framedSwap = l, swap
}

// framedSource wraps the decoded pictures of a framed source: cut into
// eyes, or the left eye alone for a 2D output.
func (r *Runner) framedSource(src pictureSource) pictureSource {
	return &framedPictures{src: src, layout: r.framedIn, swap: r.framedSwap, leftOnly: r.flat,
		colour: func(c gpu.ColorInfo) {
			if c.Primaries != 2 || c.Transfer != 2 || c.Matrix != 2 {
				r.colour, r.Opts.Color = &c, &c
			}
		}}
}
