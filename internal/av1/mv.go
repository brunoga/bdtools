package av1

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
}
