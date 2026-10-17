package ocr

import "math"

// CTC (Graves et al.): the network says, per step, how likely each class
// is; a text is any path through the steps that, with repeats merged and
// blanks left out, spells it. The loss is the negative log of all such
// paths' probability, found by the forward-backward recursions over the
// label with blanks around each character.

// logSoftmax turns a step's logits into log probabilities, in place.
func logSoftmax(v []float32) {
	mx := v[0]
	for _, x := range v[1:] {
		mx = max(mx, x)
	}
	var s float64
	for _, x := range v {
		s += math.Exp(float64(x - mx))
	}
	l := float32(math.Log(s)) + mx
	for i := range v {
		v[i] -= l
	}
}

func logAdd(a, b float64) float64 {
	switch {
	case math.IsInf(a, -1):
		return b
	case math.IsInf(b, -1):
		return a
	case a > b:
		return a + math.Log1p(math.Exp(b-a))
	}
	return b + math.Log1p(math.Exp(a-b))
}

// ctcLoss is the loss of a label given the logits (t x classes), and its
// gradient by them; ok is false when the label cannot fit the steps.
func ctcLoss(logits []float32, T int, label []int) (loss float64, grad []float32, ok bool) {
	// The label's minimum length in steps: a step a character, and one
	// more between two the same.
	need := len(label)
	for i := 1; i < len(label); i++ {
		if label[i] == label[i-1] {
			need++
		}
	}
	if need > T {
		return 0, nil, false
	}
	lp := make([]float32, len(logits))
	copy(lp, logits)
	for t := range T {
		logSoftmax(lp[t*classes : (t+1)*classes])
	}
	S := 2*len(label) + 1
	ext := func(s int) int {
		if s%2 == 0 {
			return 0
		}
		return label[s/2]
	}
	inf := math.Inf(-1)
	alpha := make([]float64, T*S)
	beta := make([]float64, T*S)
	for i := range alpha {
		alpha[i], beta[i] = inf, inf
	}
	alpha[0] = float64(lp[0])
	if S > 1 {
		alpha[1] = float64(lp[ext(1)])
	}
	for t := 1; t < T; t++ {
		for s := range S {
			a := alpha[(t-1)*S+s]
			if s > 0 {
				a = logAdd(a, alpha[(t-1)*S+s-1])
			}
			if s > 1 && ext(s) != 0 && ext(s) != ext(s-2) {
				a = logAdd(a, alpha[(t-1)*S+s-2])
			}
			alpha[t*S+s] = a + float64(lp[t*classes+ext(s)])
		}
	}
	beta[(T-1)*S+S-1] = 0
	if S > 1 {
		beta[(T-1)*S+S-2] = 0
	}
	for t := T - 2; t >= 0; t-- {
		for s := range S {
			b := beta[(t+1)*S+s] + float64(lp[(t+1)*classes+ext(s)])
			if s+1 < S {
				b = logAdd(b, beta[(t+1)*S+s+1]+float64(lp[(t+1)*classes+ext(s+1)]))
			}
			if s+2 < S && ext(s+2) != 0 && ext(s+2) != ext(s) {
				b = logAdd(b, beta[(t+1)*S+s+2]+float64(lp[(t+1)*classes+ext(s+2)]))
			}
			beta[t*S+s] = b
		}
	}
	logP := alpha[(T-1)*S+S-1]
	if S > 1 {
		logP = logAdd(logP, alpha[(T-1)*S+S-2])
	}
	if math.IsInf(logP, -1) {
		return 0, nil, false
	}
	// The gradient by the logits: the class's probability less its share
	// of the paths through it.
	grad = make([]float32, len(logits))
	post := make([]float64, classes)
	for t := range T {
		for k := range post {
			post[k] = inf
		}
		for s := range S {
			post[ext(s)] = logAdd(post[ext(s)], alpha[t*S+s]+beta[t*S+s])
		}
		for k := range classes {
			y := math.Exp(float64(lp[t*classes+k]))
			grad[t*classes+k] = float32(y - math.Exp(post[k]-logP))
		}
	}
	return -logP, grad, true
}

// greedy reads the most likely class at each step, repeats merged and
// blanks left out.
func greedy(logits []float32, T int) []int {
	var out []int
	prev := 0
	for t := range T {
		row := logits[t*classes : (t+1)*classes]
		best := 0
		for k, v := range row {
			if v > row[best] {
				best = k
			}
		}
		if best != 0 && best != prev {
			out = append(out, best)
		}
		prev = best
	}
	return out
}
