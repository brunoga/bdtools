package nn

import "github.com/brunoga/bdtools/internal/cuda"

// The windowed attention of a hierarchical vision transformer (Hiera's,
// in SAM 2): the tokens of a grid, one a row, are cut into square windows
// that each attend within themselves, as many windows at once.

// WindowGrid is the windows of an h x w grid cut ws x ws: the grid
// padded to ph x pw, and how many windows that makes.
func WindowGrid(h, w, ws int) (ph, pw, n int) {
	ph, pw = (h+ws-1)/ws*ws, (w+ws-1)/ws*ws
	return ph, pw, ph / ws * (pw / ws)
}

// WindowPartition is the rows of an h x w grid x [h w, C] in windows of ws
// x ws (window_partition): [windows ws ws, C], each window's rows in
// raster order and the windows too, the grid padded with zeros on the
// right and at the bottom to whole windows.
func WindowPartition(x *Tensor, h, w, ws int) *Tensor {
	c := x.Dim(1)
	_, pw, n := WindowGrid(h, w, ws)
	y := New(n*ws*ws, c)
	nwx := pw / ws
	parallel(n, func(win int) {
		for t := range ws * ws {
			Y, X := win/nwx*ws+t/ws, win%nwx*ws+t%ws
			if Y < h && X < w {
				copy(y.Data[(win*ws*ws+t)*c:(win*ws*ws+t+1)*c], x.Data[(Y*w+X)*c:])
			}
		}
	})
	return y
}

// WindowUnpartition is WindowPartition undone: the h x w grid's rows
// [h w, C] from its windows x, the padding dropped.
func WindowUnpartition(x *Tensor, h, w, ws int) *Tensor {
	c := x.Dim(1)
	_, pw, _ := WindowGrid(h, w, ws)
	nwx := pw / ws
	y := New(h*w, c)
	parallel(h, func(Y int) {
		for X := range w {
			r := (Y/ws*nwx+X/ws)*ws*ws + Y%ws*ws + X%ws
			copy(y.Data[(Y*w+X)*c:(Y*w+X+1)*c], x.Data[r*c:])
		}
	})
	return y
}

// WindowPool is the 2 x 2 maximum within each window of x [windows ws
// ws, C] (ws even): [windows (ws/2)^2, C], nn.MaxPool2d(2, 2) on each.
// A whole grid is one window.
func WindowPool(x *Tensor, ws int) *Tensor {
	c := x.Dim(1)
	n := x.Dim(0) / (ws * ws)
	hs := ws / 2
	y := New(n*hs*hs, c)
	parallel(n, func(win int) {
		for t := range hs * hs {
			p := win*ws*ws + 2*(t/hs)*ws + 2*(t%hs)
			dst := y.Data[(win*hs*hs+t)*c : (win*hs*hs+t+1)*c]
			for j := range dst {
				dst[j] = max(x.Data[p*c+j], x.Data[(p+1)*c+j], x.Data[(p+ws)*c+j], x.Data[(p+ws+1)*c+j])
			}
		}
	})
	return y
}

// AttentionBatch is Attention within each of b batches: q [b Nq, D] and k,
// v [b Nk, D], each batch's rows attending only to its own.
func AttentionBatch(q, k, v *Tensor, b, heads int, scale float32) *Tensor {
	nq, nk, d := q.Dim(0)/b, k.Dim(0)/b, q.Dim(1)
	out := New(b*nq, d)
	slice := func(t *Tensor, i, n int) *Tensor {
		return &Tensor{Shape: []int{n, d}, Data: t.Data[i*n*d : (i+1)*n*d]}
	}
	if b == 1 {
		return Attention(q, k, v, heads, scale)
	}
	parallel(b, func(i int) {
		o := attentionSerial(slice(q, i, nq), slice(k, i, nk), slice(v, i, nk), heads, scale)
		copy(out.Data[i*nq*d:], o.Data)
	})
	return out
}

