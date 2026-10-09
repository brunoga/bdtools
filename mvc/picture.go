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

	// motion data per 4x4 block, stride mbW*4
	mvs  [2][]mv
	refs [2][]int8
	// per macroblock slice index into sliceInfo (for reference identity)
	mbSlice   []uint16
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
	p.cond = sync.NewCond(&p.mu)
	return p
}

// reset prepares a picture for decoding a new view component.
func (p *picture) reset() {
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
