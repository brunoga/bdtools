package convert

import (
	"encoding/binary"
	"runtime"
	"sync"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/mvc"
)

// drawFramed draws a stereo frame into the encoder's picture in the
// layout: side by side, or over-under (the left eye on top), each full
// or squeezed to half.
func drawFramed(p *gpu.Picture, sf *mvc.StereoFrame, swap bool, l Layout) {
	if !l.overUnder() {
		drawSBS(p, sf, swap, l.halved())
		return
	}
	a, b := sf.Base, sf.Dependent
	if b == nil {
		b = a
	}
	if swap {
		a, b = b, a
	}
	half := l.halved()
	eyeH := a.Height
	if half {
		eyeH /= 2
	}
	// Bands of output rows (in pairs, for the chroma), each eye's apart.
	rows := eyeH / 2
	bands := min(4, runtime.GOMAXPROCS(0), max(rows/8, 1))
	var wg sync.WaitGroup
	for e, f := range [2]*mvc.Frame{a, b} {
		for i := range bands {
			wg.Go(func() { drawOURows(p, f, e*eyeH, half, rows*i/bands, rows*(i+1)/bands) })
		}
	}
	wg.Wait()
}

// drawOURows draws chroma rows [from, to) of one eye (and their luma rows)
// at output luma row top: copied, or halved in height with the 4-tap
// filter (-1, 5, 5, -1)/8 the side-by-side squeeze uses across columns.
func drawOURows(p *gpu.Picture, f *mvc.Frame, top int, half bool, from, to int) {
	w := f.Width
	wide := p.Depth > 8
	// put writes a filtered sum (eight times the value) at sample i of
	// dst, every step-th sample.
	put := func(dst []byte, i, sum8 int) {
		if wide {
			v := min(max((sum8+1)>>1, 0), 1023)                    // the 10-bit value: four times the 8-bit one
			binary.LittleEndian.PutUint16(dst[2*i:], uint16(v<<6)) //nolint:gosec // 10 bits
			return
		}
		dst[i] = byte(min(max((sum8+4)>>3, 0), 255))
	}
	// row gives source row k of a plane (clamped), n samples.
	row := func(pl []byte, stride, n, rows, k int) []byte {
		k = min(max(k, 0), rows-1)
		return pl[k*stride : k*stride+n]
	}
	for y := 2 * from; y < 2*to; y++ {
		dst := p.Y[(top+y)*p.Pitch:]
		if !half {
			if wide {
				widen(dst[:2*w], f.Y[y*f.StrideY:y*f.StrideY+w])
			} else {
				copy(dst[:w], f.Y[y*f.StrideY:y*f.StrideY+w])
			}
			continue
		}
		a, b := row(f.Y, f.StrideY, w, f.Height, 2*y-1), row(f.Y, f.StrideY, w, f.Height, 2*y)
		c, d := row(f.Y, f.StrideY, w, f.Height, 2*y+1), row(f.Y, f.StrideY, w, f.Height, 2*y+2)
		for x := range w {
			put(dst, x, -int(a[x])+5*int(b[x])+5*int(c[x])-int(d[x]))
		}
	}
	cw, ch := w/2, f.Height/2
	for y := from; y < to; y++ {
		dst := p.UV[(top/2+y)*p.Pitch:]
		for c, pl := range [2][]byte{f.Cb, f.Cr} {
			if !half {
				r := pl[y*f.StrideC : y*f.StrideC+cw]
				for x := range cw {
					put(dst, 2*x+c, 8*int(r[x]))
				}
				continue
			}
			ra, rb := row(pl, f.StrideC, cw, ch, 2*y-1), row(pl, f.StrideC, cw, ch, 2*y)
			rc, rd := row(pl, f.StrideC, cw, ch, 2*y+1), row(pl, f.StrideC, cw, ch, 2*y+2)
			for x := range cw {
				put(dst, 2*x+c, -int(ra[x])+5*int(rb[x])+5*int(rc[x])-int(rd[x]))
			}
		}
	}
}
