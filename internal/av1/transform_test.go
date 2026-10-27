package av1

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// The golden hashes below come from a C harness (in ~/mvc-scratch/av1/
// itxharness, not part of the repository) built against libaom's C
// transforms: av1_inv_txfm2d_add_*_c and av1_highbd_iwht4x4_16_add_c, with
// their final clip-and-add replaced by a store of the residual (flipped as
// libaom's add flips it), and av1_fwd_txfm2d_*_c and av1_fwht4x4_c. libaom's
// decoder is conformant, so its inverse is the spec's. Both sides draw the
// same blocks from the xorshift generator below: for bit depths 8 and 10,
// every size and every type valid at it (ADST up to 16, identity up to 32),
// 16 + 16384/(w*h) blocks each of coefficients of four kinds (sparse and
// decaying with frequency, dense and small, extreme values at the lowest
// frequencies, dense across the whole dequantizer range), and residuals in
// [-(2^BitDepth-1), 2^BitDepth-1] for the forward transform; and 1024
// lossless 4x4 blocks. Each line holds an FNV-1a hash, per depth and size,
// of the per-type hashes of every output (inverse residuals in picture
// orientation; forward coefficients in the spec's layout). The inverse
// matches libaom bit for bit, and so does the forward port.

var transformGolden = []struct {
	bitDepth, txSz int
	inv, fwd       uint64
}{
	{8, 0, 0xe9c9ec76e09fa1d2, 0xbc69afa724476a1e},
	{8, 1, 0x006d595f480ffd78, 0x7f623067f4783de0},
	{8, 2, 0x27792cc781cd0123, 0x56e43519b1d87f41},
	{8, 3, 0x8f2e77cd0fc562ef, 0xa9673ad1439a8370},
	{8, 4, 0xa69b03582bd5fdf5, 0x4ecd07a7c33c0532},
	{8, 5, 0xef400f73102d267d, 0x73d3a86325cbc1d3},
	{8, 6, 0x1a86541ebc81b4ad, 0x1cabadbc6adc49cb},
	{8, 7, 0x69b9bea385a95a3a, 0xf3984c3fc9817a78},
	{8, 8, 0x5048ad080cb8fb4b, 0x0724100891d8f29e},
	{8, 9, 0x7a9f55e399e10b02, 0x3a9a91051e77b69e},
	{8, 10, 0x28595824113e8997, 0xabe976425827a1bc},
	{8, 11, 0x813af67ca83a2285, 0x007a5af45edf6a14},
	{8, 12, 0xd1895bddca7a02e9, 0x380b97235663ae95},
	{8, 13, 0x3b2911dd22c51e67, 0x1b0ef975ad57cc68},
	{8, 14, 0x7cad1a0e41e02180, 0x9eeb2b5c70d97684},
	{8, 15, 0x9d9971ca8b8a8e59, 0xeced7fbfe1c9757c},
	{8, 16, 0xd5ea11d8d4080388, 0xfd5c412fd1d52f19},
	{8, 17, 0x21dc9fb4ab38c592, 0x0aab29f983f79b53},
	{8, 18, 0x3c9464587898a1b6, 0x73cf32e0f3244f07},
	{8, -1, 0x9d978a7063e19f86, 0x13da5fbeb6114161},
	{10, 0, 0x15d1b51bd0a94812, 0xd062264a4f2de45a},
	{10, 1, 0x70f745fe90e22a13, 0x1656247100df2626},
	{10, 2, 0xacd917218f13690a, 0x453c9ae95b851ae8},
	{10, 3, 0xcb204dbadfc11d72, 0xb84fc85791780903},
	{10, 4, 0xcecf355cc58d9fc8, 0x3d6a9f813485337f},
	{10, 5, 0xd31845cd5916d1b2, 0x0f3ec63343ad8fc4},
	{10, 6, 0xcd5ed59f9652cca3, 0xbfb22ec94a16202f},
	{10, 7, 0xdc79048224f372d6, 0x5edd2d89258b925b},
	{10, 8, 0xda54bebfc9d3399c, 0xb5f8cb973c9bd50b},
	{10, 9, 0xe0024261baac5942, 0x0975900fca19e40f},
	{10, 10, 0x029135a2c61e6e1e, 0x906712fde1251a30},
	{10, 11, 0xcd45837177dd41fd, 0x6e8748cbcc24bedb},
	{10, 12, 0x3ff0c911f8a32a88, 0xda758573f777e2ce},
	{10, 13, 0x8599971dd6589c0b, 0x75899ffacfb916fa},
	{10, 14, 0x767aa36e32c6d897, 0x820943a285de5c61},
	{10, 15, 0x69244b103ba92ac5, 0xbe227abb25e0183e},
	{10, 16, 0x1e6e5feb1ad4d6d1, 0xa96d7aa413a2d9d0},
	{10, 17, 0xbf36cd892f9917f0, 0x00ab54d560f439f5},
	{10, 18, 0xafe5e5591444342e, 0x79dbccbe5f187592},
	{10, -1, 0x2ab5ca53591a7e46, 0x812c17739eed6fbf},
}

