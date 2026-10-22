// Command depthtrain learns the depth refinement (internal/stereo's
// RefineWeights) from 3D discs' pairs: stereobench -samples files, each a
// picture's left eye, the depth network's depth of it and the disc's
// disparity, all at half size.
//
//	depthtrain -train DIR -val DIR -out FILE [-epochs N] [-lr R]
//
// The loss is what is left of the disc's disparity after fitting it, a
// picture at a time, by a scale and an offset of the refined depth (as
// stereobench's "fitted" measure): the films' own grading, which the
// pictures do not tell, is left out, and the depth's shape is learnt.
// Each epoch it reports the validation pictures' error after that fit
// (in pixels at the picture's size), the depth's as the network gives it
// and refined, and writes the weights.
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/brunoga/bdtools/internal/nn"
	"github.com/brunoga/bdtools/internal/stereo"
)

func main() {
	trainDir := flag.String("train", "", "the training samples' directory")
	valDir := flag.String("val", "", "the validation samples' directory")
	out := flag.String("out", "refine.bin", "the weights' file")
	epochs := flag.Int("epochs", 10, "passes over the training samples")
	lr := flag.Float64("lr", 1e-3, "Adam's learning rate (decayed to a tenth by the end)")
	batch := flag.Int("batch", 4, "pictures a step")
	from := flag.String("from", "", "start from these weights")
	limit := flag.Int("limit", 0, "use at most this many training samples (0: all)")
	flag.Parse()
	train, err := list(*trainDir)
	if err != nil {
		fail(err)
	}
	val, err := list(*valDir)
	if err != nil {
		fail(err)
	}
	r := rand.New(rand.NewPCG(1, 1)) //nolint:gosec // shuffling and initialising, repeatably
	r.Shuffle(len(train), func(i, j int) { train[i], train[j] = train[j], train[i] })
	if *limit > 0 && *limit < len(train) {
		train = train[:*limit]
	}
	var m *model
	if *from != "" {
		w, err := stereo.ReadRefine(*from)
		if err != nil {
			fail(err)
		}
		m = &model{RefineWeights: *w}
	} else {
		m = newModel(r)
	}
	opt := newAdam(m)
	fmt.Printf("%d training pictures, %d validation\n", len(train), len(val))
	base, refined := validate(m, val)
	fmt.Printf("epoch 0: validation error after fitting %.3f px as the network gives it, %.3f refined\n", base, refined)
	steps := *epochs * ((len(train) + *batch - 1) / *batch)
	step := 0
	for e := 1; e <= *epochs; e++ {
		r.Shuffle(len(train), func(i, j int) { train[i], train[j] = train[j], train[i] })
		t0 := time.Now()
		var lossSum float64
		var n int
		samples := prefetch(train)
		for b := 0; b < len(train); b += *batch {
			m.zeroGrad()
			// the batch's pictures at once, each to its own gradients
			var batchOf []*sample
			for range min(*batch, len(train)-b) {
				if s := <-samples; s != nil {
					if r.IntN(2) == 1 {
						s.flip()
					}
					batchOf = append(batchOf, s)
				}
			}
			views := make([]*model, len(batchOf))
			losses := make([]float64, len(batchOf))
			oks := make([]bool, len(batchOf))
			var wg sync.WaitGroup
			for i, s := range batchOf {
				views[i] = &model{RefineWeights: m.RefineWeights}
				views[i].zeroGrad()
				wg.Go(func() { losses[i], oks[i] = views[i].step(s) })
			}
			wg.Wait()
			k := 0
			for i, v := range views {
				if !oks[i] {
					continue
				}
				lossSum += losses[i]
				n++
				k++
				for _, l := range stereo.RefineLayers {
					acc(m.gw[l.Name], v.gw[l.Name])
					acc(m.gb[l.Name], v.gb[l.Name])
				}
			}
			if k == 0 {
				continue
			}
			// the rate decayed along a cosine to a tenth
			rate := *lr * (0.1 + 0.9*0.5*(1+math.Cos(math.Pi*float64(step)/float64(steps))))
			opt.update(m, rate, float32(1/float64(k)))
			step++
			if step%100 == 0 {
				fmt.Printf("  step %d: loss %.4f (%.2f s a picture)\n", step, lossSum/float64(max(n, 1)), time.Since(t0).Seconds()/float64(max(n, 1)))
			}
		}
		base, refined := validate(m, val)
		fmt.Printf("epoch %d: loss %.4f; validation error after fitting %.3f px as the network gives it, %.3f refined (%v)\n",
			e, lossSum/float64(max(n, 1)), base, refined, time.Since(t0).Round(time.Second))
		if err := save(m, *out); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "depthtrain:", err)
	os.Exit(1)
}

