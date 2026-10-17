package av1

import "testing"

func TestDbgIsolate(t *testing.T) {
	type sw struct {
		name string
		v    *bool
	}
	all := []sw{{"dct", &dbgDCTOnly}, {"angles", &dbgNoAngles}, {"depth", &dbgNoDepth}, {"square", &dbgSquare}}
	for mask := 0; mask < 16; mask++ {
		name := ""
		for i, s := range all {
			*s.v = mask>>i&1 == 1
			if *s.v {
				name += s.name + "-off "
			}
		}
		t.Run(name, func(t *testing.T) {
			o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 60, FPSNum: 25, FPSDen: 1}
			checkDecodes(t, o, []*Source{testPicture(200, 136, 8, 1)})
		})
	}
	for _, s := range all {
		*s.v = false
	}
}

func TestDbgFixed(t *testing.T) {
	dbgDCTOnly, dbgNoAngles, dbgNoDepth = true, true, true
	defer func() { dbgDCTOnly, dbgNoAngles, dbgNoDepth, dbgFixed = false, false, false, 0 }()
	for _, s := range []int{BLOCK_8X8, BLOCK_16X16, BLOCK_32X32, BLOCK_64X64} {
		dbgFixed = s
		t.Run(string(rune('0'+s)), func(t *testing.T) {
			o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 60, FPSNum: 25, FPSDen: 1}
			checkDecodes(t, o, []*Source{testPicture(200, 136, 8, 1)})
		})
	}
}

func TestDbgCands(t *testing.T) {
	dbgDCTOnly, dbgNoAngles, dbgNoDepth, dbgFixed = true, true, true, BLOCK_16X16
	defer func() {
		dbgDCTOnly, dbgNoAngles, dbgNoDepth, dbgFixed, lumaCands, chromaCands = false, false, false, 0, 3, 2
	}()
	for _, c := range [][2]int{{1, 1}, {3, 1}, {1, 2}} {
		lumaCands, chromaCands = c[0], c[1]
		t.Run(string(rune('0'+c[0]))+string(rune('0'+c[1])), func(t *testing.T) {
			o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 60, FPSNum: 25, FPSDen: 1}
			checkDecodes(t, o, []*Source{testPicture(200, 136, 8, 1)})
		})
	}
}

func TestDbgCompare(t *testing.T) {
	dbgDCTOnly, dbgNoAngles, dbgNoDepth, dbgFixed, dbgCompare = true, true, true, BLOCK_16X16, true
	lumaCands, chromaCands = 1, 1
	defer func() {
		dbgDCTOnly, dbgNoAngles, dbgNoDepth, dbgFixed, lumaCands, chromaCands, dbgCompare = false, false, false, 0, 3, 2, false
	}()
	o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 60, FPSNum: 25, FPSDen: 1}
	checkDecodes(t, o, []*Source{testPicture(200, 136, 8, 1)})
}

// Every mode, size and availability predicted twice with different
// garbage in the edge buffers: the predictions must not differ.
func TestDbgEdgeReads(t *testing.T) {
	rec := make([]uint16, 256*256)
	for i := range rec {
		rec[i] = uint16(i*7919) & 255
	}
	var e1, e2 intraEdges
	for i := range e1.above {
		e1.above[i], e1.left[i] = 1000, 1000
		e2.above[i], e2.left[i] = 5000, 5000
	}
	p1, p2 := make([]uint16, 64*64), make([]uint16, 64*64)
	for txSz := range TX_SIZES_ALL {
		for mode := DC_PRED; mode <= PAETH_PRED; mode++ {
			for a := -3; a <= 3; a++ {
				if a != 0 && !isDirectional(mode) {
					continue
				}
				for av := range 16 * 6 {
					fi := av/16 - 1
					if fi >= 0 && (mode != DC_PRED || txWidth[txSz] > 32 || txHeight[txSz] > 32) {
						continue
					}
					b := intraBlock{plane: 0, x: 64, y: 64, log2W: int(txWidthLog2[txSz]), log2H: int(txHeightLog2[txSz]),
						haveLeft: av&1 != 0, haveAbove: av&2 != 0, haveAboveRight: av&4 != 0, haveBelow: av&8 != 0,
						maxX: 255, maxY: 255, mode: mode, angleDelta: a, filterIntra: fi, bitDepth: 8}
					e1.predictIntra(&b, rec, 256, p1)
					e2.predictIntra(&b, rec, 256, p2)
					w, h := 1<<b.log2W, 1<<b.log2H
					for i := range w * h {
						if p1[i] != p2[i] {
							t.Fatalf("tx %d mode %d angle %d avail %04b: (%d,%d) reads outside its edges", txSz, mode, a, av, i%w, i/w)
						}
					}
				}
			}
		}
	}
}

// CDEF signalled, every strength zero.
func TestDbgCdefSignal(t *testing.T) {
	o := EncoderOptions{Width: 200, Height: 136, BitDepth: 8, QIndex: 100, FPSNum: 25, FPSDen: 1}
	checkDecodesWith(t, o, []*Source{testPicture(200, 136, 8, 1)}, func(e *Encoder) { e.seq.cdef = true })
}
