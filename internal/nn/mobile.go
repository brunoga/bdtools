package nn

// The layers of the small convolutional networks made for phones
// (MobileNet and its kind): convolutions of each channel on its own,
// hard-swish and hard-sigmoid activations, squeeze-and-excitation, and
// nearest-neighbour upsampling.

// Activation functions of Act.
const (
	ActNone = iota
	ActReLU
	ActHardSwish   // x clip(x+3, 0, 6) / 6
	ActHardSigmoid // clip(A x + B, 0, 1), A and B the activation's
	ActSigmoid
)

// Act is an elementwise function: y = PostS f(PreS x + PreT) + PostT, f
// one of the Act* functions. A and B are a hard sigmoid's slope and
// offset.
type Act struct {
	Fn                       int
	PreS, PreT, PostS, PostT float32
	A, B                     float32
}

// Identity is the Act that changes nothing.
var Identity = Act{PreS: 1, PostS: 1}

// Apply is the act's value at x.
func (a Act) Apply(x float32) float32 {
	x = a.PreS*x + a.PreT
	switch a.Fn {
	case ActReLU:
		x = max(x, 0)
	case ActHardSwish:
		x = x * min(max(x+3, 0), 6) / 6
	case ActHardSigmoid:
		x = min(max(a.A*x+a.B, 0), 1)
	case ActSigmoid:
		x = 1 / (1 + expf(-x))
	}
	return a.PostS*x + a.PostT
}

// DepthwiseConv2d convolves each channel of x [C, H, W] with its own
// kernel w [C, 1, k, k] (plus b [C], or nil), zero beyond the edges.
func DepthwiseConv2d(x, w, b *Tensor, stride, pad int) *Tensor {
	c, h, wd := x.Dim(0), x.Dim(1), x.Dim(2)
	k := w.Dim(2)
	ho, wo := (h+2*pad-k)/stride+1, (wd+2*pad-k)/stride+1
	y := New(c, ho, wo)
	parallel(c, func(ci int) {
		src := x.Data[ci*h*wd : (ci+1)*h*wd]
		kw := w.Data[ci*k*k : (ci+1)*k*k]
		dst := y.Data[ci*ho*wo : (ci+1)*ho*wo]
		var bias float32
		if b != nil {
			bias = b.Data[ci]
		}
		for oy := range ho {
			for ox := range wo {
				s := bias
				for ky := range k {
					iy := oy*stride - pad + ky
					if iy < 0 || iy >= h {
						continue
					}
					for kx := range k {
						ix := ox*stride - pad + kx
						if ix < 0 || ix >= wd {
							continue
						}
						s += kw[ky*k+kx] * src[iy*wd+ix]
					}
				}
				dst[oy*wo+ox] = s
			}
		}
	})
	return y
}

// Activate is a's function of each element of x, a new tensor.
func Activate(x *Tensor, a Act) *Tensor {
	y := &Tensor{Shape: append([]int(nil), x.Shape...), Data: make([]float32, len(x.Data))}
	for i, v := range x.Data {
		y.Data[i] = a.Apply(v)
	}
	return y
}

// GlobalAvgPool is the mean of each channel of x [C, H, W], [C, 1, 1].
func GlobalAvgPool(x *Tensor) *Tensor {
	c, n := x.Dim(0), x.Dim(1)*x.Dim(2)
	y := New(c, 1, 1)
	for ci := range c {
		var s float64
		for _, v := range x.Data[ci*n : (ci+1)*n] {
			s += float64(v)
		}
		y.Data[ci] = float32(s / float64(n))
	}
	return y
}

// MulChannels is x [C, H, W] with each channel times its s [C, 1, 1].
func MulChannels(x, s *Tensor) *Tensor {
	c, n := x.Dim(0), x.Dim(1)*x.Dim(2)
	y := New(x.Shape...)
	for ci := range c {
		for i, v := range x.Data[ci*n : (ci+1)*n] {
			y.Data[ci*n+i] = v * s.Data[ci]
		}
	}
	return y
}

// Nearest is x [C, H, W] made f times larger by repeating each element.
func Nearest(x *Tensor, f int) *Tensor {
	c, h, w := x.Dim(0), x.Dim(1), x.Dim(2)
	y := New(c, h*f, w*f)
	for ci := range c {
		for oy := range h * f {
			src := x.Data[(ci*h+oy/f)*w:]
			dst := y.Data[(ci*h*f+oy)*w*f:]
			for ox := range w * f {
				dst[ox] = src[ox/f]
			}
		}
	}
	return y
}

// Concat is the tensors [Ci, H, W] one after another along the channels.
func Concat(xs ...*Tensor) *Tensor {
	c := 0
	for _, x := range xs {
		c += x.Dim(0)
	}
	y := New(c, xs[0].Dim(1), xs[0].Dim(2))
	off := 0
	for _, x := range xs {
		off += copy(y.Data[off:], x.Data)
	}
	return y
}