// xorshift32 is the generator the C harness shares.
type xorshift32 uint32

func (x *xorshift32) next() uint32 {
	v := uint32(*x)
	v ^= v << 13
	v ^= v >> 17
	v ^= v << 5
	*x = xorshift32(v)
	return v
}

const fnvOffset = 14695981039346656037

func fnv32s(h uint64, v []int32) uint64 {
	for _, x := range v {
		u := uint32(x)
		for b := 0; b < 4; b++ {
			h ^= uint64(u >> (8 * b) & 255)
			h *= 1099511628211
		}
	}
	return h
}

func fnv64(h, v uint64) uint64 {
	return fnv32s(h, []int32{int32(uint32(v)), int32(uint32(v >> 32))})
}

// genCoeffs draws a coefficient block of one of four kinds, as the harness
// does.
func genCoeffs(rs *xorshift32, c []int32, tw, th, bitDepth, blk int) {
	lim := int32(1)<<(bitDepth+7) - 1
	for i := 0; i < th; i++ {
		for j := 0; j < tw; j++ {
			var v int32
			r := rs.next()
			switch blk & 3 {
			case 0:
				if r&7 == 0 {
					m := int32(1<<(bitDepth+4)) / int32(1+i+j)
					v = int32(rs.next() % uint32(m+1))
					if r&8 != 0 {
						v = -v
					}
				}
			case 1:
				m := int32(1) << (bitDepth - 4)
				v = int32(r%uint32(2*m+1)) - m
			case 2:
				if i < 2 && j < 2 && r&1 != 0 {
					v = -lim - 1
					if r&2 != 0 {
						v = lim
					}
				}
			default:
				v = int32(r%uint32(2*lim+2)) - lim - 1
				if rs.next()&3 != 0 {
					v >>= (i + j) & 15
				}
			}
			c[i*tw+j] = v
		}
	}
}

// flipResidual puts a residual in picture orientation, as reconstructAdd
// places it.
func flipResidual(res, out []int32, w, h, txType int) {
	for i := 0; i < h; i++ {
		for j := 0; j < w; j++ {
			yy, xx := i, j
			if txFlipUD[txType] {
				yy = h - 1 - i
			}
			if txFlipLR[txType] {
				xx = w - 1 - j
			}
			out[yy*w+xx] = res[i*w+j]
		}
	}
}