func list(dir string) ([]string, error) {
	if dir == "" {
		return nil, errors.New("no samples' directory")
	}
	f, err := filepath.Glob(filepath.Join(dir, "*.bds"))
	if err == nil && len(f) == 0 {
		err = fmt.Errorf("%s: no samples", dir)
	}
	return f, err
}

func save(m *model, path string) error {
	f, err := os.Create(path + ".tmp") //nolint:gosec // the operator's file
	if err != nil {
		return err
	}
	_, err = m.WriteTo(f)
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// sample is a stereobench -samples file's picture, its active rows only.
type sample struct {
	w, h        int
	y, cb, cr   []byte
	depth, disp []float32 // disp NaN where unsure
}

func read(path string) (*sample, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's files
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var hdr struct {
		Magic             [4]byte
		W, H, Top, Bottom uint32
	}
	if err := binary.Read(f, binary.LittleEndian, &hdr); err != nil || string(hdr.Magic[:]) != "BDS1" {
		return nil, fmt.Errorf("%s: not a sample", path)
	}
	w, h := int(hdr.W), int(hdr.H)
	top, bottom := int(hdr.Top), int(hdr.Bottom)
	planes := make([]byte, 3*w*h)
	if _, err := io.ReadFull(f, planes); err != nil {
		return nil, err
	}
	maps := make([]float32, 2*w*h)
	if err := binary.Read(f, binary.LittleEndian, maps); err != nil {
		return nil, err
	}
	s := &sample{w: w, h: bottom - top}
	s.y = planes[top*w : bottom*w]
	s.cb = planes[w*h+top*w : w*h+bottom*w]
	s.cr = planes[2*w*h+top*w : 2*w*h+bottom*w]
	s.depth = maps[top*w : bottom*w]
	s.disp = maps[w*h+top*w : w*h+bottom*w]
	if s.h < 16 || slices.ContainsFunc(s.depth, func(v float32) bool { return math.IsNaN(float64(v)) }) {
		return nil, fmt.Errorf("%s: no active rows", path)
	}
	return s, nil
}

// flip mirrors the sample left to right.
func (s *sample) flip() {
	for r := range s.h {
		slices.Reverse(s.y[r*s.w : (r+1)*s.w])
		slices.Reverse(s.cb[r*s.w : (r+1)*s.w])
		slices.Reverse(s.cr[r*s.w : (r+1)*s.w])
		slices.Reverse(s.depth[r*s.w : (r+1)*s.w])
		slices.Reverse(s.disp[r*s.w : (r+1)*s.w])
	}
}

// prefetch reads the samples ahead, in order (nil for one unreadable).
func prefetch(paths []string) <-chan *sample {
	ch := make(chan *sample, 4)
	go func() {
		for _, p := range paths {
			s, err := read(p)
			if err != nil {
				fmt.Fprintln(os.Stderr, "depthtrain:", err)
			}
			ch <- s
		}
		close(ch)
	}()
	return ch
}

// fit is the disc's disparity fitted by the depth z (padded pw wide) over
// its sure pixels: the scale, the offset and the count.
func fit(s *sample, z []float32, pw int) (a, b float64, n int) {
	var sx, sy, sxx, sxy float64
	for r := range s.h {
		for c := range s.w {
			d := s.disp[r*s.w+c]
			if math.IsNaN(float64(d)) {
				continue
			}
			x := float64(z[r*pw+c])
			sx += x
			sy += float64(d)
			sxx += x * x
			sxy += x * float64(d)
			n++
		}
	}
	if n < 100 {
		return 0, 0, n
	}
	fn := float64(n)
	a = (sxy - sx*sy/fn) / max(sxx-sx*sx/fn, 1e-9)
	b = (sy - a*sx) / fn
	return a, b, n
}

// fitError is the mean error left after fitting, in pixels at the
// picture's size (the samples' are at half).
func fitError(s *sample, z []float32, pw int) (float64, bool) {
	a, b, n := fit(s, z, pw)
	if n < 100 {
		return 0, false
	}
	var e float64
	for r := range s.h {
		for c := range s.w {
			if d := s.disp[r*s.w+c]; !math.IsNaN(float64(d)) {
				e += math.Abs(a*float64(z[r*pw+c]) + b - float64(d))
			}
		}
	}
	return 2 * e / float64(n), true
}

func validate(m *model, paths []string) (base, refined float64) {
	var n int
	for s := range prefetch(paths) {
		if s == nil {
			continue
		}
		x, _, _ := stereo.RefineInput(s.y, s.cb, s.cr, s.w, s.depth, s.w, s.h)
		pw := x.Dim(2)
		z := x.Data[3*x.Dim(1)*pw:]
		e0, ok := fitError(s, z, pw)
		if !ok {
			continue
		}
		y := m.Refine(nn.CPU, x)
		e1, _ := fitError(s, y.Data, pw)
		base += e0
		refined += e1
		n++
	}
	return base / float64(max(n, 1)), refined / float64(max(n, 1))
}
