package mvc

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
)

const (
	padY = 32 // luma border in pixels (must be >= 16+5)
	padC = 16 // chroma border in pixels (must be >= 8+1)
)

type mv struct{ x, y int16 }

// picture is a decoded (or being decoded) view component with its border
// padded sample planes and the motion data needed by later pictures.
type picture struct {
	id int32 // unique per decoded picture, used to identify references

	planes [3][]byte
	stride [3]int
	origin [3]int // offset of sample (0,0) in planes

	mbW, mbH int
	width    int // luma width (multiple of 16)
	height   int

	// the encoder's list 0 vectors per macroblock when it coded the
	// picture: search starting points for the pictures predicting from it
	encMV []mv

	// the luma's half-sample planes, for an encoder (halfPel)
	hpel   [3][]byte
	hpelOK bool
	hpelMu sync.Mutex

	// motion data per 4x4 block, stride mbW*4
	mvs  [2][]mv
	refs [2][]int8
	// per macroblock slice index into sliceInfo (for reference identity)
	mbSlice   []uint16
	fieldMB   []bool // per macroblock: an MBAFF field macroblock
	sliceInfo [maxSliceChunks]*[sliceChunk]sliceRefInfo

	// reference marking and ordering state
	poc              int32
	pts              int64
	frameNum         int
	longTermFrameIdx int
	shortRef         bool
	longRef          bool
	outputNeeded     bool
	idr              bool
	mmco5            bool
	viewID           int
	viewIdx          int
	interView        bool // usable for inter-view prediction
	anchor           bool

	// cropping
	cropLeft, cropRight, cropTop, cropBottom int

	// decode progress: number of luma rows (in MB rows) fully decoded,
	// deblocked and border extended.
	mu       sync.Mutex
	cond     *sync.Cond
	prog     atomic.Int32
	progress int
	done     bool
	inflight int // in-flight decodes referencing this picture (poolMu)

	decoding    bool
	userHeld    bool
	nonExisting bool

	// With an accelerator: the picture's surface, and its fields' POCs.
	surface        int
	topPOC, botPOC int32

	// Interlaced coding. A frame keeps its fields (fld) as pictures of
	// their own, made on first use: decoded into through a view of the
	// frame's planes (every other row), predicted from through a copy of
	// those rows with its own border. A field has its frame (parent) and
	// parity (0 top, 1 bottom).
	fld    [2]*picture
	parent *picture
	parity int
	// refMask: a frame's fields marked for reference (bit 0 top, bit 1
	// bottom); shortRef and longRef say which list the frame is in.
	refMask uint8
	// fieldsDone: a frame's fields decoded as field pictures (bits as
	// refMask); asFields: the frame's pictures are fields.
	mbaffFrame    bool // a frame coded MBAFF
	fieldsDone    uint8
	fieldsStarted uint8 // a frame's fields whose decoding started (decoder side)
	asFields      bool
	// asField: a field decoded as a field picture (set when its decoding
	// starts); its copy (copyOK once it holds the field) and the copy's
	// buffers, kept across reuse
	asField    bool
	copyMu     sync.Mutex
	copyOK     bool
	copyPlanes [3][]byte
	copyStride [3]int
	copyOrigin [3]int
}

// field returns the frame's field of the parity, made on first use.
func (p *picture) field(parity int) *picture {
	if f := p.fld[parity]; f != nil {
		return f
	}
	f := &picture{mbW: p.mbW, mbH: p.mbH / 2, width: p.width, height: p.height / 2, surface: -1, parent: p, parity: parity}
	n4 := f.mbW * 4 * f.mbH * 4
	for l := 0; l < 2; l++ {
		f.mvs[l] = make([]mv, n4)
		f.refs[l] = make([]int8, n4)
	}
	f.mbSlice = make([]uint16, f.mbW*f.mbH)
	f.cond = sync.NewCond(&f.mu)
	p.fld[parity] = f
	return f
}

// fieldRef returns the frame's field of the parity as a reference: its
// identity, order count and frame number brought up to date with the
// frame's.
func (p *picture) fieldRef(parity int) *picture {
	f := p.field(parity)
	f.syncFromFrame()
	return f
}

// syncFromFrame sets a field's identity and order count from its frame.
func (p *picture) syncFromFrame() {
	fr := p.parent
	p.id = fieldID(fr.id, p.parity)
	p.poc = fr.topPOC
	if p.parity == 1 {
		p.poc = fr.botPOC
	}
	p.frameNum = fr.frameNum
	p.viewID, p.viewIdx = fr.viewID, fr.viewIdx
	p.nonExisting = fr.nonExisting
}

