package ac3

// bitWriter writes bits most significant first, counting them.
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

func (w *bitWriter) flag(b bool) { w.u(1, uint32(boolInt(b))) }

// flush pads to a byte with zeros.
func (w *bitWriter) flush() {
	for w.nbit != 0 {
		w.u(1, 0)
	}
}

// writeHeader writes syncinfo and bsi, the CRC1 left zero.
func (e *Encoder) writeHeader(w *bitWriter) {
	w.u(16, 0x0b77)
	w.u(16, 0)                  // crc1
	w.u(2, 0)                   // fscod: 48 kHz
	w.u(6, uint32(2*e.rateIdx)) //nolint:gosec // frmsizecod
	w.u(5, 8)                   // bsid
	w.u(3, 0)                   // bsmod: complete main
	w.u(3, uint32(e.acmod))     //nolint:gosec // acmod
	if e.acmod&1 != 0 && e.acmod != 1 {
		w.u(2, 0) // cmixlev: -3 dB
	}
	if e.acmod&4 != 0 {
		w.u(2, 0) // surmixlev: -3 dB
	}
	if e.acmod == 2 {
		w.u(2, 0) // dsurmod: not indicated
	}
	w.flag(e.lfe)
	w.u(5, 31)    // dialnorm: -31 dB, no change
	w.flag(false) // compre
	w.flag(false) // langcode
	w.flag(false) // audprodie
	w.flag(false) // copyrightb
	w.flag(true)  // origbs
	w.flag(false) // timecod1e
	w.flag(false) // timecod2e
	w.flag(false) // addbsie
}

// writeBlockSide writes block b's side information and exponents (the
// bit allocation at snr).
func (e *Encoder) writeBlockSide(w *bitWriter, blks *[6][6]block, b int, snr [6]int) {
	nch := e.fbw + boolInt(e.lfe)
	for range e.fbw {
		w.flag(false) // blksw: long blocks
	}
	for range e.fbw {
		w.flag(true) // dithflag
	}
	w.flag(false) // dynrnge
	if e.acmod == 0 {
		w.flag(false) // dynrng2e
	}
	w.flag(b == 0) // cplstre
	if b == 0 {
		w.flag(false) // cplinu
	}
	if e.acmod == 2 {
		w.flag(true) // rematstr
		for _, f := range e.remat[b] {
			w.flag(f) // rematflg
		}
	}
	for ch := range e.fbw {
		w.u(2, uint32(blks[b][ch].expStrat)) //nolint:gosec // 0..3
	}
	if e.lfe {
		w.u(1, uint32(blks[b][e.fbw].expStrat)) //nolint:gosec // 0 or 1
	}
	for ch := range e.fbw {
		if blks[b][ch].expStrat != expReuse {
			w.u(6, uint32(e.bw)) //nolint:gosec // chbwcod
		}
	}
	for ch := range nch {
		bl := &blks[b][ch]
		if bl.expStrat == expReuse {
			continue
		}
		n := e.coded(ch)
		u := unitsOf(bl.exp[:n], bl.expStrat)
		w.u(4, uint32(u[0])) //nolint:gosec // 0..15
		for g := 0; g+3 < len(u); g += 3 {
			d0, d1, d2 := u[g+1]-u[g]+2, u[g+2]-u[g+1]+2, u[g+3]-u[g+2]+2
			w.u(7, uint32(25*d0+5*d1+d2)) //nolint:gosec // 0..124
		}
		if ch < e.fbw {
			w.u(2, 0) // gainrng
		}
	}
	w.flag(b == 0) // baie
	if b == 0 {
		w.u(2, slowDecayCode)
		w.u(2, fastDecayCode)
		w.u(2, slowGainCode)
		w.u(2, dbPerBitCode)
		w.u(3, floorCode)
	}
	w.flag(b == 0) // snroffste
	if b == 0 {
		w.u(6, uint32(snr[0]>>4)) //nolint:gosec // csnroffst
		for ch := range nch {
			w.u(4, uint32(snr[ch]&15)) //nolint:gosec // fsnroffst
			w.u(3, fastGainCode)
		}
	}
	w.flag(false) // deltbaie
	w.flag(false) // skiple
}

