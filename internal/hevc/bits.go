// Package hevc decodes H.265/HEVC Main and Main 10 video in Go (4:2:0, 8
// and 10 bits, every tool of those profiles): a fallback where no GPU
// decodes an Ultra HD Blu-ray's HEVC.
package hevc

import "errors"

var (
	errStream = errors.New("hevc: an invalid stream")
	errShort  = errors.New("hevc: the stream ends inside a syntax element")
)

type unsupported string

func (u unsupported) Error() string { return "hevc: " + string(u) + " is not supported" }

// IsUnsupported reports whether err is the decoder declining a stream it
// does not decode (the format range extensions, for one).
func IsUnsupported(err error) bool {
	var u unsupported
	return errors.As(err, &u)
}

// bits reads an RBSP's bits, most significant first.
type bits struct {
	b   []byte
	pos int
}

func (r *bits) left() int { return len(r.b)*8 - r.pos }

func (r *bits) peek(n int) uint32 {
	byteAt := r.pos >> 3
	var v uint64
	if byteAt+8 <= len(r.b) {
		b := r.b[byteAt : byteAt+8]
		v = uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
			uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
	} else {
		for i := range 8 {
			v <<= 8
			if byteAt+i < len(r.b) {
				v |= uint64(r.b[byteAt+i])
			}
		}
	}
	v <<= uint(r.pos & 7)
	return uint32(v >> (64 - n))
}

func (r *bits) skip(n int) { r.pos += n }

func (r *bits) u(n int) int {
	if n == 0 {
		return 0
	}
	if n > 32 {
		hi := r.u(n - 32)
		return hi<<32 | r.u(32)
	}
	v := r.peek(n)
	r.pos += n
	return int(v)
}

func (r *bits) flag() bool { return r.u(1) != 0 }

// ue reads an unsigned Exp-Golomb code.
func (r *bits) ue() int {
	zeros := 0
	for r.u(1) == 0 {
		zeros++
		if zeros > 31 {
			r.pos = len(r.b)*8 + 1 // past the end: the caller sees an error
			return 0
		}
	}
	return 1<<zeros - 1 + r.u(zeros)
}

// se reads a signed Exp-Golomb code.
func (r *bits) se() int {
	v := r.ue()
	if v&1 != 0 {
		return (v + 1) >> 1
	}
	return -(v >> 1)
}

// byteAlign moves to the next byte boundary.
func (r *bits) byteAlign() { r.pos = (r.pos + 7) &^ 7 }

// nalUnit is a NAL unit: its header's fields and its RBSP.
type nalUnit struct {
	typ        int
	layer      int
	temporalID int
	rbsp       []byte
	// ep are the payload offsets (after the header) of the emulation
	// prevention bytes removed: entry points count them.
	ep []int
}

// unescaped converts a payload offset counting emulation prevention bytes
// into an RBSP one.
func (n *nalUnit) unescaped(raw int) int {
	k := 0
	for k < len(n.ep) && n.ep[k] < raw {
		k++
	}
	return raw - k
}

// escaped converts an RBSP offset into a payload one.
func (n *nalUnit) escaped(off int) int {
	raw := off
	for _, e := range n.ep {
		if e <= raw {
			raw++
		}
	}
	return raw
}

// NAL unit types.
const (
	nalTrailN     = 0
	nalRASLR      = 9
	nalBLAWLP     = 16
	nalBLAWRADL   = 17
	nalBLANLP     = 18
	nalIDRWRADL   = 19
	nalIDRNLP     = 20
	nalCRA        = 21
	nalRsvIRAP23  = 23
	nalVPS        = 32
	nalSPS        = 33
	nalPPS        = 34
	nalAUD        = 35
	nalEOS        = 36
	nalEOB        = 37
	nalFD         = 38
	nalSEIPrefix  = 39
	nalSEISuffix  = 40
	nalRASLN      = 8
	nalRADLN      = 6
	nalRADLR      = 7
	nalTSAN       = 2
	nalSTSAN      = 4
	nalSubLayerNR = 14 // the last sub-layer non-reference type
)

// nalUnits splits an Annex B access unit into its NAL units, unescaping
// each.
func nalUnits(au []byte) []nalUnit {
	var out []nalUnit
	start := -1
	for i := 0; i+2 < len(au); {
		if au[i] == 0 && au[i+1] == 0 && au[i+2] == 1 {
			if start >= 0 {
				out = appendNAL(out, au[start:trimZeros(au, start, i)])
			}
			start = i + 3
			i += 3
			continue
		}
		i++
	}
	if start >= 0 && start < len(au) {
		out = appendNAL(out, au[start:])
	}
	return out
}

// trimZeros drops the zeros before a start code (a four-byte start code's
// first, trailing_zero_8bits).
func trimZeros(b []byte, start, end int) int {
	for end > start && b[end-1] == 0 {
		end--
	}
	return end
}

func appendNAL(out []nalUnit, b []byte) []nalUnit {
	if len(b) < 2 {
		return out
	}
	n := nalUnit{typ: int(b[0]>>1) & 0x3f, layer: int(b[0]&1)<<5 | int(b[1]>>3), temporalID: int(b[1]&7) - 1}
	n.rbsp, n.ep = unescapeEP(b[2:])
	return append(out, n)
}

// unescapeEP removes emulation prevention bytes (a 3 after two zeros),
// giving where they were.
func unescapeEP(b []byte) ([]byte, []int) {
	var out []byte
	var ep []int
	zeros := 0
	for i := 0; i < len(b); i++ {
		c := b[i]
		if zeros >= 2 && c == 3 {
			if out == nil {
				out = append(make([]byte, 0, len(b)), b[:i]...)
			}
			ep = append(ep, i)
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		if out != nil {
			out = append(out, c)
		}
	}
	if out == nil {
		return b, nil
	}
	return out, ep
}
