package convert

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/brunoga/bdtools/internal/dovi"
	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/internal/hdr"
)

// Tone mapping (--tonemap): an HDR picture (PQ or HLG, BT.2020) made SDR
// (BT.709, BT.1886's gamma 2.4), for screens and players that show HDR
// wrongly or not at all.
//
//   - The picture's light, in cd/m², from its PQ (or HLG, at a 1000 cd/m²
//     display) code values.
//   - Each pixel's max(R,G,B) brought into SDR's range by BT.2390's EETF,
//     in the PQ domain, and its three components scaled alike: the hue
//     and saturation stay. SDR white is HDR's reference white, 203 cd/m²
//     (BT.2408). The curve is the identity up to its knee, so midtones
//     keep their light, and bends the highlights down to the white.
//   - What it bends from is the brightest the picture gets: the shot's
//     own where the source says so (Dolby Vision's level 1 metadata, a
//     shot at a time; HDR10+'s, a scene at a time), else the film's
//     (MaxCLL, or the mastering display's peak), else 1000 cd/m².
//   - BT.2020's colours into BT.709's: a colour outside it moved toward
//     the grey of its luminance until it is inside, its hue kept.

const (
	sdrWhite = 203.0 // cd/m²: HDR's reference white, SDR's white
	lutSize  = 4096
)

// PQ's constants (SMPTE ST 2084).
const (
	pqM1 = 2610.0 / 16384
	pqM2 = 2523.0 / 4096 * 128
	pqC1 = 3424.0 / 4096
	pqC2 = 2413.0 / 4096 * 32
	pqC3 = 2392.0 / 4096 * 32
)

// pqToNits is PQ's EOTF: a code value (0 to 1) to cd/m².
func pqToNits(e float64) float64 {
	p := math.Pow(max(e, 0), 1/pqM2)
	return 10000 * math.Pow(max(p-pqC1, 0)/(pqC2-pqC3*p), 1/pqM1)
}

// nitsToPQ is its inverse.
func nitsToPQ(l float64) float64 {
	y := math.Pow(max(l, 0)/10000, pqM1)
	return math.Pow((pqC1+pqC2*y)/(1+pqC3*y), pqM2)
}

// hlgToScene is HLG's inverse OETF: a code value to scene light, 0 to 1.
func hlgToScene(e float64) float64 {
	const a, b, c = 0.17883277, 0.28466892, 0.55991073
	if e <= 0.5 {
		return e * e / 3
	}
	return (math.Exp((e-c)/a) + b) / 12
}

// eetf is BT.2390's EETF over PQ code values: the source's [0, from]
// into [0, to].
func eetf(e, from, to float64) float64 {
	if from <= to {
		return e
	}
	e1 := e / from
	maxLum := to / from
	ks := 1.5*maxLum - 0.5
	if e1 >= ks {
		t := (min(e1, 1) - ks) / (1 - ks)
		t2, t3 := t*t, t*t*t
		e1 = (2*t3-3*t2+1)*ks + (t3-2*t2+t)*(1-ks) + (-2*t3+3*t2)*maxLum
	}
	return e1 * from
}

// toneMapper makes HDR pictures SDR.
type toneMapper struct {
	hlg    bool
	static float64 // the film's peak, cd/m²
	where  string  // what the peak is from, for the report
	peak   float64 // the curve's, now
	// toLight is the source code value's light (cd/m²); curve maps a
	// max(R,G,B) code value to the light it becomes; gamma takes linear
	// light (0 to 1, by its fourth root) to BT.1886's code value.
	toLight, curve, gamma [lutSize + 1]float32
	out                   gpu.DecodedPicture
}

func newToneMapper(transfer int, static hdr.Static) *toneMapper {
	t := &toneMapper{hlg: transfer == 18, static: 1000, where: "an assumed 1000 cd/m²"}
	switch {
	case t.hlg:
		t.where = "HLG's 1000 cd/m² display"
	case static.Light != nil && static.Light.MaxCLL > 0:
		t.static, t.where = float64(static.Light.MaxCLL), fmt.Sprintf("its MaxCLL, %d cd/m²", static.Light.MaxCLL)
	case static.Mastering != nil && static.Mastering.MaxLuma > 0:
		t.static = float64(static.Mastering.MaxLuma) / 10000
		t.where = fmt.Sprintf("its mastering display's %.0f cd/m²", t.static)
	}
	for i := range lutSize + 1 {
		e := float64(i) / lutSize
		if t.hlg {
			t.toLight[i] = float32(hlgToScene(e))
		} else {
			t.toLight[i] = float32(pqToNits(e))
		}
		t.gamma[i] = float32(math.Pow(e, 4/2.4))
	}
	t.setPeak(t.static)
	return t
}

