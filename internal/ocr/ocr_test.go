package ocr

import (
	"bytes"
	"math"
	"math/rand/v2"
	"testing"
)

// CTC's gradient is the loss's slope.
func TestCTCGradient(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	const T = 9
	logits := make([]float32, T*classes)
	for i := range logits {
		logits[i] = float32(rng.NormFloat64())
	}
	label := []int{5, 5, 12}
	loss, grad, ok := ctcLoss(logits, T, label)
	if !ok {
		t.Fatal("no fit")
	}
	for _, i := range []int{0, 5, classes + 5, 3*classes + 12, 4*classes + 0, 8*classes + 12, 2*classes + 7} {
		const h = 1e-2
		old := logits[i]
		logits[i] = old + h
		lp, _, _ := ctcLoss(logits, T, label)
		logits[i] = old - h
		lm, _, _ := ctcLoss(logits, T, label)
		logits[i] = old
		num := (lp - lm) / (2 * h)
		if math.Abs(num-float64(grad[i])) > 1e-3+1e-2*math.Abs(num) {
			t.Errorf("logit %d: gradient %v, numerically %v (loss %v)", i, grad[i], num, loss)
		}
	}
	if _, _, ok := ctcLoss(logits[:2*classes], 2, label); ok {
		t.Error("a label of three (and a repeat) fitted two steps")
	}
}

// The network's gradients are the loss's slopes, at weights here and
// there in every layer.
func TestNetworkGradient(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	m := NewModel(7)
	const w = 24
	img := make([]float32, Height*w)
	for i := range img {
		img[i] = float32(rng.Float64())
	}
	label := []int{3, 9}
	lossOf := func() float64 {
		p := m.forward(img, w)
		l, _, _ := ctcLoss(p.logits, p.t, label)
		return l
	}
	p := m.forward(img, w)
	_, dl, ok := ctcLoss(p.logits, p.t, label)
	if !ok {
		t.Fatal("no fit")
	}
	grad := m.zeroLike()
	m.backward(p, dl, grad)
	ps, gs := m.params(), grad.params()
	for b := range ps {
		for range 6 {
			i := rng.IntN(len(ps[b]))
			const h = 1e-2
			old := ps[b][i]
			ps[b][i] = old + h
			lp := lossOf()
			ps[b][i] = old - h
			lm := lossOf()
			ps[b][i] = old
			num := (lp - lm) / (2 * h)
			if math.Abs(num-float64(gs[b][i])) > 2e-3+5e-2*math.Abs(num) {
				t.Errorf("block %d weight %d: gradient %v, numerically %v", b, i, gs[b][i], num)
			}
		}
	}
}

// Training learns: lines of three made-up glyphs, read back after a few
// hundred steps. Also, weights saved and loaded read the same.
func TestTrainingLearns(t *testing.T) {
	if testing.Short() {
		t.Skip("trains")
	}
	glyph := map[rune][]string{
		'a': {"..##..", ".#..#.", "#....#", "######", "#....#", "#....#"},
		'b': {"#####.", "#....#", "#####.", "#....#", "#....#", "#####."},
		'c': {".####.", "#....#", "#.....", "#.....", "#....#", ".####."},
	}
	line := func(text string) Sample {
		w := (len(text)*10 + 8 + 3) &^ 3
		img := make([]float32, Height*w)
		for i, r := range text {
			for y, row := range glyph[r] {
				for x, c := range row {
					if c == '#' {
						for dy := range 2 {
							for dx := range 1 {
								img[(10+2*y+dy)*w+4+i*10+x+dx] = 1
							}
						}
					}
				}
			}
		}
		return Sample{Img: img, W: w, Text: text}
	}
	var batch []Sample
	for _, s := range []string{"abc", "cab", "bca", "aab", "cc", "ba", "acb", "bbc"} {
		batch = append(batch, line(s))
	}
	tr := NewTrainer(NewModel(1), 3e-3)
	first, _ := tr.Step(batch)
	var last float64
	for range 300 {
		last, _ = tr.Step(batch)
	}
	if last > first/20 {
		t.Errorf("loss %.3f -> %.3f", first, last)
	}
	for _, s := range batch {
		if got := tr.M.Recognise(s.Img, s.W); got != s.Text {
			t.Errorf("read %q as %q", s.Text, got)
		}
	}
	var buf bytes.Buffer
	if err := tr.M.Save(&buf); err != nil {
		t.Fatal(err)
	}
	m, err := Load(&buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range batch {
		if got := m.Recognise(s.Img, s.W); got != s.Text {
			t.Errorf("loaded: read %q as %q", s.Text, got)
		}
	}
}

func TestFix(t *testing.T) {
	for in, want := range map[string]string{
		"GLORlA: lt's me.":   "GLORIA: It's me.",
		"lf so, ln here.":    "If so, In here.",
		"l know.":            "I know.",
		"l'm here, l'll go.": "I'm here, I'll go.",
		"[SlGHS] Hello":      "[SIGHS] Hello",
		"lovely little lamp": "lovely little lamp",
		"Well, l never":      "Well, I never",
	} {
		if got := Fix(in); got != want {
			t.Errorf("Fix(%q) = %q, want %q", in, got, want)
		}
	}
}

// The vector kernels agree with plain sums.
func TestKernels(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for _, n := range []int{1, 15, 16, 17, 48, 100} {
		a, b, y := make([]float32, n), make([]float32, n), make([]float32, n)
		var want float64
		for i := range a {
			a[i], b[i], y[i] = float32(rng.NormFloat64()), float32(rng.NormFloat64()), float32(rng.NormFloat64())
			want += float64(a[i]) * float64(b[i])
		}
		if got := dot(a, b); math.Abs(float64(got)-want) > 1e-4*float64(n) {
			t.Errorf("dot of %d: %v, want %v", n, got, want)
		}
		y0 := append([]float32(nil), y...)
		axpy(y, a, 0.5)
		for i := range y {
			if d := y[i] - (y0[i] + 0.5*a[i]); d > 1e-5 || d < -1e-5 {
				t.Fatalf("axpy of %d at %d: %v, want %v", n, i, y[i], y0[i]+0.5*a[i])
			}
		}
	}
}
