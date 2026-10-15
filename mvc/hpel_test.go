package mvc

import (
	"math/rand"
	"testing"
)

// The half-sample planes give every quarter-sample prediction that the
// filters do, sample for sample, wherever the prediction may reach.
func TestLumaMCPlanesMatchesLumaMC(t *testing.T) {
	p := allocNewPicture(4, 3)
	rng := rand.New(rand.NewSource(1))
	for y := 0; y < p.height; y++ {
		row := p.planes[0][p.origin[0]+y*p.stride[0]:]
		for x := 0; x < p.width; x++ {
			row[x] = byte(rng.Intn(256))
		}
	}
	p.extendRows(0, p.mbH)
	p.halfPel()
	var m mcBuf
	var want, got [256]byte
	for _, w := range []int{4, 8, 16} {
		for _, h := range []int{4, 8, 16} {
			for i := 0; i < 2000; i++ {
				qx := rng.Intn((p.width+2*w+8)*4) - (w+4)*4
				qy := rng.Intn((p.height+2*h+8)*4) - (h+4)*4
				lumaMC(&m, want[:], 16, p, qx, qy, w, h)
				lumaMCPlanes(got[:], 16, p, qx, qy, w, h)
				if want != got {
					t.Fatalf("%dx%d at (%d,%d): the planes' prediction differs", w, h, qx, qy)
				}
			}
		}
	}
}

// satdAt reads the planes as lumaMCPlanes would and scores the same
// prediction, and the averaging kernel agrees with the plain code.
func TestSATDAtMatchesPrediction(t *testing.T) {
	p := allocNewPicture(4, 3)
	rng := rand.New(rand.NewSource(3))
	for y := 0; y < p.height; y++ {
		row := p.planes[0][p.origin[0]+y*p.stride[0]:]
		for x := 0; x < p.width; x++ {
			row[x] = byte(rng.Intn(256))
		}
	}
	p.extendRows(0, p.mbH)
	p.halfPel()
	c := newDepCoder(p.mbW, p.mbH, 24)
	c.src = DepSource{Y: make([]byte, p.width*p.height), StrideY: p.width}
	for i := range c.src.Y {
		c.src.Y[i] = byte(rng.Intn(256))
	}
	var pred [256]byte
	for i := 0; i < 3000; i++ {
		pw, ph := []int{16, 16, 8}[i%3], []int{16, 8, 16}[i%3]
		px, py := rng.Intn(p.width/pw)*pw, rng.Intn(p.height/ph)*ph
		qx := rng.Intn((p.width+2*pw+8)*4) - (pw+4)*4
		qy := rng.Intn((p.height+2*ph+8)*4) - (ph+4)*4
		lumaMCPlanes(pred[:], 16, p, qx, qy, pw, ph)
		want := c.satd(px, py, pw, ph, pred[:])
		if got := c.satdAt(p, px, py, pw, ph, qx, qy); got != want {
			t.Fatalf("%dx%d at (%d,%d) from (%d,%d): satdAt %d, want %d", pw, ph, px, py, qx, qy, got, want)
		}
	}
}
