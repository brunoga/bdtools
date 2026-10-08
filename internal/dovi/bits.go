package dovi

import "errors"

var errShort = errors.New("dovi: RPU ends early")

// bitReader reads an RPU's bits, most significant first.
type bitReader struct {
	b   []byte
	pos int // in bits
	err error
}

func (r *bitReader) left() int { return len(r.b)*8 - r.pos }

func (r *bitReader) u(n int) uint64 {
	if r.err != nil {
		return 0
	}
	if n > r.left() {
		r.err = errShort
		return 0
	}
	var v uint64
	for range n {
		v = v<<1 | uint64(r.b[r.pos>>3]>>(7-r.pos&7)&1)
		r.pos++
	}
	return v
}

func (r *bitReader) flag() bool { return r.u(1) != 0 }

func (r *bitReader) ue() uint64 {
	zeros := 0
	for r.u(1) == 0 {
		if r.err != nil || zeros > 32 {
			r.err = errShort
			return 0
		}
		zeros++
	}
	return 1<<zeros - 1 + r.u(zeros)
}

func (r *bitReader) se() int64 {
	v := r.ue()
	if v&1 != 0 {
		return int64(v+1) / 2 //nolint:gosec // at most 33 bits
	}
	return -int64(v / 2) //nolint:gosec // at most 33 bits
}

func (r *bitReader) aligned() bool { return r.pos&7 == 0 }

func (r *bitReader) align() {
	for !r.aligned() && r.err == nil {
		r.u(1)
	}
}

// bitWriter writes bits, most significant first.
type bitWriter struct {
	b []byte
	n int // bits written
}

func (w *bitWriter) u(n int, v uint64) {
	for i := n - 1; i >= 0; i-- {
		if w.n&7 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>i&1 != 0 {
			w.b[len(w.b)-1] |= 1 << (7 - w.n&7)
		}
		w.n++
	}
}

func (w *bitWriter) flag(f bool) {
	if f {
		w.u(1, 1)
	} else {
		w.u(1, 0)
	}
}

func (w *bitWriter) ue(v uint64) {
	v++
	n := 0
	for x := v; x > 1; x >>= 1 {
		n++
	}
	w.u(n, 0)
	w.u(n+1, v)
}

func (w *bitWriter) se(v int64) {
	if v > 0 {
		w.ue(uint64(2*v - 1)) //nolint:gosec // positive
	} else {
		w.ue(uint64(-2 * v)) //nolint:gosec // not negative
	}
}

func (w *bitWriter) aligned() bool { return w.n&7 == 0 }

func (w *bitWriter) align() {
	for !w.aligned() {
		w.u(1, 0)
	}
}
