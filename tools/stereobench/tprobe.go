package main

import (
	"fmt"
	"math"

	"github.com/brunoga/bdtools/internal/stereo"
	"github.com/brunoga/bdtools/mvc"
)

// The temporal fill probe (-tfill-probe): how much of what a hole hides
// the pictures around show, by the disc's own depth. Each measured
// picture's right eye rendered from its left, its holes looked up in the
// pictures around rendered alike, moved by the background's motion
// between them, where that picture's pixel is seen and as far as the
// hole's; against the disc's right eye, beside Render's stretch.

var tprobeOn bool

var tprobeKs = []int{1, -1, 2, -2, 4, -4, 8, -8}

const tprobeR = 8

type tprobeFrame struct {
	left, right *mvc.Frame
	d           []float32
	k           int
}

type tprobeStats struct {
	holes, filled float64
	seTemp, seStr float64
	seAll         float64 // the stretch over all the holes
	byK           map[int]float64
	// by the hole's run's width: <4, 4-12, >12
	wN, wTemp, wStr [3]float64
	// the pictures around moved onto the picture's visible far pixels
	mcSE, mcN float64
	frames    int
	ring      []tprobeFrame
}

var tprobe = &tprobeStats{byK: map[int]float64{}}

// tprobeFrame takes a picture; the one tprobeR before is measured when its
// number is one the bench measures.
func (s *tprobeStats) frame(left, right *mvc.Frame, dm *stereo.Disparity, k, every int) {
	s.ring = append(s.ring, tprobeFrame{left, right, stereo.Dense(dm, left.Width, left.Height), k})
	if len(s.ring) > 2*tprobeR+1 {
		s.ring = s.ring[1:]
	}
	if len(s.ring) < 2*tprobeR+1 || s.ring[tprobeR].k%every != 0 {
		return
	}
	s.measure()
}

// warped is a disparity as the eye it is rendered for sees it (the holes
// the farther side's, as Render fills them), a quarter pixel's precision.
func warped(f *mvc.Frame, d []float32) []float32 {
	w, h := f.Width, f.Height
	z := &mvc.Frame{Width: w, Height: h, StrideY: w, StrideC: (w + 1) / 2,
		Y: make([]byte, w*h), Cb: make([]byte, ((w+1)/2)*((h+1)/2)), Cr: make([]byte, ((w+1)/2)*((h+1)/2))}
	for i, v := range d {
		z.Y[i] = byte(min(max(4*v+128, 0), 255))
	}
	e, _ := stereo.Render(z, d, -1)
	out := make([]float32, w*h)
	for i, v := range e.Y[:w*h] {
		out[i] = (float32(v) - 128) / 4
	}
	return out
}

// motion is the background's integer motion from a to b (b(p+m) ~ a(p)),
// over the pixels seen in a and farther than its median: searched at a
// quarter size, then refined.
func motion(a, b *mvc.Frame, ha []bool, za []float32) (int, int) {
	w, h := a.Width, a.Height
	far := make([]float32, 0, w*h/16)
	for y := 0; y < h; y += 4 {
		for x := 0; x < w; x += 4 {
			if !ha[y*w+x] {
				far = append(far, za[y*w+x])
			}
		}
	}
	if len(far) == 0 {
		return 0, 0
	}
	med := median(far)
	cost := func(dx, dy, step int) float64 {
		var c, n float64
		for y := 32; y < h-32; y += step {
			for x := 32; x < w-32; x += step {
				i := y*w + x
				if ha[i] || za[i] > med {
					continue
				}
				X, Y := x+dx, y+dy
				if X < 0 || X >= w || Y < 0 || Y >= h {
					continue
				}
				c += math.Abs(float64(a.Y[y*a.StrideY+x]) - float64(b.Y[Y*b.StrideY+X]))
				n++
			}
		}
		if n < 100 {
			return math.Inf(1)
		}
		return c / n
	}
	bx, by, best := 0, 0, math.Inf(1)
	for dy := -24; dy <= 24; dy += 4 {
		for dx := -48; dx <= 48; dx += 4 {
			if c := cost(dx, dy, 8); c < best {
				bx, by, best = dx, dy, c
			}
		}
	}
	cx, cy := bx, by
	best = math.Inf(1)
	for dy := cy - 3; dy <= cy+3; dy++ {
		for dx := cx - 3; dx <= cx+3; dx++ {
			if c := cost(dx, dy, 4); c < best {
				bx, by, best = dx, dy, c
			}
		}
	}
	return bx, by
}

