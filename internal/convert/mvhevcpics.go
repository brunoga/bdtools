package convert

import (
	"errors"
	"io"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/hdr"
	"github.com/brunoga/bdtools/internal/hevc"
	"github.com/brunoga/bdtools/mvc"
)

// streamMVHEVC is the dependent view of an MV-HEVC source: the second
// layer of its HEVC track (Apple's spatial video, this program's
// --layout mvhevc).
const streamMVHEVC = "V_MPEGH/ISO/MVHEVC"

// mvhevcPictures decodes an MV-HEVC stream's two views with the HEVC
// decoder here and gives them as stereo pairs (8-bit, as the 3D outputs
// take them), the base layer the left eye.
type mvhevcPictures struct {
	next    func() (base, dep []byte, pts int64, err error)
	threads int
	colour  func(gpu.ColorInfo)
	rateN   int
	rateD   int
	errs    int
	pairs   int
}

func (m *mvhevcPictures) run(each func(picture) error) error {
	d := hevc.New()
	if m.threads > 0 {
		d.SetThreads(m.threads)
	}
	d.SetLayers(2)
	// The views of an access unit come out apart (each layer in its own
	// output order): a picture waits for its other view by time.
	waiting := map[int64][2]*mvc.Frame{}
	first := true
	out := func(p *hevc.Picture) error {
		if first {
			first = false
			m.rateN, m.rateD = p.FrameRateNum, p.FrameRateDen
			if m.colour != nil {
				m.colour(gpu.ColorInfo{Primaries: p.Primaries, Transfer: p.Transfer, Matrix: p.Matrix, FullRange: p.FullRange})
			}
		}
		w := waiting[p.PTS]
		w[p.Layer&1] = frame8(p)
		if w[0] == nil || w[1] == nil {
			waiting[p.PTS] = w
			return nil
		}
		delete(waiting, p.PTS)
		m.pairs++
		return each(picture{stereo: &mvc.StereoFrame{Base: w[0], Dependent: w[1]}, pts: p.PTS})
	}
	for {
		au, _, pts, err := m.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if err := d.Decode(au, pts, out); err != nil {
			return err
		}
	}
	if err := d.Flush(out); err != nil {
		return err
	}
	m.errs = d.Errors()
	return nil
}

// frame8 copies a decoded picture into an 8-bit frame.
func frame8(p *hevc.Picture) *mvc.Frame {
	w, h := p.Width, p.Height
	f := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: w / 2,
		Y: make([]byte, w*h), Cb: make([]byte, w*h/4), Cr: make([]byte, w*h/4)}
	sh := p.BitDepth - 8
	plane := func(dst []byte, src []uint16, stride, pw, ph int) {
		for y := range ph {
			row := src[y*stride : y*stride+pw]
			d := dst[y*pw : (y+1)*pw]
			for x, v := range row {
				d[x] = byte(v >> sh)
			}
		}
	}
	plane(f.Y, p.Y, p.StrideY, w, h)
	plane(f.Cb, p.Cb, p.StrideC, w/2, h/2)
	plane(f.Cr, p.Cr, p.StrideC, w/2, h/2)
	return f
}

func (m *mvhevcPictures) frameRate() (int, int) { return m.rateN, m.rateD }
func (m *mvhevcPictures) errors() int           { return m.errs }
func (m *mvhevcPictures) dependentFrames() int  { return m.pairs }
func (m *mvhevcPictures) hdrStatic() hdr.Static { return hdr.Static{} }