// attentionSerial is Attention on one goroutine, for many small ones at
// once.
func attentionSerial(q, k, v *Tensor, heads int, scale float32) *Tensor {
	nq, nk, d := q.Dim(0), k.Dim(0), q.Dim(1)
	hd := d / heads
	out := New(nq, d)
	scores := make([]float32, nk)
	for h := range heads {
		lo := h * hd
		for i := range nq {
			qi := q.Data[i*d+lo : i*d+lo+hd]
			for j := range nk {
				kj := k.Data[j*d+lo : j*d+lo+hd]
				var s float32
				for e, a := range qi {
					s += a * kj[e]
				}
				scores[j] = s * scale
			}
			softmaxRow(scores)
			oi := out.Data[i*d+lo : i*d+lo+hd]
			for j, p := range scores {
				vj := v.Data[j*d+lo : j*d+lo+hd]
				for e := range oi {
					oi[e] += p * vj[e]
				}
			}
		}
	}
	return out
}

func (g *GPU) WindowPartition(x *Tensor, h, w, ws int) *Tensor {
	c := x.Dim(1)
	_, pw, n := WindowGrid(h, w, ws)
	y := g.tensor(n*ws*ws, c)
	total := n * ws * ws * c
	gr, bl := each(total)
	g.launch("win_part", gr, bl, 0, h, w, c, ws, pw, ptr(x), ptr(y), int64(total))
	return y
}

func (g *GPU) WindowUnpartition(x *Tensor, h, w, ws int) *Tensor {
	c := x.Dim(1)
	_, pw, _ := WindowGrid(h, w, ws)
	y := g.tensor(h*w, c)
	gr, bl := each(h * w * c)
	g.launch("win_unpart", gr, bl, 0, h, w, c, ws, pw, ptr(x), ptr(y))
	return y
}

func (g *GPU) WindowPool(x *Tensor, ws int) *Tensor {
	c := x.Dim(1)
	n := x.Dim(0) / (ws * ws)
	hs := ws / 2
	y := g.tensor(n*hs*hs, c)
	total := n * hs * hs * c
	gr, bl := each(total)
	g.launch("win_pool", gr, bl, 0, c, ws, ptr(x), ptr(y), int64(total))
	return y
}

func (g *GPU) AttentionBatch(q, k, v *Tensor, b, heads int, scale float32) *Tensor {
	if b == 1 {
		return g.Attention(q, k, v, heads, scale)
	}
	nq, nk, d := q.Dim(0)/b, k.Dim(0)/b, q.Dim(1)
	hd := d / heads
	split := func(x *Tensor, n int) *Tensor {
		y := g.tensor(b*heads*n, hd)
		gr, bl := each(b * n * d)
		g.launch("heads_split", gr, bl, 0, n, heads, hd, ptr(x), ptr(y), int64(b*n*d))
		return y
	}
	qs, ks, vs := split(q, nq), split(k, nk), split(v, nk)
	bh := b * heads
	scores := g.tensor(bh, nq, nk)
	g.gemm("gemm_nt", nq, nk, hd, ptr(qs), hd, nq*hd, ptr(ks), hd, nk*hd, ptr(scores), nk, nq*nk, 1, 0, 0, bh)
	g.launch("softmax", cuda.Dim{X: bh * nq}, cuda.Dim{X: 256}, 0, nk, ptr(scores), scale)
	o := g.tensor(bh*nq, hd)
	g.gemm("gemm_nn", nq, hd, nk, ptr(scores), nk, nq*nk, ptr(vs), hd, nk*hd, ptr(o), hd, nq*hd, 1, 0, 0, bh)
	out := g.tensor(b*nq, d)
	gr, bl := each(b * nq * d)
	g.launch("heads_merge", gr, bl, 0, nq, heads, hd, ptr(o), ptr(out), int64(b*nq*d))
	return out
}