// quantized is a mantissa's code at its bap: a level index for the
// symmetric quantizers, a two's complement value for the others.
func quantize(m float64, bap uint8) int {
	switch bap {
	case 1, 2, 3, 4, 5:
		levels := [6]int{0, 3, 5, 7, 11, 15}[bap]
		q := int((m + 1) * float64(levels) / 2)
		return min(max(q, 0), levels-1)
	}
	bits := uint(bap) - 1
	switch bap {
	case 14:
		bits = 14
	case 15:
		bits = 16
	}
	lim := 1 << (bits - 1)
	q := int(m*float64(lim) + 0.5*sign(m))
	return min(max(q, -lim), lim-1)
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// write writes the frame at snr: header, blocks, padding and CRCs.
func (e *Encoder) write(blks *[6][6]block, snr [6]int) []byte {
	w := &bitWriter{b: make([]byte, 0, e.size)}
	e.writeHeader(w)
	nch := e.fbw + boolInt(e.lfe)
	for b := range 6 {
		e.writeBlockSide(w, blks, b, snr)
		// The mantissas, grouped: a group's code goes where its first
		// mantissa is, built from it and the next ones of its bap.
		type slot struct {
			bap  uint8
			code int
		}
		var slots []slot
		for ch := range nch {
			bl := &blks[b][ch]
			for k := range e.coded(ch) {
				if bl.bap[k] == 0 {
					continue
				}
				m := bl.coef[k] * float64(int(1)<<bl.exp[k])
				slots = append(slots, slot{bl.bap[k], quantize(m, bl.bap[k])})
			}
		}
		var open [5]int // the slot of each bap's group being filled, and its count
		var count [5]int
		size := [5]int{0, 3, 3, 0, 2}
		mul := [5][3]int{{}, {9, 3, 1}, {25, 5, 1}, {}, {11, 1}}
		codes := make([]int, len(slots))
		first := make([]bool, len(slots))
		for i, s := range slots {
			switch s.bap {
			case 1, 2, 4:
				g := s.bap
				if count[g] == 0 {
					open[g] = i
					first[i] = true
				}
				codes[open[g]] += mul[g][count[g]] * s.code
				count[g] = (count[g] + 1) % size[g]
			default:
				codes[i], first[i] = s.code, true
			}
		}
		for i, s := range slots {
			if !first[i] {
				continue
			}
			switch s.bap {
			case 1:
				w.u(5, uint32(codes[i])) //nolint:gosec // a group code
			case 2, 4:
				w.u(7, uint32(codes[i])) //nolint:gosec // a group code
			case 3:
				w.u(3, uint32(codes[i])) //nolint:gosec // 0..6
			case 5:
				w.u(4, uint32(codes[i])) //nolint:gosec // 0..14
			case 14:
				w.u(14, uint32(codes[i])) //nolint:gosec // two's complement
			case 15:
				w.u(16, uint32(codes[i])) //nolint:gosec // two's complement
			default:
				w.u(uint(s.bap)-1, uint32(codes[i])) //nolint:gosec // two's complement
			}
		}
	}
	w.flush()
	frame := append(w.b, make([]byte, e.size-len(w.b))...)
	// CRC1 over the first 5/8 (after the sync word) comes to zero; CRC2
	// over the rest.
	fs58 := ((e.size >> 2) + (e.size >> 4)) << 1
	c := crc16(frame[4:fs58])
	c = mulPoly(c, powPoly(0xc002, 8*fs58-16)) // times x^-(8*fs58-16): the CRC of the whole is zero
	frame[2], frame[3] = byte(c>>8), byte(c)
	c2 := crc16(frame[fs58 : e.size-2])
	if c2 == 0x0b77 {
		frame[e.size-3] ^= 1 // crcrsv: keeps the sync word out of the CRC
		c2 ^= 0x8005
	}
	frame[e.size-2], frame[e.size-1] = byte(c2>>8), byte(c2)
	return frame
}

// crc16 is A/52's CRC: x^16 + x^15 + x^2 + 1, most significant bit first,
// from zero.
func crc16(b []byte) uint32 {
	var c uint32
	for _, v := range b {
		c ^= uint32(v) << 8
		for range 8 {
			c <<= 1
			if c&0x10000 != 0 {
				c ^= 0x18005
			}
		}
	}
	return c & 0xffff
}

// mulPoly multiplies two polynomials over GF(2) modulo the CRC's.
func mulPoly(a, b uint32) uint32 {
	var c uint32
	for a != 0 {
		if a&1 != 0 {
			c ^= b
		}
		a >>= 1
		b <<= 1
		if b&0x10000 != 0 {
			b ^= 0x18005
		}
	}
	return c
}

// powPoly raises a to n modulo the CRC's polynomial.
func powPoly(a uint32, n int) uint32 {
	r := uint32(1)
	for n > 0 {
		if n&1 != 0 {
			r = mulPoly(r, a)
		}
		a = mulPoly(a, a)
		n >>= 1
	}
	return r
}
