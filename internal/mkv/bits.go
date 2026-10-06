package mkv

import "bytes"

// rbsp removes emulation prevention bytes (00 00 03 -> 00 00).
func rbsp(b []byte) []byte {
	if !bytes.Contains(b, []byte{0, 0, 3}) {
		return b
	}
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

type bitReader struct {
	b   []byte
	pos int
	bad bool
}

func (r *bitReader) u(n int) uint32 {
	var v uint32
	for ; n > 0; n-- {
		if r.pos>>3 >= len(r.b) {
			r.bad = true
			return v << n
		}
		v = v<<1 | uint32(r.b[r.pos>>3]>>(7-r.pos&7)&1)
		r.pos++
	}
	return v
}

func (r *bitReader) flag() bool { return r.u(1) == 1 }

func (r *bitReader) ue() uint32 {
	n := 0
	for r.u(1) == 0 {
		if r.bad || n > 31 {
			r.bad = true
			return 0
		}
		n++
	}
	return 1<<n - 1 + r.u(n)
}

func (r *bitReader) se() int32 {
	v := r.ue()
	if v&1 == 1 {
		return int32((v + 1) / 2)
	}
	return -int32(v / 2)
}

// splitNALs returns the NAL units of an Annex B buffer, without start codes.
func splitNALs(b []byte) [][]byte {
	nals, _ := splitNALsAt(b)
	return nals
}

// splitNALsAt is splitNALs that also gives where each NAL unit's start code
// begins in b.
func splitNALsAt(b []byte) (nals [][]byte, at []int) {
	start, code := -1, -1
	for i := 0; i+2 < len(b); {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				end := i
				for end > start && b[end-1] == 0 {
					end--
				}
				nals, at = append(nals, b[start:end]), append(at, code)
			}
			code = i
			i += 3
			start = i
			continue
		}
		i++
	}
	if start >= 0 && start < len(b) {
		nals, at = append(nals, b[start:]), append(at, code)
	}
	return nals, at
}