func (s *tprobeStats) measure() {
	mid := s.ring[tprobeR]
	w, h := mid.left.Width, mid.left.Height
	eye, holes := stereo.Render(mid.left, mid.d, -1)
	z := warped(mid.left, mid.d)
	top, bottom := stereo.Bars(mid.left)
	fill := make([]int, w*h) // 0: none, else 1 + the value
	from := make([]int, w*h)
	for _, k := range tprobeKs {
		o := s.ring[tprobeR+k]
		ek, hk := stereo.Render(o.left, o.d, -1)
		zk := warped(o.left, o.d)
		dx, dy := motion(eye, ek, holes, z)
		if k == 1 {
			for y := top + 32; y < bottom-32; y += 2 {
				for x := 32; x < w-32; x += 2 {
					i := y*w + x
					X, Y := x+dx, y+dy
					if holes[i] || X < 0 || X >= w || Y < 0 || Y >= h || hk[Y*w+X] {
						continue
					}
					e := float64(eye.Y[y*eye.StrideY+x]) - float64(ek.Y[Y*ek.StrideY+X])
					s.mcSE += e * e
					s.mcN++
				}
			}
		}
		for y := top; y < bottom; y++ {
			for x := range w {
				i := y*w + x
				if !holes[i] || fill[i] != 0 {
					continue
				}
				X, Y := x+dx, y+dy
				if X < 0 || X >= w || Y < top || Y >= bottom {
					continue
				}
				j := Y*w + X
				if hk[j] || math.Abs(float64(zk[j]-z[i])) > 1.5 {
					continue
				}
				fill[i], from[i] = 1+int(ek.Y[Y*ek.StrideY+X]), k
			}
		}
	}
	run := make([]int, w*h)
	for y := top; y < bottom; y++ {
		for x := 0; x < w; {
			if !holes[y*w+x] {
				x++
				continue
			}
			a := x
			for x < w && holes[y*w+x] {
				x++
			}
			for X := a; X < x; X++ {
				run[y*w+X] = x - a
			}
		}
	}
	for y := top; y < bottom; y++ {
		for x := range w {
			i := y*w + x
			if !holes[i] {
				continue
			}
			want := float64(mid.right.Y[y*mid.right.StrideY+x])
			es := float64(eye.Y[y*eye.StrideY+x]) - want
			s.holes++
			s.seAll += es * es
			if fill[i] == 0 {
				continue
			}
			et := float64(fill[i]-1) - want
			s.filled++
			s.seTemp += et * et
			s.seStr += es * es
			s.byK[from[i]]++
			b := 0
			if run[i] >= 4 {
				b = 1
			}
			if run[i] > 12 {
				b = 2
			}
			s.wN[b]++
			s.wTemp[b] += et * et
			s.wStr[b] += es * es
		}
	}
	s.frames++
}

func (s *tprobeStats) report() {
	if s.frames == 0 {
		fmt.Println("tfill probe: no pictures")
		return
	}
	db := func(se, n float64) float64 { return psnr(se / max(n, 1)) }
	fmt.Printf("tfill probe: %d pictures; holes filled from the pictures around %.1f%%\n", s.frames, 100*s.filled/max(s.holes, 1))
	fmt.Printf("  those: %.2f dB temporal, %.2f dB stretched; all holes stretched %.2f dB, with the temporal ones %.2f dB\n",
		db(s.seTemp, s.filled), db(s.seStr, s.filled), db(s.seAll, s.holes), db(s.seAll-s.seStr+s.seTemp, s.holes))
	fmt.Printf("  the next picture moved onto the visible far pixels: %.2f dB\n", db(s.mcSE, s.mcN))
	for b, name := range []string{"<4", "4-12", ">12"} {
		fmt.Printf("  holes %s wide: %.1f%% of those filled, %.2f dB temporal, %.2f stretched\n", name, 100*s.wN[b]/max(s.filled, 1), db(s.wTemp[b], s.wN[b]), db(s.wStr[b], s.wN[b]))
	}
	for _, k := range tprobeKs {
		fmt.Printf("  from %+d: %.1f%%\n", k, 100*s.byK[k]/max(s.filled, 1))
	}
}
