package mvc

// Macroblock flags stored in mbInfo.flags.
const (
	mbfIntra    = 1 << iota
	mbfSkip     // P_Skip / B_Skip
	mbfPCM      // I_PCM
	mbfT8x8     // transform_size_8x8_flag
	mbfDirect16 // B_Skip or B_Direct_16x16
	mbfI16x16
	mbfINxN
	mbfAvail // decoded in the current picture
)

// mbInfo is the per-macroblock state kept for neighbour derivations and
// deblocking.
type mbInfo struct {
	flags      uint16
	cbp        uint8 // bits 0-3 luma 8x8, bits 4-5 chroma (0,1,2)
	qp         int8
	chromaPred uint8
	directSub  uint8  // B_8x8 sub-macroblocks coded as B_Direct_8x8
	slice      uint16 // slice index within the picture
	cbf        uint32 // CABAC coded_block_flag bits (see cbf* constants)
	nzMask     uint16 // luma 4x4 blocks (raster) with non-zero coefficients, expanded for 8x8
	mvEdges    uint8  // internal edges whose sides may differ in motion: bits 0-2 vertical edges 1-3, bits 4-6 horizontal
}

const (
	cbfLumaDC = 1 << 24
	cbfCbDC   = 1 << 25
	cbfCrDC   = 1 << 26
)

// sliceParams holds per-slice values needed after parsing (deblocking).
type sliceParams struct {
	disableDeblock int
	alphaOffset    int
	betaOffset     int
	chromaQPOffset [2]int
}

// frameCtx is the per-picture decoding state shared by its slices.
type frameCtx struct {
	pic  *picture
	sps  *sps
	mbW  int
	mbH  int
	mbs  []mbInfo
	nnz  []uint8    // luma total_coeff per 4x4, stride mbW*4
	nnzC [2][]uint8 // chroma AC total_coeff per 4x4, stride mbW*2
	i4   []int8     // Intra4x4/8x8 pred modes per 4x4, stride mbW*4
	mvd  [2][][2]uint8
	// reference picture ids per 4x4 block (-1 if unused), for deblocking
	refIDs [2][]int32
	slices []sliceParams

	dba           deblockArgs // arguments of the assembly deblocking control
	rowsDeblocked int         // MB rows deblocked
	rowsFinal     int         // MB rows published as final
	broken        bool
}

func newFrameCtx(mbW, mbH int) *frameCtx {
	fc := &frameCtx{mbW: mbW, mbH: mbH}
	n := mbW * mbH
	fc.mbs = make([]mbInfo, n)
	fc.nnz = make([]uint8, n*16)
	fc.nnzC[0] = make([]uint8, n*4)
	fc.nnzC[1] = make([]uint8, n*4)
	fc.i4 = make([]int8, n*16)
	fc.mvd[0] = make([][2]uint8, n*16)
	fc.mvd[1] = make([][2]uint8, n*16)
	fc.refIDs[0] = make([]int32, n*16)
	fc.refIDs[1] = make([]int32, n*16)
	return fc
}

func (fc *frameCtx) reset(pic *picture, s *sps) {
	fc.pic = pic
	fc.sps = s
	clear(fc.mbs)
	fc.slices = fc.slices[:0]
	fc.rowsDeblocked = 0
	fc.rowsFinal = 0
	fc.broken = false
}

// Partition shapes.
const (
	part16x16 = iota
	part16x8
	part8x16
	part8x8
)

// Predicition flags.
const (
	predL0 = 1
	predL1 = 2
	predBi = 3
)

