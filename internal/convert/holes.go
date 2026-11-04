package convert

import "time"

// maxHole is the longest jump in the source's picture timestamps that is
// filled: longer, and the timestamps are more likely wrong than the
// pictures missing.
const maxHole = 30 * time.Second

// pictureHoles finds where the source's pictures skip time: a damaged
// source (a disc image with a run of zeros, a broken download) loses
// pictures, the sound beside them keeps its timestamps, and an output
// that packs the pictures back to back plays the rest of the film out of
// step with the sound. Each hole is filled with as many pictures as it
// lost, so the picture keeps the source's timeline.
type pictureHoles struct {
	last time.Duration
	seen bool
}

// missing reports how many pictures are lost before the one presented at
// at, frame being a picture's duration. A jump of up to one and a half
// pictures is the stream's own unevenness (a soft pulldown's, a rounded
// timestamp's), not a loss.
func (h *pictureHoles) missing(at, frame time.Duration) int {
	last, seen := h.last, h.seen
	h.last, h.seen = at, true
	if !seen || frame <= 0 {
		return 0
	}
	gap := at - last
	if gap <= frame*3/2 || gap > maxHole {
		return 0
	}
	return int((gap+frame/2)/frame) - 1
}
