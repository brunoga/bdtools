package aac

// bitWriter writes bits most significant first.
type bitWriter struct {
	b    []byte
	cur  uint64
	nbit uint
	n    int
}

func (w *bitWriter) u(n uint, v uint32) {
	w.n += int(n)
	w.cur = w.cur<<n | uint64(v)&(1<<n-1)
	w.nbit += n
	for w.nbit >= 8 {
		w.nbit -= 8
		w.b = append(w.b, byte(w.cur>>w.nbit))
	}
}

func (w *bitWriter) align() {
	for w.nbit != 0 {
		w.u(1, 0)
	}
}

// Code book geometry: values a book codes (unsigned books code magnitudes
// and add sign bits), tuple size, and the range of an index digit.
var (
	bookMax      = [12]int{0, 1, 1, 2, 2, 4, 4, 7, 7, 12, 12, 16}
	bookDim      = [12]int{0, 4, 4, 4, 4, 2, 2, 2, 2, 2, 2, 2}
	bookUnsigned = [12]bool{false, false, false, true, true, false, false, true, true, true, true, true}
)

// tupleIndex is a tuple's index in book cb.
func tupleIndex(q []int, cb int) int {
	rng := bookMax[cb]*2 + 1
	off := bookMax[cb]
	if bookUnsigned[cb] {
		rng, off = bookMax[cb]+1, 0
	}
	idx := 0
	for _, v := range q {
		if bookUnsigned[cb] {
			v = min(abs(v), bookMax[cb])
		}
		idx = idx*rng + v + off
	}
	return idx
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// escapeBits is the escape sequence's length for a magnitude of 16 or more.
func escapeBits(v int) int {
	n := 0
	for 1<<(n+1) <= v {
		n++
	}
	return 2*n - 3 // n-4 ones, a zero, then n bits
}

// bandBits is what coding q with book cb costs, -1 if the book cannot.
func bandBits(q []int, cb int) int {
	if cb == 0 {
		for _, v := range q {
			if v != 0 {
				return -1
			}
		}
		return 0
	}
	for _, v := range q {
		if abs(v) > bookMax[cb] && cb != 11 {
			return -1
		}
	}
	bits := 0
	dim := bookDim[cb]
	for i := 0; i < len(q); i += dim {
		t := q[i : i+dim]
		bits += int(spectralBits[cb-1][tupleIndex(t, cb)])
		if bookUnsigned[cb] {
			for _, v := range t {
				if v != 0 {
					bits++
				}
				if cb == 11 && abs(v) >= 16 {
					bits += escapeBits(abs(v))
				}
			}
		}
	}
	return bits
}

// writeBand writes q with book cb.
func writeBand(w *bitWriter, q []int, cb int) {
	dim := bookDim[cb]
	for i := 0; i < len(q); i += dim {
		t := q[i : i+dim]
		idx := tupleIndex(t, cb)
		w.u(uint(spectralBits[cb-1][idx]), uint32(spectralCodes[cb-1][idx]))
		if !bookUnsigned[cb] {
			continue
		}
		for _, v := range t {
			if v != 0 {
				w.u(1, uint32(boolInt(v < 0)))
			}
		}
		if cb == 11 {
			for _, v := range t {
				a := abs(v)
				if a < 16 {
					continue
				}
				n := 0
				for 1<<(n+1) <= a {
					n++
				}
				w.u(uint(n-3), uint32(1<<(n-3)-2)) // n-4 ones and a zero
				w.u(uint(n), uint32(a))            // the low n bits
			}
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// section chooses each band's code book, minimising the spectral bits and
// the sections' (a new section costs 9 bits); bands coded zero go to book
// 0 or join a neighbour's section.
func (s *spectrum) section(nsfb int) {
	const inf = 1 << 30
	// cost[b][cb]: the bits of band b in book cb (and its scale factor,
	// about 2 bits, when the book sends one).
	cost := make([][12]int, nsfb)
	for b := range nsfb {
		q := s.quant[swbOffset1024_48[b]:swbOffset1024_48[b+1]]
		peak := 0
		for _, v := range q {
			peak = max(peak, abs(v))
		}
		// The books that hold the band's values: the smallest two levels
		// that do (a larger book may still join a neighbour's section),
		// and the escape book.
		levels := 0
		for cb := range 12 {
			c := inf
			switch {
			case cb == 0:
				if peak == 0 {
					c = 0
				}
			case bookMax[cb] >= peak && levels < 4 || cb == 11:
				c = bandBits(q, cb)
				if cb != 11 {
					levels++
				}
			}
			if c < 0 {
				c = inf
			} else if cb != 0 && c < inf {
				c += 2
			}
			cost[b][cb] = c
		}
	}
	// best[b][cb]: the least bits for bands 0..b with band b in book cb.
	best := make([][12]int, nsfb)
	from := make([][12]int, nsfb)
	for b := range nsfb {
		for cb := range 12 {
			if cost[b][cb] >= inf {
				best[b][cb] = inf
				continue
			}
			if b == 0 {
				best[b][cb] = cost[b][cb] + 9
				continue
			}
			bv, bp := inf, 0
			for p := range 12 {
				v := best[b-1][p]
				if v >= inf {
					continue
				}
				if p != cb {
					v += 9
				}
				if v < bv {
					bv, bp = v, p
				}
			}
			best[b][cb] = bv + cost[b][cb]
			from[b][cb] = bp
		}
	}
	cb := 0
	for c := range 12 {
		if best[nsfb-1][c] < best[nsfb-1][cb] {
			cb = c
		}
	}
	for b := nsfb - 1; b >= 0; b-- {
		s.book[b] = cb
		cb = from[b][cb]
	}
}

// codedBands is the bands up to the last one coded.
func (s *spectrum) codedBands() int {
	n := s.nsfb
	for n > 0 && s.book[n-1] == 0 {
		n--
	}
	return n
}

// writeICS writes a channel's individual_channel_stream (ics_info first
// unless common), its bands up to maxSfb.
func (e *Encoder) writeICS(w *bitWriter, s *spectrum, maxSfb int, common bool) {
	// The scale factors of the bands sent: a zero band joined to a section
	// takes the one before it.
	global := -1
	last := 100
	for b := range maxSfb {
		if s.book[b] == 0 {
			continue
		}
		if !hasValues(s, b) {
			s.sf[b] = last
		}
		if global < 0 {
			global = s.sf[b]
		}
		last = s.sf[b]
	}
	if global < 0 {
		global = 100
	}
	w.u(8, uint32(global)) //nolint:gosec // 0..255
	if !common {
		writeICSInfo(w, maxSfb)
	}
	// section_data
	for b := 0; b < maxSfb; {
		cb := s.book[b]
		n := 1
		for b+n < maxSfb && s.book[b+n] == cb {
			n++
		}
		w.u(4, uint32(cb)) //nolint:gosec // 0..11
		for l := n; ; l -= 31 {
			if l < 31 {
				w.u(5, uint32(l)) //nolint:gosec // 0..30
				break
			}
			w.u(5, 31)
		}
		b += n
	}
	// scale_factor_data
	prev := global
	for b := range maxSfb {
		if s.book[b] == 0 {
			continue
		}
		d := s.sf[b] - prev
		d = min(max(d, -60), 60)
		s.sf[b] = prev + d
		w.u(uint(scalefactorBits[d+60]), scalefactorCode[d+60])
		prev = s.sf[b]
	}
	w.u(1, 0) // pulse_data_present
	w.u(1, 0) // tns_data_present
	w.u(1, 0) // gain_control_data_present
	for b := range maxSfb {
		if cb := s.book[b]; cb != 0 {
			writeBand(w, s.quant[swbOffset1024_48[b]:swbOffset1024_48[b+1]], cb)
		}
	}
}

func hasValues(s *spectrum, b int) bool {
	for _, v := range s.quant[swbOffset1024_48[b]:swbOffset1024_48[b+1]] {
		if v != 0 {
			return true
		}
	}
	return false
}

// writeICSInfo writes ics_info for a long window.
func writeICSInfo(w *bitWriter, maxSfb int) {
	w.u(1, 0)              // ics_reserved_bit
	w.u(2, 0)              // window_sequence: ONLY_LONG_SEQUENCE
	w.u(1, 0)              // window_shape: sine
	w.u(6, uint32(maxSfb)) //nolint:gosec // 0..49
	w.u(1, 0)              // predictor_data_present
}

// write writes the frame: an ADTS header and its raw data block.
func (e *Encoder) write(specs []*spectrum, ms [][49]bool) []byte {
	for _, s := range specs {
		s.section(s.nsfb)
	}
	w := &bitWriter{b: make([]byte, 7, 1024)}
	for i, el := range e.elements {
		w.u(3, uint32(el.typ)) //nolint:gosec // an element id
		w.u(4, uint32(i))      //nolint:gosec // element_instance_tag
		switch el.typ {
		case idCPE:
			l, r := specs[el.ch[0]], specs[el.ch[1]]
			maxSfb := max(l.codedBands(), r.codedBands())
			w.u(1, 1) // common_window
			writeICSInfo(w, maxSfb)
			any := false
			for b := range maxSfb {
				any = any || ms[i][b]
			}
			if any {
				w.u(2, 1) // ms_mask_present: by band
				for b := range maxSfb {
					w.u(1, uint32(boolInt(ms[i][b])))
				}
			} else {
				w.u(2, 0)
			}
			e.writeICS(w, l, maxSfb, true)
			e.writeICS(w, r, maxSfb, true)
		default:
			s := specs[el.ch[0]]
			e.writeICS(w, s, s.codedBands(), false)
		}
	}
	w.u(3, idEND)
	w.align()
	frame := w.b
	n := len(frame)
	cfg := e.Config()
	object := int(cfg[0] >> 3)
	chans := int(cfg[1]>>3) & 15
	copy(frame, []byte{
		0xff, 0xf1,
		byte((object-1)<<6 | e.rateIndex<<2 | chans>>2),
		byte(chans&3<<6 | n>>11),
		byte(n >> 3),
		byte(n&7<<5 | 0x1f),
		0xfc,
	})
	return frame
}