// mbData holds the parsed syntax of the current macroblock.
type mbData struct {
	flags      uint16
	cbp        uint8
	i16Mode    uint8
	chromaPred uint8
	shape      uint8
	partPred   [4]uint8 // per partition (16x16: [0], 16x8/8x16: [0..1], 8x8: [0..3])
	subShape   [4]uint8 // per 8x8 sub-macroblock: part8x8.. (8x8, 8x4, 4x8, 4x4)
	directSub  uint8    // bitmask of B_Direct_8x8 sub-macroblocks
	refIdx     [2][4]int8
	prevFlag   [16]bool
	remMode    [16]int8

	// residual, dequantized; luma blocks indexed by raster 4x4 position
	// residual coefficients in spatial layout: luma 16x16 (4x4 or 8x8
	// blocks at their positions), chroma 8x8 per component. Blocks are
	// cleared by the inverse transform.
	coefY [256]int16
	coefC [2][64]int16
	dcY   [16]int32
	nzY   uint16 // luma 4x4 blocks (raster) needing an inverse transform
	nz8   uint8  // luma 8x8 blocks needing an inverse transform
	acY   uint16 // luma 4x4 blocks with AC coefficients (others are DC-only)
	ac8   uint8  // luma 8x8 blocks with AC coefficients
	nzC   [2]uint8
	nzDC  bool
	nzCDC [2]bool
	pcm   [384]uint8
}

// Raster 4x4 block position for luma4x4BlkIdx and back.
var blkX = [16]uint8{0, 1, 0, 1, 2, 3, 2, 3, 0, 1, 0, 1, 2, 3, 2, 3}
var blkY = [16]uint8{0, 0, 1, 1, 0, 0, 1, 1, 2, 2, 3, 3, 2, 2, 3, 3}

var chromaQPTable = [52]int8{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29,
	29, 30, 31, 32, 32, 33, 34, 34, 35, 35, 36, 36, 37, 37, 37, 38, 38, 38, 39, 39, 39, 39,
}

func chromaQP(qp, offset int) int {
	q := qp + offset
	if q < 0 {
		q = 0
	} else if q > 51 {
		q = 51
	}
	return int(chromaQPTable[q])
}

// sliceDec decodes the macroblocks of one slice.
type sliceDec struct {
	fc       *frameCtx
	h        *sliceHeader
	pic      *picture
	sliceIdx int
	refList  [2][]*picture
	cabacOn  bool

	br     bitReader
	cab    cabac
	padded []byte // slice data with cabacPad zero bytes appended
	ctx    [512]uint8

	qp                             int
	qpc                            [2]int
	lastDQ                         int // previous mb_qp_delta (CABAC ctx)
	mbX, mbY                       int
	mbAddr                         int
	firstMb                        int
	mb                             mbData
	availA, availB, availC, availD bool

	dq4     *[6][6][16]int32
	dq8     *[2][6][64]int32
	dq4z    *[6][6][16]int32
	dq8z    *[2][6][64]int32
	refInfo *sliceRefInfo
	curPOC  int32
	deblock bool // the slice is deblocked (reference ids are needed)
	// uniformity bits of the last spatial direct derivation (see
	// directFillAsm), used by mcDirect instead of scanning the grids
	directUniform      int
	directUniformValid bool

	// inter prediction state
	implicitW  [maxRefsPerList][maxRefsPerList][2]int16 // implicit bi-pred weights
	colPic     *picture
	mapCol     map[int32]int // colocated ref picture id -> refIdxL0 (temporal direct)
	distScale  [maxRefsPerList]int32
	dsLongTerm [maxRefsPerList]bool
	wpMode     int
	mc         mcBuf
	cb         coeffBuf
}

// setQP updates the slice QP and derived chroma QPs.
func (s *sliceDec) setQP(qp int) {
	s.qp = qp
	s.qpc[0] = chromaQP(qp, s.h.pps.chromaQPOffset[0])
	s.qpc[1] = chromaQP(qp, s.h.pps.chromaQPOffset[1])
}