// viewFrame points a field's planes at its rows of the frame's, for
// decoding into.
func (p *picture) viewFrame() {
	fr := p.parent
	for c := 0; c < 3; c++ {
		p.planes[c] = fr.planes[c]
		p.stride[c] = 2 * fr.stride[c]
		p.origin[c] = fr.origin[c] + p.parity*fr.stride[c]
	}
}

// makeCopy copies a field's rows of its frame into its own planes and
// extends their borders: the field to predict from. The frame's rows must
// be final.
func (p *picture) makeCopy() {
	fr := p.parent
	for c := 0; c < 3; c++ {
		w, h, pad := p.width, p.height, padY
		if c > 0 {
			w, h, pad = w/2, h/2, padC
		}
		if p.copyPlanes[c] == nil {
			stride := (w + 2*pad + 63) &^ 63
			p.copyStride[c] = stride
			p.copyOrigin[c] = pad*stride + pad
			p.copyPlanes[c] = make([]byte, stride*(h+2*pad))
		}
		src, ss := fr.planes[c], 2*fr.stride[c]
		so := fr.origin[c] + p.parity*fr.stride[c]
		dst, ds, do := p.copyPlanes[c], p.copyStride[c], p.copyOrigin[c]
		for y := 0; y < h; y++ {
			copy(dst[do+y*ds:do+y*ds+w], src[so+y*ss:so+y*ss+w])
		}
	}
	p.planes, p.stride, p.origin = p.copyPlanes, p.copyStride, p.copyOrigin
	p.extendRows(0, p.mbH)
	p.copyOK = true
}

// ensureCopy makes the field ready to predict from: a field decoded as a
// field picture has its copy made when it completes; one of a frame
// decoded as a frame gets it once the frame is complete.
func (p *picture) ensureCopy() {
	if p.asField {
		p.waitRows(p.mbH) // its decoding makes the copy, then signals
		return
	}
	fr := p.parent
	fr.waitRows(fr.mbH)
	p.copyMu.Lock()
	if !p.copyOK {
		p.makeCopy()
		p.setProgress(p.mbH)
	}
	p.copyMu.Unlock()
}

// resetFields prepares a frame's fields for a new picture.
func (p *picture) resetFields() {
	p.refMask, p.fieldsDone, p.fieldsStarted, p.asFields, p.mbaffFrame = 0, 0, 0, false, false
	for _, f := range p.fld {
		if f == nil {
			continue
		}
		f.reset()
		f.asField = false
		f.copyMu.Lock()
		f.copyOK = false
		f.copyMu.Unlock()
	}
}

// inUse reports whether the buffer is still needed (must hold poolMu).
func (p *picture) inUse() bool {
	return p.decoding || p.userHeld || p.outputNeeded || p.shortRef || p.longRef || p.inflight > 0
}

const (
	sliceChunk     = 32
	maxSliceChunks = 256
	maxSlices      = sliceChunk * maxSliceChunks
)

// sliceRefInfo records the reference lists of one slice.
type sliceRefInfo struct {
	ids [2][maxRefsPerList]int32
	poc [2][maxRefsPerList]int32
	lt  [2][maxRefsPerList]bool
}

func (p *picture) sliceRef(i int) *sliceRefInfo {
	return &p.sliceInfo[i/sliceChunk][i%sliceChunk]
}

// setSliceRef stores the reference info of slice i. Chunks are kept across
// picture reuse so readers never observe a reallocation.
func (p *picture) setSliceRef(i int, info *sliceRefInfo) {
	c := p.sliceInfo[i/sliceChunk]
	if c == nil {
		c = new([sliceChunk]sliceRefInfo)
		p.sliceInfo[i/sliceChunk] = c
	}
	c[i%sliceChunk] = *info
}

func allocNewPicture(mbW, mbH int) *picture {
	p := &picture{mbW: mbW, mbH: mbH, width: mbW * 16, height: mbH * 16, surface: -1}
	for c := 0; c < 3; c++ {
		w, h, pad := p.width, p.height, padY
		if c > 0 {
			w, h, pad = w/2, h/2, padC
		}
		stride := (w + 2*pad + 63) &^ 63
		p.stride[c] = stride
		p.origin[c] = pad*stride + pad
		p.planes[c] = make([]byte, stride*(h+2*pad))
	}
	n4 := mbW * 4 * mbH * 4
	for l := 0; l < 2; l++ {
		p.mvs[l] = make([]mv, n4)
		p.refs[l] = make([]int8, n4)
	}
	p.mbSlice = make([]uint16, mbW*mbH)
	p.fieldMB = make([]bool, mbW*mbH)
	p.cond = sync.NewCond(&p.mu)
	return p
}

