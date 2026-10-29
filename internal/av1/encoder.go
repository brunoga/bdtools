// Package av1 is an AV1 encoder: 4:2:0 pictures of 8 or 10 bits coded as
// an AV1 stream (OBUs, the low overhead format), each picture's
// reconstruction exactly a decoder's.
package av1

import (
	"errors"
	"fmt"
	"sync"
)

// Source is a picture to code: 4:2:0, a sample a uint16.
type Source struct {
	Y, Cb, Cr        []uint16
	StrideY, StrideC int
}

// EncoderOptions are an encoder's settings.
type EncoderOptions struct {
	Width, Height int
	BitDepth      int // 8 or 10
	// QIndex is the quantizer index, 1..255 (0 codes losslessly).
	QIndex         int
	FPSNum, FPSDen int
	// Primaries, Transfer and Matrix are the colour description the
	// stream states (H.273); 0 for unspecified.
	Primaries, Transfer, Matrix int
	FullRange                   bool
	// TileColsLog2 and TileRowsLog2 ask for 1<<n tile columns and rows
	// (as the limits allow), coded at once: -1 the fewest the limits
	// allow, one tile to 4096x2304. (A tile starts its CDFs and its
	// neighbours afresh: on the two 120-picture 1080p clips one tile is
	// 5.9% smaller than 2x2, two columns 3.6%, for 1.31 and 1.17 times
	// the time; the superblock rows of a tile and several frames are
	// coded at once still.)
	TileColsLog2, TileRowsLog2 int
	// Keyint is the key frame interval; 0 picks 250, 1 codes key frames
	// only.
	Keyint int
	// MiniGOP is how many pictures a mini-GOP has (coded as a pyramid,
	// the last first); 0 picks 32, 1 codes each picture from the one
	// before.
	MiniGOP int
	// Speed trades size for time: -2 the partitions searched without
	// pruning (5.3 times slower, 3.0% smaller on two 120-picture 1080p
	// clips); -1 those after a skipped whole block too, intra everywhere
	// and every inter candidate coded in full, its transforms searched,
	// the best's interpolation filter by coding it with each (2.8 times
	// slower, 2.3% smaller); 0 every tool searched, intra in
	// inter pictures where its estimate is near inter's, inter candidates
	// coded in full where theirs are near the best's, with their largest
	// transforms, DCT_DCT, the best's transforms then searched; 1 the
	// transform trees decided with
	// DCT_DCT, no warped motion or masked compound, no compound of two
	// earlier frames or joint compound motion search, CDEF's damping from
	// q, luma's two deblocking levels the same, the temporal model's
	// vectors whole samples and nine of the self-guided filter's sixteen
	// sets (1.56 times faster, 2.6% larger on six clips); 2 also no
	// inter-intra, four references and two candidates coded in full
	// (1.88 times, 3.8%).
	Speed int
}

// tools are the search's settings, by EncoderOptions.Speed (from the
// package's defaults, which tests and benches change).
type tools struct {
	interCands                      int
	filterRD, obmc, fewRefs, txTree bool
	pruneSkip, pruneExt             bool // the partition search's prunings
	extEst                          float64
	fastCands                       bool // inter candidates costed with DCT_DCT, the best's transforms searched
	// the filters' and the temporal model's searches cut short: CDEF's
	// damping from q, luma's deblocking levels the same, the model's
	// vectors whole samples, a subset of the self-guided filter's sets
	cdefDampQ, lfBoth, tplFull, sgrSubset bool
	intraSkip, candRatio                  float64
	sameDir                               int // sameDirComp's
	txCands                               int
	jointME                               bool
}

// Encoder codes pictures.
type Encoder struct {
	o     EncoderOptions
	seq   seqParams
	tools tools
	frame *frameEnc // the last frame coded (with keepRecon)
	// queue is the pictures not coded yet; nIn counts the pictures in;
	// gopStart is the picture before the mini-GOP being planned.
	gopStart int
	// runs are the mini-GOPs being coded, oldest first.
	runs []*gopRun
	// pending is the coded OBUs of the temporal unit being made.
	queue   []*frameJob
	nIn     int
	pending []byte
	// key is the key frame's step, planned and coded with the mini-GOP
	// after it.
	key []step
	// tfPast is the sources of the pictures just before the queue's (back
	// to the last key frame's), for the temporal filter's window.
	tfPast []*Source
	// keepRecon keeps each shown picture's reconstruction in its unit,
	// and the last frame coded in frame (tests).
	keepRecon bool
	recons    map[int]*[3][]uint16
	// recent is the last pictures in, by display index; tplRec the
	// temporal model's luma reconstructions of the frames kept.
	recent map[int]*Source
	tplRec map[int][]uint16
	// slots are the reference frame slots (the spec's eight).
	slots [NUM_REF_FRAMES]*refFrameState
	// lfFixed, when set, gives the loop filter levels and sharpness
	// instead of searching them (for tests).
	lfFixed *[5]int
	// sbQHook, when set, gives a frame's superblocks' q indexes (for
	// tests; n superblocks).
	sbQHook func(n int) []int
	// frameHook, when set, sees each frame once its tiles are coded
	// (frames coded at once call it at once; for tests).
	frameHook func(f *frameEnc)
	// cdefFixed, when set, gives the CDEF parameters instead of
	// searching them (for tests).
	cdefFixed *cdefSettings
	// lrFixed, when set, gives the loop restoration settings instead of
	// searching them (for tests).
	lrFixed *lrSettings
}

