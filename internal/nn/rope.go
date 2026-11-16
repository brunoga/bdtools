package nn

// Rope rotates the first n rows of x [N, D] in place by a rotary position
// encoding: each pair of channels (2j, 2j+1), taken as a complex number,
// times cos + i sin from row i % P of cs [P, D] (the cosine of pair j at
// 2j, its sine at 2j+1), as SAM 2's apply_rotary_enc does. A table of P
// rows serves keys that repeat its positions, one frame's after another.
func Rope(x, cs *Tensor, n int) *Tensor {
	d, p := x.Dim(1), cs.Dim(0)
	parallel(n, func(r int) {
		row, t := x.Data[r*d:(r+1)*d], cs.Data[r%p*d:(r%p+1)*d]
		for j := 0; j < d; j += 2 {
			re, im := row[j], row[j+1]
			row[j], row[j+1] = re*t[j]-im*t[j+1], re*t[j+1]+im*t[j]
		}
	})
	return x
}

func (g *GPU) Rope(x, cs *Tensor, n int) *Tensor {
	d := x.Dim(1)
	gr, bl := each(n * d / 2)
	g.launch("rope", gr, bl, 0, n, d, cs.Dim(0), ptr(x), ptr(cs))
	return x
}