// halfPel gives the picture's luma at its half-sample positions, three
// planes laid out as plane 0 (b, h and j of 8.4.2.2.1), computed on the
// first call after the picture is decoded in full, for an encoder whose
// search reads them instead of filtering every candidate block: a
// quarter-sample prediction is then an average of two samples, and
// exactly the decoder's. The planes cover the border the prediction may
// reach (lumaMC's clamping) and no more.
func (p *picture) halfPel() *[3][]byte {
	p.hpelMu.Lock()
	defer p.hpelMu.Unlock()
	if p.hpelOK {
		return &p.hpel
	}
	ss, n := p.stride[0], len(p.planes[0])
	for i := range p.hpel {
		if len(p.hpel[i]) != n {
			p.hpel[i] = make([]byte, n)
		}
	}
	// 6-tap filters reach 2 samples before and 3 after: the border of
	// padY allows [-padY+2, w+padY-3); the prediction reads at most
	// [-w-3, width+18]. Tiles of 16 over a range that is a multiple of
	// 16 wide and high.
	const reach = padY - 8
	src := p.planes[0]
	var tmp [21 * 16]int16
	for y := -reach; y < p.height+reach; y += 16 {
		for x := -reach; x < p.width+reach; x += 16 {
			so := p.origin[0] + y*ss + x
			hpelH(p.hpel[0][so:], ss, src, so, ss, 16, 16)
			hpelV(p.hpel[1][so:], ss, src, so, ss, 16, 16)
			hpelJ(p.hpel[2][so:], ss, tmp[:], src, so, ss, 16, 16)
		}
	}
	p.hpelOK = true
	return &p.hpel
}

// reset prepares a picture for decoding a new view component.
func (p *picture) reset() {
	p.hpelMu.Lock()
	p.hpelOK = false
	p.hpelMu.Unlock()
	p.shortRef, p.longRef, p.outputNeeded = false, false, false
	p.idr, p.mmco5 = false, false
	p.nonExisting = false
	p.nonExisting = false
	p.progress = 0
	p.prog.Store(0)
	p.done = false
}

// setProgress publishes that MB rows [0, rows) are final.
func (p *picture) setProgress(rows int) {
	p.mu.Lock()
	if rows > p.progress {
		p.progress = rows
		p.prog.Store(int32(rows))
		p.cond.Broadcast()
	}
	p.mu.Unlock()
}

// waitRows blocks until MB rows [0, rows) are final.
func (p *picture) waitRows(rows int) {
	if rows > p.mbH {
		rows = p.mbH
	}
	if int(p.prog.Load()) >= rows {
		return
	}
	p.mu.Lock()
	for p.progress < rows {
		p.cond.Wait()
	}
	p.mu.Unlock()
}

// extendRows replicates the picture borders for MB rows [r0, r1).
func (p *picture) extendRows(r0, r1 int) {
	for c := 0; c < 3; c++ {
		pad, mbs := padY, 16
		w := p.width
		if c > 0 {
			pad, mbs, w = padC, 8, p.width/2
		}
		st := p.stride[c]
		pl := p.planes[c]
		y0, y1 := r0*mbs, r1*mbs
		for y := y0; y < y1; y++ {
			row := pl[p.origin[c]+y*st-pad : p.origin[c]+y*st+w+pad]
			l := uint64(row[pad]) * 0x0101010101010101
			r := uint64(row[pad+w-1]) * 0x0101010101010101
			left, right := row[:pad], row[pad+w:pad+w+pad]
			for i := 0; i < pad; i += 8 {
				binary.LittleEndian.PutUint64(left[i:], l)
				binary.LittleEndian.PutUint64(right[i:], r)
			}
		}
		if r0 == 0 {
			src := pl[p.origin[c]-pad : p.origin[c]-pad+st]
			for y := 1; y <= pad; y++ {
				o := p.origin[c] - pad - y*st
				copy(pl[o:o+st], src)
			}
		}
		if r1 == p.mbH {
			h := p.mbH * mbs
			src := pl[p.origin[c]-pad+(h-1)*st : p.origin[c]-pad+h*st]
			for y := 0; y < pad; y++ {
				o := p.origin[c] - pad + (h+y)*st
				copy(pl[o:o+st], src)
			}
		}
	}
}

// fieldID is the identity of a frame's field: positive (negative ids
// mean no reference) and apart from frames'.
func fieldID(frame int32, parity int) int32 {
	return int32(1<<30 | (uint32(frame)*2+uint32(parity))&(1<<30-1)) //nolint:gosec // within 31 bits
}
