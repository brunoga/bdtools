package vc1

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// Every code of every table reads back as its value, and no code is the
// prefix of another (a table built wrong would read some other code's).
func TestVLCTables(t *testing.T) {
	initVLCs()
	check := func(name string, tab *vlc, codes []uint32, lens []uint8) {
		t.Helper()
		for i := range codes {
			n := int(lens[i])
			if n == 0 {
				continue
			}
			// The code followed by ones and then zeros: whatever follows,
			// the code alone is read.
			for _, tail := range []uint64{0, ^uint64(0)} {
				v := uint64(codes[i])<<(64-n) | tail>>n
				var b [8]byte
				for k := range b {
					b[k] = byte(v >> (56 - 8*k))
				}
				r := &bits{b: b[:]}
				got, ok := tab.read(r)
				if !ok || got != i || r.pos != n {
					t.Errorf("%s: code %d (%d bits) reads as %d (%v) after %d bits", name, i, n, got, ok, r.pos)
					return
				}
			}
		}
	}
	u32 := func(v []uint8) []uint32 {
		out := make([]uint32, len(v))
		for i, c := range v {
			out[i] = uint32(c)
		}
		return out
	}
	u32s := func(v []int16) []uint32 {
		out := make([]uint32, len(v))
		for i, c := range v {
			out[i] = uint32(c)
		}
		return out
	}
	u32i := func(v []int32) []uint32 {
		out := make([]uint32, len(v))
		for i, c := range v {
			out[i] = uint32(c)
		}
		return out
	}
	check("imode", imodeVLC, u32(imodeCodes[:]), imodeBits[:])
	check("norm6", norm6VLC, u32s(norm6Codes[:]), norm6Bits[:])
	for i := range 4 {
		check("cbpcy", cbpcyPVLC[i], u32s(cbpcyPCodes[i][:]), cbpcyPBits[i][:])
		check("mvdiff", mvDiffVLC[i], u32s(mvDiffCodes[i][:]), mvDiffBits[i][:])
		check("mvdata1ref", mvdata1RefVLC[i], u32i(mvdata1RefCodes[i][:]), mvdata1RefBits[i][:])
		check("intfr4mv", intfr4MVModeVLC[i], u32s(intfr4MVModeCodes[i][:]), intfr4MVModeBits[i][:])
	}
	for i := range 8 {
		n := int(acSizes[i])
		codes, lens := make([]uint32, n), make([]uint8, n)
		for j := range n {
			codes[j], lens[j] = uint32(acTables[i][j][0]), uint8(acTables[i][j][1])
		}
		check("ac", acVLC[i], codes, lens)
		check("mvdata2ref", mvdata2RefVLC[i], u32i(mvdata2RefCodes[i][:]), mvdata2RefBits[i][:])
		check("icbpcy", icbpcyVLC[i], u32s(icbpcyCodes[i][:]), icbpcyBits[i][:])
	}
	for i := range 2 {
		for j := range 2 {
			codes, lens := make([]uint32, 120), make([]uint8, 120)
			for k := range 120 {
				codes[k], lens[k] = uint32(dcTables[i][j][k][0]), uint8(dcTables[i][j][k][1])
			}
			check("dc", dcVLC[i][j], codes, lens)
		}
	}
}

func TestUnescape(t *testing.T) {
	for _, c := range []struct{ in, out []byte }{
		{[]byte{1, 2, 3}, []byte{1, 2, 3}},
		{[]byte{0, 0, 3, 1, 5}, []byte{0, 0, 1, 5}},
		{[]byte{0, 0, 3, 4}, []byte{0, 0, 3, 4}}, // a 3 before a byte above 3 stays
		{[]byte{0, 0, 3, 0, 0, 3, 2}, []byte{0, 0, 0, 0, 2}},
	} {
		if got := unescape(c.in); !bytes.Equal(got, c.out) {
			t.Errorf("unescape(%v) = %v, want %v", c.in, got, c.out)
		}
	}
}

// The DC-only shortcuts add what the full transforms add.
func TestDCShortcuts(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		dc := rng.IntN(4096) - 2048
		base := make([]byte, 8*8)
		for i := range base {
			base[i] = byte(rng.IntN(256))
		}
		var blk [64]int16
		blk[0] = int16(dc)
		full := append([]byte(nil), base...)
		short := append([]byte(nil), base...)
		b := blk
		idct8x8(&b)
		addBlock(full, 8, b[:], 8, 8)
		addDC8x8(short, 8, dc)
		if !bytes.Equal(full, short) {
			t.Fatalf("8x8 DC %d: the shortcut differs", dc)
		}
		full = append(full[:0], base...)
		short = append(short[:0], base...)
		idct8x4(blk[:], full, 8)
		d := (3*dc + 1) >> 1
		addDC(short, 8, 8, 4, (17*d+64)>>7)
		if !bytes.Equal(full, short) {
			t.Fatalf("8x4 DC %d: the shortcut differs", dc)
		}
		full = append(full[:0], base...)
		short = append(short[:0], base...)
		idct4x8(blk[:], full, 8)
		d = (17*dc + 4) >> 3
		addDC(short, 8, 4, 8, (12*d+64)>>7)
		if !bytes.Equal(full, short) {
			t.Fatalf("4x8 DC %d: the shortcut differs", dc)
		}
		full = append(full[:0], base...)
		short = append(short[:0], base...)
		idct4x4(blk[:], full, 8)
		addDC(short, 8, 4, 4, (17*d+64)>>7)
		if !bytes.Equal(full, short) {
			t.Fatalf("4x4 DC %d: the shortcut differs", dc)
		}
	}
}
