package ocr

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"runtime"
	"sync"
)

// Sample is a line to learn from: its picture (Height x W, values 0 to 1,
// the text bright on dark) and its text.
type Sample struct {
	Img  []float32
	W    int
	Text string
}

// Trainer fits a model to samples with Adam.
type Trainer struct {
	M        *Model
	LR       float64
	m, v     *Model
	step     int
	ClipNorm float64 // the gradient's largest norm, 0 for none
}

// NewTrainer starts training m.
func NewTrainer(m *Model, lr float64) *Trainer {
	return &Trainer{M: m, LR: lr, m: m.zeroLike(), v: m.zeroLike(), ClipNorm: 5}
}

// Step learns from a batch, returning its mean loss and how many samples
// counted (a text too long for its picture does not).
func (tr *Trainer) Step(batch []Sample) (float64, int) {
	workers := min(runtime.GOMAXPROCS(0), len(batch))
	grads := make([]*Model, workers)
	losses := make([]float64, workers)
	counts := make([]int, workers)
	var wg sync.WaitGroup
	for k := range workers {
		grads[k] = tr.M.zeroLike()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := k; i < len(batch); i += workers {
				s := batch[i]
				label, _ := Encode(s.Text)
				p := tr.M.forward(s.Img, s.W)
				loss, dl, ok := ctcLoss(p.logits, p.t, label)
				if !ok || math.IsNaN(loss) {
					continue
				}
				tr.M.backward(p, dl, grads[k])
				losses[k] += loss
				counts[k]++
			}
		}()
	}
	wg.Wait()
	g := grads[0]
	total, n := losses[0], counts[0]
	for k := 1; k < workers; k++ {
		for b, blk := range g.params() {
			axpy(blk, grads[k].params()[b], 1)
		}
		total += losses[k]
		n += counts[k]
	}
	if n == 0 {
		return 0, 0
	}
	scale := 1 / float64(n)
	var norm float64
	for _, blk := range g.params() {
		for i := range blk {
			blk[i] *= float32(scale)
			norm += float64(blk[i]) * float64(blk[i])
		}
	}
	if norm = math.Sqrt(norm); tr.ClipNorm > 0 && norm > tr.ClipNorm {
		f := float32(tr.ClipNorm / norm)
		for _, blk := range g.params() {
			for i := range blk {
				blk[i] *= f
			}
		}
	}
	tr.adam(g)
	return total / float64(n), n
}

// adam moves the weights by the gradient (Kingma and Ba).
func (tr *Trainer) adam(g *Model) {
	const b1, b2, eps = 0.9, 0.999, 1e-8
	tr.step++
	c1 := 1 - math.Pow(b1, float64(tr.step))
	c2 := 1 - math.Pow(b2, float64(tr.step))
	lr := float32(tr.LR * math.Sqrt(c2) / c1)
	ps, gs, ms, vs := tr.M.params(), g.params(), tr.m.params(), tr.v.params()
	for b := range ps {
		p, gg, m, v := ps[b], gs[b], ms[b], vs[b]
		for i := range p {
			m[i] = b1*m[i] + (1-b1)*gg[i]
			v[i] = b2*v[i] + (1-b2)*gg[i]*gg[i]
			p[i] -= lr * m[i] / (float32(math.Sqrt(float64(v[i]))) + eps)
		}
	}
}

// Recognise reads a line's picture.
func (m *Model) Recognise(img []float32, w int) string {
	p := m.forward(img, w)
	return Decode(greedy(p.logits, p.t))
}

// Loss is the loss of a sample, for measuring; ok is false when the text
// cannot fit.
func (m *Model) Loss(s Sample) (float64, bool) {
	label, _ := Encode(s.Text)
	p := m.forward(s.Img, s.W)
	l, _, ok := ctcLoss(p.logits, p.t, label)
	return l, ok
}

// modelMagic starts a saved model: the format and the charset's size.
const modelMagic = "bdocr1"

// Save writes the weights as half-precision floats.
func (m *Model) Save(w io.Writer) error {
	hdr := []byte(modelMagic)
	hdr = binary.BigEndian.AppendUint32(hdr, uint32(classes)) //nolint:gosec // small
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	for _, blk := range m.params() {
		buf := make([]byte, 2*len(blk))
		for i, v := range blk {
			binary.LittleEndian.PutUint16(buf[2*i:], toHalf(v))
		}
		if _, err := w.Write(buf); err != nil {
			return err
		}
	}
	return nil
}

// Load reads weights Save wrote.
func Load(r io.Reader) (*Model, error) {
	hdr := make([]byte, len(modelMagic)+4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	if string(hdr[:len(modelMagic)]) != modelMagic || int(binary.BigEndian.Uint32(hdr[len(modelMagic):])) != classes {
		return nil, errors.New("ocr: not a model for this charset")
	}
	m := NewModel(0)
	for _, blk := range m.params() {
		buf := make([]byte, 2*len(blk))
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		for i := range blk {
			blk[i] = fromHalf(binary.LittleEndian.Uint16(buf[2*i:]))
		}
	}
	return m, nil
}

// toHalf rounds a float32 to IEEE half precision.
func toHalf(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16(b >> 16 & 0x8000)
	exp := int(b>>23&0xff) - 127 + 15
	mant := b & 0x7fffff
	switch {
	case exp >= 31:
		return sign | 0x7c00
	case exp <= 0:
		if exp < -10 {
			return sign
		}
		mant |= 0x800000
		shift := uint(14 - exp)
		h := mant >> shift
		if mant>>(shift-1)&1 != 0 {
			h++
		}
		return sign | uint16(h) //nolint:gosec // fits
	}
	h := uint32(exp)<<10 | mant>>13 //nolint:gosec // in range
	if mant&0x1000 != 0 {
		h++ // rounds into the exponent if need be
	}
	return sign | uint16(h) //nolint:gosec // fits
}

func fromHalf(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := int(h >> 10 & 0x1f)
	mant := uint32(h & 0x3ff)
	switch exp {
	case 0:
		if mant == 0 {
			return math.Float32frombits(sign)
		}
		f := float32(mant) / 1024 / 16384
		if sign != 0 {
			f = -f
		}
		return f
	case 31:
		return math.Float32frombits(sign | 0x7f800000 | mant<<13)
	}
	return math.Float32frombits(sign | uint32(exp-15+127)<<23 | mant<<13) //nolint:gosec // in range
}