// setPeak bends the curve from a peak.
func (t *toneMapper) setPeak(peak float64) {
	peak = min(max(peak, sdrWhite), 10000)
	if peak == t.peak {
		return
	}
	t.peak = peak
	from, to := nitsToPQ(peak), nitsToPQ(sdrWhite)
	for i := range lutSize + 1 {
		t.curve[i] = float32(pqToNits(eetf(float64(i)/lutSize, from, to)) / sdrWhite)
	}
}

// describe says what the tone mapping does, for the report.
func (t *toneMapper) describe(dynamic string) string {
	kind := "HDR10 (PQ)"
	if t.hlg {
		kind = "HLG"
	}
	from := t.where
	if dynamic != "" && !t.hlg {
		from = dynamic + ", else " + t.where
	}
	return fmt.Sprintf("tone mapping %s to SDR (BT.709, BT.1886) by BT.2390, from the brightest the picture gets: %s", kind, from)
}

// pictureMeta sets the peak from a picture's own metadata, if it has any.
func (t *toneMapper) pictureMeta(p picture) {
	if t.hlg {
		return
	}
	if p.rpu != nil {
		if u, err := dovi.ParseNAL(p.rpu); err == nil {
			if _, maxPQ, _, ok := u.Brightness(); ok && maxPQ > 0 {
				t.setPeak(pqToNits(float64(maxPQ) / 4095))
				return
			}
		}
	}
	if p.hdr10Plus != nil {
		if n, ok := hdr.HDR10PlusPeak(p.hdr10Plus); ok {
			t.setPeak(n)
			return
		}
	}
	t.setPeak(t.static)
}

// lut reads a table at v (0 to 1), between its entries.
func lut(tab *[lutSize + 1]float32, v float32) float32 {
	v = min(max(v, 0), 1) * lutSize
	i := int(v)
	if i >= lutSize {
		return tab[lutSize]
	}
	f := v - float32(i)
	return tab[i] + f*(tab[i+1]-tab[i])
}

// apply tone maps src (NV12 or P010, BT.2020 limited range) into the
// mapper's own picture, valid until the next call.
func (t *toneMapper) apply(src *gpu.DecodedPicture) *gpu.DecodedPicture {
	o := &t.out
	bps := 1
	if src.Depth > 8 {
		bps = 2
	}
	if o.Y == nil || o.Width != src.Width || o.Height != src.Height || o.Depth != src.Depth {
		o.Pitch = src.Width * bps
		o.Y = make([]byte, o.Pitch*src.Height)
		o.UV = make([]byte, o.Pitch*src.Height/2)
	}
	o.Width, o.Height, o.Depth = src.Width, src.Height, src.Depth
	o.PTS, o.FrameRateNum, o.FrameRateDen = src.PTS, src.FrameRateNum, src.FrameRateDen
	o.Color = gpu.ColorInfo{Primaries: 1, Transfer: 1, Matrix: 1}
	scale := float32(int(1) << (src.Depth - 8))
	full := src.Color.FullRange
	parallelRows(src.Height/2, func(r0, r1 int) {
		for r := r0; r < r1; r++ {
			t.rowPair(o, src, 2*r, bps, scale, full)
		}
	})
	return o
}

// rowPair tone maps rows y and y+1 and their chroma row.
func (t *toneMapper) rowPair(o, src *gpu.DecodedPicture, y, bps int, scale float32, full bool) {
	read := func(b []byte, i int) float32 {
		if bps == 1 {
			return float32(b[i])
		}
		return float32(binary.LittleEndian.Uint16(b[2*i:]) >> 6)
	}
	write := func(b []byte, i int, v float32) {
		top := 255 * scale
		v = min(max(v+0.5, 0), top)
		if bps == 1 {
			b[i] = byte(v)
		} else {
			binary.LittleEndian.PutUint16(b[2*i:], uint16(v)<<6)
		}
	}
	// The source's code values to Y' (0 to 1) and Cb, Cr (-0.5 to 0.5).
	yOff, yRange, cRange := 16*scale, 219*scale, 224*scale
	if full {
		yOff, yRange, cRange = 0, 255*scale, 255*scale
	}
	cMid := 128 * scale
	w := src.Width
	rowY := [2][]byte{src.Y[y*src.Pitch:], src.Y[(y+1)*src.Pitch:]}
	outY := [2][]byte{o.Y[y*o.Pitch:], o.Y[(y+1)*o.Pitch:]}
	rowC, outC := src.UV[y/2*src.Pitch:], o.UV[y/2*o.Pitch:]
	// The output chroma sits on the even column: [1 2 1] across the
	// columns, both rows, the previous pair's right column carried.
	var prevCb, prevCr float32
	for x := 0; x < w; x += 2 {
		cb := (read(rowC, x) - cMid) / cRange
		cr := (read(rowC, x+1) - cMid) / cRange
		var sumCb, sumCr [2]float32 // the left column's, the right's
		for dy := range 2 {
			for dx := range 2 {
				yv := (read(rowY[dy], x+dx) - yOff) / yRange
				ny, ncb, ncr := t.pixel(yv, cb, cr)
				write(outY[dy], x+dx, 16*scale+219*scale*ny)
				sumCb[dx] += ncb
				sumCr[dx] += ncr
			}
		}
		if x == 0 {
			prevCb, prevCr = sumCb[0], sumCr[0]
		}
		ocb := (prevCb + 2*sumCb[0] + sumCb[1]) / 8
		ocr := (prevCr + 2*sumCr[0] + sumCr[1]) / 8
		write(outC, x, cMid+224*scale*ocb)
		write(outC, x+1, cMid+224*scale*ocr)
		prevCb, prevCr = sumCb[1], sumCr[1]
	}
}

