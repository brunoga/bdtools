package ocr

import (
	"math"
	"math/rand/v2"
)

// The network: a line's picture, Height rows of any width (a multiple of
// 4), in; per 4 columns, how likely each class is, out.
//
//	conv 3x3  1->16, ReLU, max pool 2x2   32 x W   -> 16 x W/2
//	conv 3x3 16->32, ReLU, max pool 2x2   16 x W/2 ->  8 x W/4
//	conv 3x3 32->64, ReLU, max pool 2x1    8 x W/4 ->  4 x W/4
//	conv 3x3 64->64, ReLU, max pool 2x1    4 x W/4 ->  2 x W/4
//	a column (64 x 2 = 128 values) a step: a bidirectional LSTM, 96 each
//	way, then a linear layer to the classes
const (
	Height = 32
	hidden = 96
)

// convSpec is a convolution layer's shape and the pooling after it.
type convSpec struct{ in, out, ph, pw int }

var convs = []convSpec{{1, 16, 2, 2}, {16, 32, 2, 2}, {32, 64, 2, 1}, {64, 64, 2, 1}}

// features is the LSTM's input size: the last layer's channels by its rows.
const features = 64 * 2

// Model is the network's weights.
type Model struct {
	// Conv[i] holds layer i's weights (out x in*9), then its biases.
	Conv [][]float32
	// LSTM[d] is a direction's (0 forward, 1 backward): the input weights
	// (4*hidden x features), the recurrent ones (4*hidden x hidden), the
	// biases (4*hidden); the gates in the order i, f, g, o.
	LSTM [2][]float32
	// Out is the linear layer: weights (classes x 2*hidden), then biases.
	Out []float32
}

// sizes of each parameter block.
func convSize(c convSpec) int { return c.out*c.in*9 + c.out }

const lstmSize = 4*hidden*features + 4*hidden*hidden + 4*hidden

func outSize() int { return classes*2*hidden + classes }

// NewModel is a model with random weights (He's for the convolutions,
// Glorot's for the rest; the LSTM's forget gates biased open).
func NewModel(seed uint64) *Model {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // weights, not secrets
	m := &Model{}
	fill := func(w []float32, scale float64) {
		for i := range w {
			w[i] = float32(rng.NormFloat64() * scale)
		}
	}
	for _, c := range convs {
		w := make([]float32, convSize(c))
		fill(w[:c.out*c.in*9], math.Sqrt(2/float64(c.in*9)))
		m.Conv = append(m.Conv, w)
	}
	for d := range 2 {
		w := make([]float32, lstmSize)
		fill(w[:4*hidden*features], math.Sqrt(1/float64(features)))
		fill(w[4*hidden*features:4*hidden*(features+hidden)], math.Sqrt(1/float64(hidden)))
		b := w[4*hidden*(features+hidden):]
		for j := hidden; j < 2*hidden; j++ {
			b[j] = 1
		}
		m.LSTM[d] = w
	}
	m.Out = make([]float32, outSize())
	fill(m.Out[:classes*2*hidden], math.Sqrt(1/float64(2*hidden)))
	return m
}

// params lists the model's parameter blocks, in a fixed order.
func (m *Model) params() [][]float32 {
	p := append([][]float32(nil), m.Conv...)
	return append(p, m.LSTM[0], m.LSTM[1], m.Out)
}

// zeroLike is a model of the same shape, all zeros: gradients.
func (m *Model) zeroLike() *Model {
	z := &Model{}
	for _, c := range m.Conv {
		z.Conv = append(z.Conv, make([]float32, len(c)))
	}
	for d := range 2 {
		z.LSTM[d] = make([]float32, len(m.LSTM[d]))
	}
	z.Out = make([]float32, len(m.Out))
	return z
}

// gemm adds a (m x k) times b (k x n) to c (m x n).
func gemm(c, a, b []float32, m, k, n int) {
	for i := range m {
		ci := c[i*n : (i+1)*n]
		for p, av := range a[i*k : (i+1)*k] {
			if av == 0 {
				continue
			}
			axpy(ci, b[p*n:(p+1)*n], av)
		}
	}
}

// gemmTN adds a transposed (a is k x m) times b (k x n) to c (m x n).
func gemmTN(c, a, b []float32, m, k, n int) {
	for p := range k {
		bp := b[p*n : (p+1)*n]
		for i, av := range a[p*m : (p+1)*m] {
			if av == 0 {
				continue
			}
			axpy(c[i*n:(i+1)*n], bp, av)
		}
	}
}

// gemmNT adds a (m x k) times b transposed (b is n x k) to c (m x n).
func gemmNT(c, a, b []float32, m, k, n int) {
	for i := range m {
		ai := a[i*k : (i+1)*k]
		ci := c[i*n : (i+1)*n]
		for j := range n {
			ci[j] += dot(ai, b[j*k:(j+1)*k])
		}
	}
}

func sigmoid(x float32) float32 { return 1 / (1 + float32(math.Exp(float64(-x)))) }

func tanh(x float32) float32 { return float32(math.Tanh(float64(x))) }
