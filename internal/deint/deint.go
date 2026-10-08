// Package deint deinterlaces 8-bit 4:2:0 pictures, one frame out for each
// frame in (the field shown first kept, the other interpolated), as
// NVDEC's adaptive deinterlacer and ffmpeg's bwdif do for the other
// decoders here.
//
// The interpolation is yadif's (Michael Niedermayer's "yet another
// deinterlacing filter"), written from the algorithm: each missing line's
// sample is the temporal average of the frames around it, kept within how
// much the picture moves there, and replaced by an edge-directed spatial
// interpolation where that is nearer: still areas keep their full
// resolution, moving ones lose no sharpness to combing.
package deint

// Frame is a picture's planes.
type Frame struct {
	Planes  [3][]byte // Y, Cb, Cr
	Strides [3]int
	Width   int // of luma
	Height  int
	// Interlaced frames are deinterlaced; others pass as they are.
	Interlaced    bool
	TopFieldFirst bool
	Tag           any // the caller's, carried to the output
}

// Deinterlacer holds the frame before and after the one it outputs.
type Deinterlacer struct {
	prev, cur, next *Frame
	out             func(*Frame) error
}

// New makes a deinterlacer giving its frames to out (valid during the call).
func New(out func(*Frame) error) *Deinterlacer { return &Deinterlacer{out: out} }

// Push takes the next frame (which must stay valid until the next Push or
// Flush returns), giving out the one before it.
func (d *Deinterlacer) Push(f *Frame) error {
	d.prev, d.cur, d.next = d.cur, d.next, f
	if d.cur == nil {
		return nil
	}
	return d.emit()
}

// Flush gives out the last frame.
func (d *Deinterlacer) Flush() error {
	if d.next == nil {
		return nil
	}
	d.prev, d.cur, d.next = d.cur, d.next, nil
	err := d.emit()
	d.prev, d.cur = nil, nil
	return err
}

func (d *Deinterlacer) emit() error {
	c := d.cur
	if !c.Interlaced {
		return d.out(c)
	}
	prev, next := d.prev, d.next
	if prev == nil {
		prev = c
	}
	if next == nil {
		next = c
	}
	out := &Frame{Width: c.Width, Height: c.Height, Tag: c.Tag, TopFieldFirst: c.TopFieldFirst}
	// The field to interpolate: the second one shown.
	parity := 1
	if !c.TopFieldFirst {
		parity = 0
	}
	for p := range 3 {
		w, h := c.Width, c.Height
		if p > 0 {
			w, h = (w+1)/2, (h+1)/2
		}
		stride := c.Strides[p]
		dst := make([]byte, stride*h)
		filterPlane(dst, prev.Planes[p], c.Planes[p], next.Planes[p], stride, w, h, parity)
		out.Planes[p], out.Strides[p] = dst, stride
	}
	return d.out(out)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// filterPlane fills dst: the kept field's lines copied, the others (those
// of parity) interpolated.
func filterPlane(dst, prev, cur, next []byte, stride, w, h, parity int) {
	for y := range h {
		row := y * stride
		if y&1 != parity || y < 1 || y >= h-1 {
			if y&1 != parity {
				copy(dst[row:row+w], cur[row:row+w])
			} else {
				// The first or last line: its neighbour within the frame.
				src := row - stride
				if y == 0 {
					src = row + stride
				}
				copy(dst[row:row+w], cur[src:src+w])
			}
			continue
		}
		// The frames on each side of the missing lines' moment: the kept
		// field is the first shown, and the other parity's fields around
		// it are the previous frame's and this one's.
		prev2, next2 := prev, cur
		up, down := row-stride, row+stride
		for x := range w {
			c, e := int(cur[up+x]), int(cur[down+x])
			dTemp := (int(prev2[row+x]) + int(next2[row+x])) >> 1
			td0 := abs(int(prev2[row+x]) - int(next2[row+x]))
			td1 := (abs(int(prev[up+x])-c) + abs(int(prev[down+x])-e)) >> 1
			td2 := (abs(int(next[up+x])-c) + abs(int(next[down+x])-e)) >> 1
			diff := max(td0>>1, td1, td2)
			spatial := (c + e) >> 1
			if x >= 3 && x < w-3 {
				score := abs(int(cur[up+x-1])-int(cur[down+x-1])) + abs(c-e) + abs(int(cur[up+x+1])-int(cur[down+x+1])) - 1
				// Edge directions, one sample, then two, each way.
				for _, dir := range [2]int{-1, 1} {
					for k := 1; k <= 2; k++ {
						j := dir * k
						s := abs(int(cur[up+x+j-1])-int(cur[down+x-j-1])) +
							abs(int(cur[up+x+j])-int(cur[down+x-j])) +
							abs(int(cur[up+x+j+1])-int(cur[down+x-j+1]))
						if s >= score {
							break
						}
						score = s
						spatial = (int(cur[up+x+j]) + int(cur[down+x-j])) >> 1
					}
				}
			}
			if y >= 2 && y < h-2 {
				b := (int(prev2[row-2*stride+x]) + int(next2[row-2*stride+x])) >> 1
				f := (int(prev2[row+2*stride+x]) + int(next2[row+2*stride+x])) >> 1
				hi := max(dTemp-e, dTemp-c, min(b-c, f-e))
				lo := min(dTemp-e, dTemp-c, max(b-c, f-e))
				diff = max(diff, lo, -hi)
			}
			if spatial > dTemp+diff {
				spatial = dTemp + diff
			} else if spatial < dTemp-diff {
				spatial = dTemp - diff
			}
			dst[row+x] = uint8(min(max(spatial, 0), 255))
		}
	}
}
