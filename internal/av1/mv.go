package av1

import "sync"

// Motion vectors and the per-mi motion state inter frames keep.

// mv is a motion vector in 1/8 sample units (luma), the spec's [row, col].
type mv struct{ row, col int32 }

// refFrameState is what a frame kept as a reference has for later frames:
// its pictures (post filters, padded as the frame's), its size in samples
// (RefUpscaledWidth, RefFrameHeight: prediction clamps to it, not to the
// padding), its order hint, its size in mi, and its motion (for temporal
// motion vector projection, unused while use_ref_frame_mvs is 0).
type refFrameState struct {
	rec            [3][]uint16
	stride         [3]int
	width, height  int
	orderHint      int
	miCols, miRows int
	frameType      int
	disp           int // its place in display order
	depth          int // its depth in the pyramid (0 key frames, 1 anchors)
	// done is closed once the frame is coded (its other fields set)
	done chan struct{}
	// cdf is the frame's CDFs as kept for later frames (save_cdfs).
	cdf *cdfs
	// refHints are SavedOrderHints (by reference); mfRef and mfMv the
	// saved motion field (SavedRefFrames, SavedMvs) on the 8x8 grid.
	refHints [TOTAL_REFS_PER_FRAME]int
	mfRef    []int8
	mfMv     []mv
	// depths is the depth (below the superblock, by the larger side)
	// of the block at each 8x8 grid point (the depth pass's prior)
	depths []uint8
	// rec8 is the luma as 8-bit samples (an 8-bit frame's, for the
	// motion search's SADs), made once on first use; nil for a
	// reference made up for a search (none made)
	rec8 *luma8Cache
}

// luma8Cache is a reference's luma as 8-bit samples, made once.
type luma8Cache struct {
	once sync.Once
	y    []uint8
}

// luma8 is the reference's luma as 8-bit samples, or nil unless it keeps
// them (rec8) and the frame is 8-bit and the 8-bit SAD kernel can be
// used.
func (r *refFrameState) luma8(bitDepth int) []uint8 {
	if r.rec8 == nil || bitDepth != 8 || !useAVX2 {
		return nil
	}
	r.rec8.once.Do(func() { r.rec8.y = narrow(r.rec[0]) })
	return r.rec8.y
}

// narrow is 8-bit samples' 16-bit copies narrowed.
func narrow(in []uint16) []uint8 {
	out := make([]uint8, len(in))
	for i, v := range in {
		out[i] = uint8(v) //nolint:gosec // 8-bit samples
	}
	return out
}
