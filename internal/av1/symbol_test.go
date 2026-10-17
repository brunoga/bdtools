package av1

import (
	"math/bits"
	"math/rand/v2"
	"testing"
)

// symbolReader is the spec's symbol decoder (8.2.2-8.2.6), to check the
// encoder against.
type symbolReader struct {
	data    []byte
	pos     int // bit position
	value   uint32
	rng     uint32
	maxBits int
	adapt   bool
}

func (r *symbolReader) bit() uint32 {
	if r.pos >= 8*len(r.data) {
		r.pos++
		return 0
	}
	b := r.data[r.pos>>3] >> (7 - r.pos&7) & 1
	r.pos++
	return uint32(b)
}

func (r *symbolReader) f(n int) uint32 {
	var v uint32
	for range n {
		v = v<<1 | r.bit()
	}
	return v
}

func newSymbolReader(data []byte, adapt bool) *symbolReader {
	r := &symbolReader{data: data, adapt: adapt}
	sz := len(data)
	numBits := min(sz*8, 15)
	buf := r.f(numBits)
	padded := buf << uint(15-numBits)
	r.value = (1<<15 - 1) ^ padded
	r.rng = 1 << 15
	r.maxBits = 8*sz - 15
	return r
}

func (r *symbolReader) symbol(cdf []uint16) int {
	n := len(cdf) - 1
	cur := r.rng
	var prev uint32
	s := -1
	for {
		s++
		prev = cur
		f := uint32(1<<15) - uint32(cdf[s])
		cur = ((r.rng >> 8) * (f >> ecProbShift)) >> (7 - ecProbShift)
		cur += ecMinProb * uint32(n-s-1)
		if r.value >= cur {
			break
		}
	}
	r.rng = prev - cur
	r.value -= cur
	b := 15 - (bits.Len32(r.rng) - 1)
	r.rng <<= uint(b)
	numBits := min(b, max(0, r.maxBits))
	newData := r.f(numBits)
	padded := newData << uint(b-numBits)
	r.value = padded ^ (((r.value + 1) << uint(b)) - 1)
	r.maxBits -= b
	if r.adapt {
		adaptCDF(cdf, s)
	}
	return s
}

func (r *symbolReader) boolean() bool {
	cdf := []uint16{1 << 14, 1 << 15, 0}
	return r.symbol(cdf) == 1
}

// exitOK checks the spec's exit process: a 1 at the trailing bit position,
// zeros after it to the end.
func (r *symbolReader) exitOK() bool {
	if r.maxBits < -14 {
		return false
	}
	trailing := r.pos - min(15, r.maxBits+15)
	end := 8 * len(r.data)
	if trailing < 0 || trailing >= end || r.data[trailing>>3]>>(7-trailing&7)&1 != 1 {
		return false
	}
	for p := trailing + 1; p < end; p++ {
		if r.data[p>>3]>>(7-p&7)&1 != 0 {
			return false
		}
	}
	return true
}

// randomCDF is an n-valued CDF, its count too.
func randomCDF(rng *rand.Rand, n int) []uint16 {
	cdf := make([]uint16, n+1)
	cuts := make([]int, n-1)
	for i := range cuts {
		cuts[i] = 1 + rng.IntN(32767)
	}
	for i := range cuts {
		for j := i + 1; j < len(cuts); j++ {
			if cuts[j] < cuts[i] {
				cuts[i], cuts[j] = cuts[j], cuts[i]
			}
		}
	}
	for i, c := range cuts {
		cdf[i] = uint16(c)
	}
	cdf[n-1] = 32768
	return cdf
}

// Symbols, booleans and literals coded, then decoded by the spec's
// decoder, adapting alike; the coded data ends as the spec's exit process
// requires.
func TestSymbolRoundTrip(t *testing.T) {
	for _, adapt := range []bool{false, true} {
		for trial := range 200 {
			rng := rand.New(rand.NewPCG(uint64(trial), 7))
			type op struct {
				kind, n, v int
				cdf        int
			}
			var cdfs [][]uint16
			for range 8 {
				cdfs = append(cdfs, randomCDF(rng, 2+rng.IntN(15)))
			}
			enc := make([][]uint16, len(cdfs))
			dec := make([][]uint16, len(cdfs))
			for i, c := range cdfs {
				enc[i] = append([]uint16(nil), c...)
				dec[i] = append([]uint16(nil), c...)
			}
			var ops []op
			w := newSymbolWriter(adapt)
			for range rng.IntN(3000) {
				switch rng.IntN(4) {
				case 0:
					v := rng.IntN(2)
					w.boolean(v == 1)
					ops = append(ops, op{kind: 1, v: v})
				case 1:
					nb := 1 + rng.IntN(16)
					v := rng.IntN(1 << nb)
					w.literal(uint32(v), nb)
					ops = append(ops, op{kind: 2, n: nb, v: v})
				default:
					k := rng.IntN(len(enc))
					n := len(enc[k]) - 1
					// skewed: mostly the likely symbols
					v := rng.IntN(n)
					if rng.IntN(3) > 0 {
						v = rng.IntN(min(2, n))
					}
					w.symbol(v, enc[k])
					ops = append(ops, op{kind: 0, v: v, cdf: k})
				}
			}
			data := w.finish()
			r := newSymbolReader(data, adapt)
			for i, o := range ops {
				var got int
				switch o.kind {
				case 1:
					got = int(b2u(r.boolean()))
				case 2:
					got = 0
					for range o.n {
						got = got<<1 | int(b2u(r.boolean()))
					}
				default:
					got = r.symbol(dec[o.cdf])
				}
				if got != o.v {
					t.Fatalf("adapt %v trial %d: op %d of %d (%+v) decoded %d", adapt, trial, i, len(ops), o, got)
				}
			}
			if !r.exitOK() {
				t.Fatalf("adapt %v trial %d: %d ops, %d bytes: the trailing bits are not as the exit process wants", adapt, trial, len(ops), len(data))
			}
		}
	}
}