func (s *sliceDec) setMbPos(addr int) {
	w := s.fc.mbW
	if addr == s.mbAddr+1 && s.mbX+1 < w {
		s.mbX++
	} else if addr == s.mbAddr+1 {
		s.mbX, s.mbY = 0, s.mbY+1
	} else {
		s.mbY = addr / w
		s.mbX = addr - s.mbY*w
	}
	s.mbAddr = addr
	f := s.firstMb
	s.availA = s.mbX > 0 && addr-1 >= f
	s.availB = s.mbY > 0 && addr-w >= f
	s.availC = s.mbY > 0 && s.mbX < w-1 && addr-w+1 >= f
	s.availD = s.mbY > 0 && s.mbX > 0 && addr-w-1 >= f
}

// idx4 returns the picture-wide 4x4 grid index of block (bx,by) of the
// current macroblock.
func (s *sliceDec) idx4(bx, by int) int {
	return (s.mbY*4+by)*s.fc.mbW*4 + s.mbX*4 + bx
}

// rowDecoded is called when MB row y has been fully decoded: the previous
// row can then be deblocked (intra prediction of row y needed its
// unfiltered samples) and the rows above it published as final.
func (fc *frameCtx) rowDecoded(y int) {
	if y < 1 || fc.rowsDeblocked != y-1 {
		return
	}
	fc.deblockRow(y - 1)
	fc.rowsDeblocked = y
	fc.publish(y - 1)
}

// publish extends the borders of rows up to n and signals progress.
func (fc *frameCtx) publish(n int) {
	if n <= fc.rowsFinal {
		return
	}
	fc.pic.extendRows(fc.rowsFinal, n)
	fc.rowsFinal = n
	fc.pic.setProgress(n)
}

// finishRows deblocks and publishes the remaining rows of the picture.
func (fc *frameCtx) finishRows() {
	for y := fc.rowsDeblocked; y < fc.mbH; y++ {
		fc.deblockRow(y)
	}
	fc.rowsDeblocked = fc.mbH
	fc.publish(fc.mbH)
}

// finishRowsSafe is finishRows guarded against panics on corrupt data, so
// that progress is always published and waiting decoders never block.
func (fc *frameCtx) finishRowsSafe() {
	defer func() {
		if r := recover(); r != nil {
			fc.broken = true
			fc.rowsDeblocked = fc.mbH
			fc.rowsFinal = 0
			fc.pic.extendRows(0, fc.mbH)
			fc.pic.setProgress(fc.mbH)
		}
	}()
	fc.finishRows()
}

// conceal fills macroblocks that were not decoded (lost or corrupt
// slices) by copying the co-located samples of ref, or mid-grey.
func (fc *frameCtx) conceal(ref *picture) {
	pic := fc.pic
	missing := false
	for i := range fc.mbs {
		if fc.mbs[i].flags&mbfAvail == 0 {
			missing = true
			break
		}
	}
	if !missing {
		return
	}
	if ref != nil {
		ref.waitRows(ref.mbH)
	}
	for addr := range fc.mbs {
		m := &fc.mbs[addr]
		if m.flags&mbfAvail != 0 {
			continue
		}
		// treat as an intra macroblock with deblocking disabled
		*m = mbInfo{flags: mbfAvail | mbfIntra, slice: 0}
		x, y := addr%fc.mbW, addr/fc.mbW
		for c := 0; c < 3; c++ {
			n := 16
			if c > 0 {
				n = 8
			}
			st := pic.stride[c]
			o := pic.origin[c] + y*n*st + x*n
			for r := 0; r < n; r++ {
				d := pic.planes[c][o+r*st : o+r*st+n]
				if ref != nil {
					copy(d, ref.planes[c][o+r*st:o+r*st+n])
				} else {
					for i := range d {
						d[i] = 128
					}
				}
			}
		}
		st4 := fc.mbW * 4
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				b := (y*4+j)*st4 + x*4 + i
				pic.refs[0][b], pic.refs[1][b] = -1, -1
				pic.mvs[0][b], pic.mvs[1][b] = mv{}, mv{}
			}
		}
	}
	if len(fc.slices) == 0 {
		fc.slices = append(fc.slices, sliceParams{disableDeblock: 1})
		pic.setSliceRef(0, &sliceRefInfo{})
	}
}