// NewEncoder makes an encoder.
func NewEncoder(o EncoderOptions) (*Encoder, error) {
	if o.Width < 16 || o.Height < 16 || o.Width > 65536 || o.Height > 65536 {
		return nil, fmt.Errorf("av1: %dx%d: no such picture size", o.Width, o.Height)
	}
	if o.BitDepth != 8 && o.BitDepth != 10 {
		return nil, errors.New("av1: 8 or 10 bits")
	}
	if o.QIndex < 0 || o.QIndex > 255 {
		return nil, errors.New("av1: q index 0..255")
	}
	e := &Encoder{o: o, recons: map[int]*[3][]uint16{}, recent: map[int]*Source{}, tplRec: map[int][]uint16{}}
	e.seq = seqParams{
		width: o.Width, height: o.Height, bitDepth: o.BitDepth,
		edgeFilter: true, filterIntra: true, cdef: true, restoration: true, jntComp: !dbgNoJntComp, refFrameMvs: !dbgNoRefFrameMvs, warpedMotion: !dbgNoWarp, maskedCompound: !dbgNoMasked, interIntra: !dbgNoInterIntra,
		primaries: o.Primaries, transfer: o.Transfer, matrix: o.Matrix, fullRange: o.FullRange,
		num: o.FPSNum, den: o.FPSDen,
	}
	e.tools = tools{interCands: interCands, obmc: !dbgNoObmc, fewRefs: dbgFewRefs, txTree: pruneTxTree,
		pruneSkip: !dbgNoPruneSkip, pruneExt: !dbgNoPruneExt, intraSkip: intraSkip, candRatio: candRatio, sameDir: sameDirComp, txCands: txCands, jointME: jointME, extEst: extEst, fastCands: true}
	if o.Speed <= -1 {
		e.tools.pruneSkip, e.tools.intraSkip, e.tools.candRatio, e.tools.fastCands = false, 0, 0, false
		e.tools.filterRD = filterRD
	}
	if o.Speed <= -2 {
		e.tools.pruneExt = false
	}
	if o.Speed >= 1 {
		e.tools.txTree = true
		e.tools.intraSkip = min(e.tools.intraSkip, 1)
		if e.tools.candRatio > 0 {
			e.tools.candRatio = min(e.tools.candRatio, 1.05)
		}
		e.seq.warpedMotion, e.seq.maskedCompound = false, false
		e.tools.sameDir = 0
		e.tools.jointME = false
		e.tools.cdefDampQ, e.tools.lfBoth, e.tools.tplFull, e.tools.sgrSubset = true, true, true, true
	}
	if o.Speed >= 2 {
		// (OBMC is worth 0.2-0.3% here on every metric for 1.5% more CPU)
		e.tools.fewRefs = true
		e.tools.interCands = min(e.tools.interCands, 2)
		// (inter-intra is worth nothing at speed 0 on the six clips, and
		// costs 0.3% here, 0.7% by VMAF)
		e.seq.interIntra = false
	}
	return e, nil
}

// SequenceHeader is the sequence header OBU (for a container's
// configuration record).
func (e *Encoder) SequenceHeader() []byte {
	return appendOBU(nil, obuSequenceHeader, e.seq.sequenceHeader())
}