func TestTransformGolden(t *testing.T) {
	coef := make([]int32, 1024)
	res := make([]int32, 4096)
	pic := make([]int32, 4096)
	src := make([]int32, 4096)
	for _, g := range transformGolden {
		var hi, hf uint64 = fnvOffset, fnvOffset
		bd := g.bitDepth
		srcMax := int32(1)<<bd - 1
		if g.txSz < 0 {
			rs := xorshift32(0x9E3779B9 ^ uint32(bd*1000+999))
			for blk := 0; blk < 1024; blk++ {
				m := int32(1) << (bd + 2)
				for i := range coef[:16] {
					coef[i] = int32(rs.next()%uint32(2*m+1)) - m
				}
				inverseTransform(coef, TX_4X4, DCT_DCT, bd, true, res)
				hi = fnv32s(hi, res[:16])
				for i := range src[:16] {
					src[i] = int32(rs.next()%uint32(2*srcMax+1)) - srcMax
				}
				forwardTransform(src, TX_4X4, DCT_DCT, bd, true, coef)
				hf = fnv32s(hf, coef[:16])
			}
		} else {
			w, h := int(txWidth[g.txSz]), int(txHeight[g.txSz])
			tw, th := min(w, 32), min(h, 32)
			for typ := 0; typ < TX_TYPES; typ++ {
				if !txTypeValid(g.txSz, typ) {
					continue
				}
				var ht, hft uint64 = fnvOffset, fnvOffset
				rs := xorshift32(0x9E3779B9 ^ uint32(bd*1000+g.txSz*16+typ))
				for blk := 0; blk < 16+16384/(w*h); blk++ {
					genCoeffs(&rs, coef, tw, th, bd, blk)
					inverseTransform(coef, g.txSz, typ, bd, false, res)
					flipResidual(res, pic, w, h, typ)
					ht = fnv32s(ht, pic[:w*h])
					for i := range src[:w*h] {
						src[i] = int32(rs.next()%uint32(2*srcMax+1)) - srcMax
					}
					forwardTransform(src, g.txSz, typ, bd, false, coef)
					hft = fnv32s(hft, coef[:tw*th])
				}
				hi = fnv64(hi, ht)
				hf = fnv64(hf, hft)
			}
		}
		if hi != g.inv {
			t.Errorf("bit depth %d size %d: inverse hash %#016x, libaom %#016x", bd, g.txSz, hi, g.inv)
		}
		if hf != g.fwd {
			t.Errorf("bit depth %d size %d: forward hash %#016x, libaom %#016x", bd, g.txSz, hf, g.fwd)
		}
	}
}

// TestTransformRoundTrip checks that the forward transform is at the
// inverse's scale: a residual through the forward transform, the inverse
// and reconstructAdd comes back within a small error for every size, type
// and depth. The coefficients go to the inverse as they are: libaom's
// forward output is at the scale of the spec's Dequant for every size (its
// quantizer, not the transform, deals with dqDenom). Residuals are random
// across the whole range; for sizes with a 64-sample dimension, whose
// upper 32 frequencies are dropped, they are instead made of low
// frequencies (the inverse of random low-frequency coefficients), which
// the dropped half does not carry.
func TestTransformRoundTrip(t *testing.T) {
	coef := make([]int32, 1024)
	res := make([]int32, 4096)
	src := make([]int32, 4096)
	rec := make([]uint16, 4096)
	for _, bd := range []int{8, 10} {
		srcMax := int32(1)<<bd - 1
		for txSz := 0; txSz < TX_SIZES_ALL; txSz++ {
			w, h := int(txWidth[txSz]), int(txHeight[txSz])
			tw, th := min(w, 32), min(h, 32)
			for typ := 0; typ < TX_TYPES; typ++ {
				if !txTypeValid(txSz, typ) {
					continue
				}
				rs := xorshift32(12345 + txSz*16 + typ + bd)
				worst := int32(0)
				for blk := 0; blk < 64; blk++ {
					if w == 64 || h == 64 {
						for i := 0; i < th; i++ {
							for j := 0; j < tw; j++ {
								coef[i*tw+j] = 0
								if i < 8 && j < 8 {
									coef[i*tw+j] = int32(rs.next()%512) - 256
								}
							}
						}
						coef[0] = int32(rs.next()%4096) - 2048
						inverseTransform(coef, txSz, typ, bd, false, res)
						flipResidual(res, src, w, h, typ)
						for i := range src[:w*h] {
							src[i] = min(max(src[i], -srcMax), srcMax)
						}
					} else {
						for i := range src[:w*h] {
							src[i] = int32(rs.next()%uint32(2*srcMax+1)) - srcMax
						}
					}
					forwardTransform(src, txSz, typ, bd, false, coef)
					inverseTransform(coef, txSz, typ, bd, false, res)
					// Added to a mid-level picture of two more bits than the
					// residual's depth, so that nothing clips.
					base := uint16(1) << bd
					for i := range rec[:w*h] {
						rec[i] = base
					}
					reconstructAdd(rec, w, res, txSz, typ, bd+2)
					for i := range rec[:w*h] {
						d := int32(rec[i]) - int32(base) - src[i]
						worst = max(worst, d, -d)
					}
				}
				limit := int32(3)
				if w == 64 || h == 64 {
					limit = 4
				}
				if worst > limit {
					t.Errorf("bit depth %d size %d type %d: roundtrip error up to %d", bd, txSz, typ, worst)
				}
			}
		}
	}
}