// pixel tone maps one pixel: BT.2020 Y'CbCr in, BT.709 Y'CbCr out.
func (t *toneMapper) pixel(y, cb, cr float32) (float32, float32, float32) {
	// BT.2020's non-constant luminance matrix.
	r := y + 1.4746*cr
	g := y - 0.16455*cb - 0.57135*cr
	b := y + 1.8814*cb
	lr, lg, lb := lut(&t.toLight, r), lut(&t.toLight, g), lut(&t.toLight, b)
	var k float32
	if t.hlg {
		// HLG's OOTF at a 1000 cd/m² display, system gamma 1.2.
		ys := 0.2627*lr + 0.6780*lg + 0.0593*lb
		k = 1000 * float32(math.Pow(float64(max(ys, 1e-6)), 0.2))
		lr, lg, lb = lr*k, lg*k, lb*k
		m := max(lr, lg, lb)
		k = lut(&t.curve, float32(nitsToPQ(float64(m)))) / max(m, 1e-6)
	} else {
		// max(R,G,B)'s code value is the max of the three's.
		m := max(lr, lg, lb)
		k = lut(&t.curve, max(r, g, b)) / max(m, 1e-6)
	}
	lr, lg, lb = lr*k, lg*k, lb*k
	// Into BT.709's primaries.
	r7 := 1.6605*lr - 0.5876*lg - 0.0728*lb
	g7 := -0.1246*lr + 1.1329*lg - 0.0083*lb
	b7 := -0.0182*lr - 0.1006*lg + 1.1187*lb
	lum := 0.2126*r7 + 0.7152*g7 + 0.0722*b7
	if lum <= 0 {
		return 0, 0, 0
	}
	if lum >= 1 {
		return 1, 0, 0
	}
	// Outside BT.709 (or past white): toward the grey of its luminance.
	if lo := min(r7, g7, b7); lo < 0 {
		s := lum / (lum - lo)
		r7, g7, b7 = lum+(r7-lum)*s, lum+(g7-lum)*s, lum+(b7-lum)*s
	}
	if hi := max(r7, g7, b7); hi > 1 {
		s := (1 - lum) / (hi - lum)
		r7, g7, b7 = lum+(r7-lum)*s, lum+(g7-lum)*s, lum+(b7-lum)*s
	}
	q := func(v float32) float32 { return lut(&t.gamma, float32(math.Sqrt(math.Sqrt(float64(max(v, 0)))))) }
	rp, gp, bp := q(r7), q(g7), q(b7)
	yp := 0.2126*rp + 0.7152*gp + 0.0722*bp
	return yp, (bp - yp) / 1.8556, (rp - yp) / 1.5748
}

// setupToneMap readies --tonemap on t for a conversion whose first
// picture is p.
func (r *Runner) setupToneMap(t *picTransform, p picture, src pictureSource) error {
	if p.rpu != nil {
		if u, err := dovi.ParseNAL(p.rpu); err == nil && u.Profile() == 5 {
			return fmt.Errorf("--tonemap: Dolby Vision profile 5's picture (IPT, not HDR10) is not tone mapped here")
		}
	}
	var transfer int
	if p.gpu != nil {
		transfer = p.gpu.Color.Transfer
	} else {
		_, transfer, _, _ = p.stereo.ColorInfo()
	}
	if transfer != 16 && transfer != 18 {
		r.Report.Report("--tonemap: the source is not HDR: not tone mapped")
		return nil
	}
	t.tm = newToneMapper(transfer, src.hdrStatic())
	dynamic := ""
	switch {
	case p.rpu != nil:
		dynamic = "Dolby Vision's, shot by shot"
	case p.hdr10Plus != nil:
		dynamic = "HDR10+'s, scene by scene"
	}
	r.Report.Report("%s", t.tm.describe(dynamic))
	return nil
}
