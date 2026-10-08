//go:build !amd64 || purego

package dovi

type mmrTable struct{}

func (k *mmrRow) prepare() {}

// row predicts a row of chroma samples from their luma, Cb and Cr.
func (k *mmrRow) row(ob, or, sy, sb, sr []float32) { k.rowGo(ob, or, sy, sb, sr) }

func lumaRow(out, bl []byte, el []int32, lut, res *[1024]float32) { lumaRowGo(out, bl, el, lut, res) }

func chromaPrep(sy, sb, sr []float32, y0, y1, uv []byte) { chromaPrepGo(sy, sb, sr, y0, y1, uv) }

func chromaStore(out []byte, ob, or []float32, eb, er []int32, resB, resR *[1024]float32) {
	chromaStoreGo(out, ob, or, eb, er, resB, resR)
}

func verticalY(out []int32, r0, r1, r2, r3 []byte, w [4]int32) { verticalYGo(out, r0, r1, r2, r3, w) }

func verticalUV(ob, or []int32, r0, r1, r2, r3 []byte, w [4]int32) {
	verticalUVGo(ob, or, r0, r1, r2, r3, w)
}

func horizontal(out, in []int32) { horizontalGo(out, in) }