func TestTransformLosslessRoundTrip(t *testing.T) {
	var src, coef, res [16]int32
	rs := xorshift32(777)
	for blk := 0; blk < 1000; blk++ {
		for i := range src {
			src[i] = int32(rs.next()%2047) - 1023
		}
		forwardTransform(src[:], TX_4X4, DCT_DCT, 10, true, coef[:])
		inverseTransform(coef[:], TX_4X4, DCT_DCT, 10, true, res[:])
		if res != src {
			t.Fatalf("lossless roundtrip: %v, want %v", res, src)
		}
	}
}

func TestDequantize(t *testing.T) {
	quant := []int32{3, -2, 1 << 22, -(1 << 22), 5}
	out := make([]int32, 1024)
	// TX_4X4 at 8 bits: DC by dcQ, the rest by acQ, magnitudes kept to 24
	// bits, then clamped to 15 bits signed.
	q := append(quant, make([]int32, 11)...)
	dequantize(q, TX_4X4, 10, 7, 8, out)
	want := []int32{30, -14, 32767, -32768, 35}
	for i, v := range want {
		if out[i] != v {
			t.Errorf("TX_4X4 [%d] = %d, want %d", i, out[i], v)
		}
	}
	// TX_64X64 divides by 4 (toward zero), the mask applied first.
	q = make([]int32, 1024)
	q[0], q[1], q[2] = -3, 1<<21, 7
	dequantize(q, TX_64X64, 5, 9, 10, out)
	// 1<<21 * 9 = 0x1200000, masked to 0x200000, /4 = 0x80000, clamped
	// to 2^17-1.
	want = []int32{-3, 1<<17 - 1, 15}
	for i, v := range want {
		if out[i] != v {
			t.Errorf("TX_64X64 [%d] = %d, want %d", i, out[i], v)
		}
	}
}

func BenchmarkInverseTransform(b *testing.B) {
	for _, txSz := range []int{TX_4X4, TX_8X8, TX_16X16, TX_32X32, TX_64X64} {
		w, h := int(txWidth[txSz]), int(txHeight[txSz])
		tw, th := min(w, 32), min(h, 32)
		coef := make([]int32, tw*th)
		res := make([]int32, w*h)
		rs := xorshift32(1)
		genCoeffs(&rs, coef, tw, th, 8, 1)
		b.Run(fmt.Sprintf("%dx%d", w, h), func(b *testing.B) {
			for b.Loop() {
				inverseTransform(coef, txSz, DCT_DCT, 8, false, res)
			}
		})
	}
}

func BenchmarkForwardTransform(b *testing.B) {
	for _, txSz := range []int{TX_4X4, TX_8X8, TX_16X16, TX_32X32, TX_64X64} {
		w, h := int(txWidth[txSz]), int(txHeight[txSz])
		tw, th := min(w, 32), min(h, 32)
		coef := make([]int32, tw*th)
		src := make([]int32, w*h)
		rs := xorshift32(1)
		for i := range src {
			src[i] = int32(rs.next()%511) - 255
		}
		b.Run(fmt.Sprintf("%dx%d", w, h), func(b *testing.B) {
			for b.Loop() {
				forwardTransform(src, txSz, DCT_DCT, 8, false, coef)
			}
		})
	}
}

