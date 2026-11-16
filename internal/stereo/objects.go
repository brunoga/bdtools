package stereo

import "github.com/brunoga/bdtools/internal/nn"

// Objects graded by hand: an object painted on a picture and followed
// through its shot (internal/sam) has its own mask on each picture, and
// its disparity is moved by its push, in front of the scene or behind it.

// ObjectMask is an object on a picture: its mask (ObjectSide x ObjectSide
// bytes, 0 to 255, over the whole picture stretched to a square) and how
// far its disparity is moved, a fraction of the picture's width (more is
// nearer).
type ObjectMask struct {
	Mask []byte
	Push float64
}

// ObjectSide is an ObjectMask's side.
const ObjectSide = 256

// objectBytes is a picture's objects as nn.PushObjects takes them, their
// pushes in pixels of a picture w wide.
func objectBytes(objs []ObjectMask, w int) []byte {
	masks := make([][]byte, len(objs))
	pushes := make([]float32, len(objs))
	for i, o := range objs {
		masks[i], pushes[i] = o.Mask, float32(o.Push*float64(w))
	}
	return nn.ObjectsBytes(masks, pushes)
}

// pushObjects is nn.GPU.PushObjects on the CPU: d is the disparity of the
// active rows [top, bottom) of a w x h picture (d's first row is top's).
func pushObjects(objs []ObjectMask, d []float32, w, h, top, bottom int) {
	if len(objs) == 0 {
		return
	}
	const s = ObjectSide
	rows(bottom-top, func(r int) {
		y := top + r
		fy := max((float32(y)+0.5)*s/float32(h)-0.5, 0)
		y0 := min(int(fy), s-1)
		y1 := min(y0+1, s-1)
		ly := fy - float32(y0)
		for x := range w {
			fx := max((float32(x)+0.5)*s/float32(w)-0.5, 0)
			x0 := min(int(fx), s-1)
			x1 := min(x0+1, s-1)
			lx := fx - float32(x0)
			var add float32
			for _, o := range objs {
				m := o.Mask
				a := float32(m[y0*s+x0])*(1-lx) + float32(m[y0*s+x1])*lx
				b := float32(m[y1*s+x0])*(1-lx) + float32(m[y1*s+x1])*lx
				add += (a*(1-ly) + b*ly) / 255 * float32(o.Push*float64(w))
			}
			d[r*w+x] += add
		}
	})
}
