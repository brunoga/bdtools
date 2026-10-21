package nn

import "sync"

// The matrix product: c [m, n] (rows ldc apart) plus a [m, k] times b
// [k, n], each operand read through its strides (a[i, p] at i as0 + p as1,
// b[p, j] at p bs0 + j bs1), so a transposed operand is no copy. Blocked as
// BLAS libraries do: a block of b's rows (kc of them) packed into
// 16-column panels, a's rows into 6-row panels, each 6x16 tile of c the
// kernel's.

const (
	mr, nr = 6, 16   // the kernel's tile
	kc     = 256     // the rows of b (columns of a) a block
	mc     = mr * 16 // the rows of a a job
)

// packedB is b packed: for each block of kc rows, its 16-column panels
// (zero beyond n).
type packedB struct {
	k, n, np int
	data     []float32
}

// packB packs b [k, n] (b[p, j] at p bs0 + j bs1).
func packB(b []float32, bs0, bs1, k, n int) *packedB {
	np := (n + nr - 1) / nr
	pb := &packedB{k: k, n: n, np: np, data: make([]float32, k*np*nr)}
	nkb := (k + kc - 1) / kc
	parallel(nkb*np, func(job int) {
		kb, j := job/np, job%np
		k0 := kb * kc
		rows := min(kc, k-k0)
		dst := pb.data[k0*np*nr+j*rows*nr:]
		for p := range rows {
			row := dst[p*nr : (p+1)*nr]
			for x := range nr {
				if col := j*nr + x; col < n {
					row[x] = b[(k0+p)*bs0+col*bs1]
				} else {
					row[x] = 0
				}
			}
		}
	})
	return pb
}

// packBInto packs b [k, n] (b[p, j] at p bs0 + j bs1) on the calling
// goroutine, reusing pb's storage.
func packBInto(pb *packedB, b []float32, bs0, bs1, k, n int) {
	np := (n + nr - 1) / nr
	pb.k, pb.n, pb.np = k, n, np
	if cap(pb.data) < k*np*nr {
		pb.data = make([]float32, k*np*nr)
	}
	pb.data = pb.data[:k*np*nr]
	for k0 := 0; k0 < k; k0 += kc {
		rows := min(kc, k-k0)
		for j := range np {
			dst := pb.data[k0*np*nr+j*rows*nr:]
			for p := range rows {
				row := dst[p*nr : (p+1)*nr]
				src := b[(k0+p)*bs0:]
				for x := range nr {
					if col := j*nr + x; col < n {
						row[x] = src[col*bs1]
					} else {
						row[x] = 0
					}
				}
			}
		}
	}
}

// panel is panel j of the block of rows from k0 (rows of them).
func (pb *packedB) panel(k0, rows, j int) []float32 {
	off := k0*pb.np*nr + j*rows*nr
	return pb.data[off : off+rows*nr]
}

// aPool holds the buffers a's panels are packed into.
var aPool = sync.Pool{New: func() any { s := make([]float32, kc*mr); return &s }}

// mulPacked adds a [m, k] (a[i, p] at i as0 + p as1) times the packed b to
// c [m, n] (rows ldc apart), on the calling goroutine.
func mulPacked(c []float32, ldc int, a []float32, as0, as1 int, pb *packedB, m int) {
	k, n := pb.k, pb.n
	bufp := aPool.Get().(*[]float32)
	defer aPool.Put(bufp)
	pa := *bufp
	var tile [mr * nr]float32
	for k0 := 0; k0 < k; k0 += kc {
		kb := min(kc, k-k0)
		for r0 := 0; r0 < m; r0 += mr {
			rows := min(mr, m-r0)
			for p := range kb {
				col := (k0 + p) * as1
				for r := range mr {
					if r < rows {
						pa[p*mr+r] = a[(r0+r)*as0+col]
					} else {
						pa[p*mr+r] = 0
					}
				}
			}
			for j := range pb.np {
				pp := pb.panel(k0, kb, j)
				cols := min(nr, n-j*nr)
				ci := r0*ldc + j*nr
				if rows == mr && cols == nr {
					kernel(kb, pa, pp, c[ci:], ldc)
					continue
				}
				tile = [mr * nr]float32{}
				kernel(kb, pa, pp, tile[:], nr)
				for r := range rows {
					for x := range cols {
						c[ci+r*ldc+x] += tile[r*nr+x]
					}
				}
			}
		}
	}
}

// sgemm adds a [m, k] times b [k, n] to c [m, n] (rows ldc apart), over
// the CPUs: b packed once, a's rows in blocks of mc a job.
func sgemm(c []float32, ldc int, a []float32, as0, as1 int, b []float32, bs0, bs1 int, m, k, n int) {
	if m == 0 || n == 0 || k == 0 {
		return
	}
	pb := packB(b, bs0, bs1, k, n)
	parallel((m+mc-1)/mc, func(blk int) {
		i0 := blk * mc
		mulPacked(c[i0*ldc:], ldc, a[i0*as0:], as0, as1, pb, min(mc, m-i0))
	})
}

// kernelGo is the tile kernel in Go.
func kernelGo(k int, a, b, c []float32, ldc int) {
	var acc [mr * nr]float32
	for p := range k {
		ap, bp := a[p*mr:p*mr+mr], b[p*nr:p*nr+nr]
		for r, av := range ap {
			row := acc[r*nr : r*nr+nr]
			for x, bv := range bp {
				row[x] += av * bv
			}
		}
	}
	for r := range mr {
		for x := range nr {
			c[r*ldc+x] += acc[r*nr+x]
		}
	}
}

// gemmNT adds a [m, k] times b [n, k] transposed to c [m, n].
func gemmNT(c, a, b []float32, m, k, n int) { sgemm(c, n, a, k, 1, b, 1, k, m, k, n) }

// gemmNN adds a [m, k] times b [k, n] to c [m, n].
func gemmNN(c, a, b []float32, m, k, n int) { sgemm(c, n, a, k, 1, b, n, 1, m, k, n) }