// codeFrame codes a frame (a key frame or an inter frame, shown or not,
// as the job says), giving its frame OBU.
func (e *Encoder) codeFrame(j *frameJob) []byte {
	key := j.key
	fp := &frameParams{key: key, show: j.show, refresh: j.refresh, refIdx: j.refIdx, orderHint: j.disp, adapt: true,
		qIndex: j.q, txSelect: true, cdefDamping: 3}
	src := j.src
	miCols, miRows := 2*((e.o.Width+7)>>3), 2*((e.o.Height+7)>>3)
	cl, rl := e.o.TileColsLog2, e.o.TileRowsLog2
	cl, rl = max(cl, 0), max(rl, 0) // newTiling raises them to the limits'
	fp.tiles = newTiling(miCols, miRows, cl, rl)
	if !key {
		fp.highPrecisionMV = fp.qIndex < hpMaxQ
		fp.interpFilter = EIGHTTAP
		if switchable {
			fp.interpFilter = SWITCHABLE
		}
	}
	if (e.sbQHook != nil || j.sbQ != nil) && fp.qIndex > 0 {
		fp.deltaQ = true
	}
	f := e.newFrame(fp, src)
	switch {
	case e.sbQHook != nil && fp.deltaQ:
		f.sbQ = e.sbQHook(((f.miCols + 15) >> 4) * ((f.miRows + 15) >> 4))
	case fp.deltaQ:
		f.sbQ = j.sbQ
	}
	f.sbLambda = j.sbLambda
	fp.primaryRef = PRIMARY_REF_NONE
	f.cdf0 = newCDFs(fp.qIndex)
	if r := j.refs[0]; !key && r != nil && r.cdf != nil && !dbgNoCDFCarry {
		fp.primaryRef = 0 // LAST_FRAME's
		if cdfSameDepth {
			// the reference nearest the frame's depth (its q, its
			// symbols' statistics), then nearest shown
			best := -1
			for i, o := range j.refs {
				if o == nil || o.cdf == nil {
					continue
				}
				if best < 0 {
					best = i
					continue
				}
				b := j.refs[best]
				if d, bd := abs(o.depth-j.depth), abs(b.depth-j.depth); d < bd || d == bd && abs(o.disp-j.disp) < abs(b.disp-j.disp) {
					best = i
				}
			}
			if best >= 0 {
				fp.primaryRef, r = best, j.refs[best]
			}
		}
		f.cdf0 = r.cdf.loaded()
	}
	if !key {
		fi := newInterFrameInfo()
		fi.allowHighPrecisionMv = fp.highPrecisionMV
		fi.enableOrderHint, fi.orderHintBits, fi.orderHint = true, orderHintBits, fp.orderHint&(1<<orderHintBits-1)
		for ref := LAST_FRAME; ref <= ALTREF_FRAME; ref++ {
			r := j.refs[ref-LAST_FRAME]
			f.refs[ref] = r
			fi.orderHints[ref] = r.orderHint & (1<<orderHintBits - 1)
			fp.refHints[ref-LAST_FRAME] = fi.orderHints[ref]
			if !dbgNoCompound && (r.disp > j.disp || e.tools.sameDir > 0 && r != j.refs[0]) {
				fp.refSelect = true // two frames to compound
			}
		}
		fi.setRefFrameSignBias()
		if ok, frames := fp.skipModeAllowed(); fp.refSelect && ok && !dbgNoSkipMode {
			fp.skipModePresent, fp.skipModeFrame = true, frames
		}
		fp.motionModeSwitchable = e.tools.obmc || e.seq.warpedMotion
		fp.allowWarp = e.seq.warpedMotion
		f.inter = fi
		if e.seq.refFrameMvs {
			fp.useRefFrameMvs, fi.useRefFrameMvs = true, true
			f.motionFieldEstimation()
		}
		f.filt = [2]int{EIGHTTAP, EIGHTTAP} // the motion search's
		if fp.interpFilter != SWITCHABLE {
			f.filt = [2]int{fp.interpFilter, fp.interpFilter}
		}
	}
	recs := make([][]symOp, fp.tiles.rows()*fp.tiles.cols())
	var wg sync.WaitGroup
	for tr := range fp.tiles.rows() {
		for tc := range fp.tiles.cols() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				recs[tr*fp.tiles.cols()+tc] = codeTile(f, fp.tiles.rowStarts[tr]*16, min(fp.tiles.rowStarts[tr+1]*16, miRows),
					fp.tiles.colStarts[tc]*16, min(fp.tiles.colStarts[tc+1]*16, miCols))
			}()
		}
	}
	wg.Wait()
	if e.frameHook != nil {
		e.frameHook(f)
	}
	f.deblock(e.lfFixed)
	deblocked := f.rec // CDEF filters into a new frame
	f.cdef(e.cdefFixed)
	lr := f.restore(deblocked, e.lrFixed)
	var tiles [][]byte
	saved := f.cdf0
	for i, ops := range recs {
		b, end := pack(ops, f.cdf0, fp.adapt, f.cdefIdx, fp.cdefBits, lr)
		tiles = append(tiles, b)
		if i == 0 && fp.adapt { // context_update_tile_id 0
			saved = end
		}
	}
	f.savedCDF = saved
	j.frame = f
	// field by field: done is read by the frames waiting on this one
	st := j.state
	st.cdf, st.rec, st.stride, st.orderHint = f.savedCDF, f.rec, f.stride, fp.orderHint
	st.miCols, st.miRows, st.width, st.height = f.miCols, f.miRows, f.width, f.height
	st.frameType = INTER_FRAME
	if key {
		st.frameType = KEY_FRAME
	}
	f.saveMotionField(st)
	payload := append(e.seq.frameHeader(fp), fp.tileGroup(tiles)...)
	return appendOBU(nil, obuFrame, payload)
}

