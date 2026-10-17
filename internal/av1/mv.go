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
}
