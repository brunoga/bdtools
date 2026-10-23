package stereo

// Samples is what of a picture's disparity the floating window and the
// subtitles read: its side strips (sampleEdge wide) on every other active
// row, and every fourth row and column of its lower half. A GPU downloads
// these and not the whole of it.
type Samples struct {
	Left, Right []float32 // the strips, a row's after the other's
	Low         []float32 // the lower half's
}

// sampleEdge is the width of the strips the floating window looks at in a
// picture w wide.
func sampleEdge(w int) int { return max(1, int(windowEdge*float64(w))) }

// SamplesOf is the Samples of a w x h disparity d whose active rows are
// [top, bottom).
func SamplesOf(d []float32, w, h, top, bottom int) *Samples {
	edge := sampleEdge(w)
	s := &Samples{}
	for y := top; y < bottom; y += 2 {
		s.Left = append(s.Left, d[y*w:y*w+edge]...)
		s.Right = append(s.Right, d[y*w+w-edge:(y+1)*w]...)
	}
	for y := h / 2; y < h; y += 4 {
		for x := 0; x < w; x += 4 {
			s.Low = append(s.Low, d[y*w+x])
		}
	}
	return s
}

// samplesSplit is the Samples in buf, nn.GPU.Samples' layout.
func samplesSplit(buf []float32, w, top, bottom int) *Samples {
	edge := sampleEdge(w)
	n := (bottom - top + 1) / 2 * edge
	return &Samples{Left: buf[:n:n], Right: buf[n : 2*n : 2*n], Low: buf[2*n:]}
}