// forwardTypes, the column passes shared, as forwardTransform for every
// type of every size to 32x32, a type at a time and all at once.
func TestForwardTypes(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	var all []int
	for tt := range TX_TYPES {
		all = append(all, tt)
	}
	for txSz := range TX_SIZES_ALL {
		w, h := int(txWidth[txSz]), int(txHeight[txSz])
		if w > 32 || h > 32 {
			continue
		}
		res := make([]int32, w*h)
		for i := range res {
			res[i] = rng.Int32N(511) - 255
		}
		var types []int
		for _, tt := range all {
			if fwdTypeExists(txSz, tt) {
				types = append(types, tt)
			}
		}
		var got [TX_TYPES][32 * 32]int32
		var cc fwdColCache
		forwardTypes(res, txSz, types, &got, &cc)
		var one [TX_TYPES][32 * 32]int32
		cc.reset()
		for i := range types {
			forwardTypes(res, txSz, types[i:i+1], &one, &cc)
		}
		want := make([]int32, w*h)
		for _, tt := range types {
			forwardTransform(res, txSz, tt, 8, false, want)
			for i := range w * h {
				if got[tt][i] != want[i] || one[tt][i] != want[i] {
					t.Fatalf("size %d type %d at %d: %d and %d, want %d", txSz, tt, i, got[tt][i], one[tt][i], want[i])
				}
			}
		}
	}
}

// fwdTypeExists reports whether there are forward 1D transforms for a
// type at a size.
func fwdTypeExists(txSz, tt int) bool {
	var in, out [64]int32
	return fwd1D(int(txTypeCol[tt]), int(txHeightLog2[txSz]), &in, &out, 12) &&
		fwd1D(int(txTypeRow[tt]), int(txWidthLog2[txSz]), &in, &out, 12)
}

// The AVX2 forward kernels give the scalar transform's coefficients
// exactly, on random residuals and on extreme ones (every sample at the
// bit depth's limit, alternating, a checkerboard), at 8 and 10 bits.
func TestForwardKernels(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 9))
	for txSz := range TX_SIZES_ALL {
		log2W, log2H := int(txWidthLog2[txSz]), int(txHeightLog2[txSz])
		w, h := 1<<log2W, 1<<log2H
		if w < 8 && h < 8 {
			continue
		}
		for tt := range TX_TYPES {
			if !txTypeValid(txSz, tt) {
				continue
			}
			for _, bd := range []int{8, 10} {
				m := int32(1)<<bd - 1
				for trial := range 40 {
					res := make([]int32, w*h)
					for i := range res {
						switch trial {
						case 0:
							res[i] = m
						case 1:
							res[i] = -m
						case 2:
							res[i] = m * int32(1-2*(i&1))
						case 3:
							res[i] = m * int32(1-2*((i/w+i%w)&1))
						case 4:
							res[i] = m * int32(1-2*((i/w)&1))
						default:
							res[i] = rng.Int32N(2*m+1) - m
						}
					}
					got := make([]int32, 64*64)
					forwardTransform(res, txSz, tt, bd, false, got)
					var buf [64 * 64]int32
					want := make([]int32, 64*64)
					fwdColsGo(res, txSz, int(txTypeCol[tt]), txFlipUD[tt], log2W, log2H, buf[:])
					fwdRowsGo(buf[:], txSz, int(txTypeRow[tt]), txFlipLR[tt], log2W, log2H, want)
					n := min(w, 32) * min(h, 32)
					for i := range n {
						if got[i] != want[i] {
							t.Fatalf("size %d type %d bd %d trial %d: coefficient %d is %d, want %d", txSz, tt, bd, trial, i, got[i], want[i])
						}
					}
				}
			}
		}
	}
}
