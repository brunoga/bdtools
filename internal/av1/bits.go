package av1

// bitWriter writes the headers' fixed-width fields, most significant bit
// first (the spec's f(n)).
type bitWriter struct {
	buf  []byte
	nbit int // bits in the last byte
}

func (w *bitWriter) bit(b bool) {
	if w.nbit == 0 {
		w.buf = append(w.buf, 0)
	}
	if b {
		w.buf[len(w.buf)-1] |= 0x80 >> uint(w.nbit)
	}
	w.nbit = (w.nbit + 1) & 7
}

// f writes v in n bits.
func (w *bitWriter) f(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v>>uint(i)&1 != 0)
	}
}

func (w *bitWriter) flag(b bool) { w.bit(b) }

// su writes a signed value in n bits (two's complement).
func (w *bitWriter) su(v int, n int) {
	w.f(uint32(v)&(1<<uint(n)-1), n) //nolint:gosec // n bits of it
}

// trailing ends an OBU's header bits: a 1, then zeros to the byte.
func (w *bitWriter) trailing() {
	w.bit(true)
	for w.nbit != 0 {
		w.bit(false)
	}
}

// align pads with zeros to the byte.
func (w *bitWriter) align() {
	for w.nbit != 0 {
		w.bit(false)
	}
}

// leb128 appends v as LEB128.
func leb128(b []byte, v uint64) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
			continue
		}
		return append(b, c)
	}
}

// OBU types.
const (
	obuSequenceHeader    = 1
	obuTemporalDelimiter = 2
	obuFrameHeader       = 3
	obuTileGroup         = 4
	obuMetadata          = 5
	obuFrame             = 6
	obuPadding           = 15
)

// appendOBU appends an OBU of a type with its size.
func appendOBU(b []byte, typ int, payload []byte) []byte {
	b = append(b, byte(typ<<3|1<<1)) // obu_has_size_field
	b = leb128(b, uint64(len(payload)))
	return append(b, payload...)
}