// dbgNoCDFCarry starts every frame from the default CDFs.
var dbgNoCDFCarry bool

// newFrame sets a frame up: its source padded to whole superblocks.
func (e *Encoder) newFrame(fp *frameParams, src *Source) *frameEnc {
	o := e.o
	miCols, miRows := 2*((o.Width+7)>>3), 2*((o.Height+7)>>3)
	f := &frameEnc{seq: &e.seq, tools: &e.tools, fp: fp, bitDepth: o.BitDepth, miCols: miCols, miRows: miRows, width: o.Width, height: o.Height}
	pw, ph := (miCols+15)/16*64, (miRows+15)/16*64
	for p := range 3 {
		w, h, sw, sh := pw, ph, o.Width, o.Height
		in, stride := src.Y, src.StrideY
		if p > 0 {
			w, h, sw, sh = pw/2, ph/2, (o.Width+1)/2, (o.Height+1)/2
			in, stride = src.Cb, src.StrideC
			if p == 2 {
				in = src.Cr
			}
		}
		f.stride[p] = w
		f.src[p] = make([]uint16, w*h)
		f.rec[p] = make([]uint16, w*h)
		for y := range h {
			row := in[min(y, sh-1)*stride:]
			dst := f.src[p][y*w:]
			copy(dst[:sw], row[:sw])
			for x := sw; x < w; x++ {
				dst[x] = row[sw-1]
			}
		}
	}
	n := miCols * miRows
	f.mis = make([]modeInfo, n)
	for p := range 3 {
		sub := min(p, 1)
		f.lfTx[p] = make([]uint8, (miCols>>sub)*(miRows>>sub))
	}
	for i := range f.mis {
		f.mis[i].refFrames = [2]int8{INTRA_FRAME, NONE}
	}
	f.cdefIdx = make([]int8, ((miCols+15)>>4)*((miRows+15)>>4))
	for i := range f.cdefIdx {
		f.cdefIdx[i] = -1
	}
	f.lossless = fp.lossless()
	bd := (o.BitDepth - 8) >> 1
	qi := func(v int) int { return min(max(v, 0), 255) }
	f.dcQ = [3]int{int(dcQlookup[bd][qi(fp.qIndex+fp.deltaQYDc)]), int(dcQlookup[bd][qi(fp.qIndex+fp.deltaQUVDc)]), int(dcQlookup[bd][qi(fp.qIndex+fp.deltaQUVDc)])}
	f.acQ = [3]int{int(acQlookup[bd][qi(fp.qIndex)]), int(acQlookup[bd][qi(fp.qIndex+fp.deltaQUVAc)]), int(acQlookup[bd][qi(fp.qIndex+fp.deltaQUVAc)])}
	// libaom's: 88/24 q^2 per bit, its distortion 2048 times the sum of
	// squared errors
	f.lambda = lambdaFor(f.dcQ[0])
	return f
}

// recon is a frame's reconstruction cropped to the picture (width by
// height).
func (f *frameEnc) recon(width, height int) (y, cb, cr []uint16) {
	crop := func(p, w, h int) []uint16 {
		out := make([]uint16, w*h)
		for i := range h {
			copy(out[i*w:(i+1)*w], f.rec[p][i*f.stride[p]:])
		}
		return out
	}
	cw, ch := (width+1)/2, (height+1)/2
	return crop(0, width, height), crop(1, cw, ch), crop(2, cw, ch)
}

// hpMaxQ is the q below which inter frames allow eighth sample vectors:
// none, as their bits cost more than they save. (On the two 120-picture
// clips: 0.2% smaller and 3% faster than below 128, libaom's; below 100
// 0.2% larger, below 160 0.7%.)
var hpMaxQ = 0

// cdfSameDepth has an inter frame take its CDFs (primary_ref_frame) from the
// reference nearest its depth, as libaom does from its own layer's, not
// LAST_FRAME's. (On the two 120-picture clips: 0.5% smaller at the same
// speed.)
var cdfSameDepth = true
