// Package nn runs neural networks published as PyTorch checkpoints: their
// weights read from the .pth file as it is, and the layers they are made of
// (linear, convolutions, attention, normalisations, resizing) on float32
// tensors, laid out as PyTorch lays them out (row major, the last dimension
// fastest).
package nn

import "fmt"

// Tensor is a dense float32 array of a shape: in Data, or on a GPU (a
// GPU backend's tensors; Data nil).
type Tensor struct {
	Shape []int
	Data  []float32
	dev   *devBuf
}

// New is a zeroed tensor of a shape.
func New(shape ...int) *Tensor {
	return &Tensor{Shape: append([]int(nil), shape...), Data: make([]float32, Numel(shape))}
}

// Numel is the number of elements of a shape.
func Numel(shape []int) int {
	n := 1
	for _, d := range shape {
		n *= d
	}
	return n
}

// Dim is the size of dimension i (negative from the end).
func (t *Tensor) Dim(i int) int {
	if i < 0 {
		i += len(t.Shape)
	}
	return t.Shape[i]
}

// Reshape is the tensor seen with another shape of as many elements (one
// dimension may be -1, inferred). The data is shared.
func (t *Tensor) Reshape(shape ...int) *Tensor {
	s := append([]int(nil), shape...)
	known, infer := 1, -1
	for i, d := range s {
		if d == -1 {
			infer = i
			continue
		}
		known *= d
	}
	n := Numel(t.Shape)
	if infer >= 0 {
		s[infer] = n / known
	}
	if Numel(s) != n {
		panic(fmt.Sprintf("nn: reshape %v to %v", t.Shape, shape))
	}
	return &Tensor{Shape: s, Data: t.Data, dev: t.dev}
}

// Clone is a copy of the tensor (in memory: a GPU tensor's is its
// backend's Clone).
func (t *Tensor) Clone() *Tensor {
	return &Tensor{Shape: append([]int(nil), t.Shape...), Data: append([]float32(nil), t.Data...)}
}

func (t *Tensor) String() string { return fmt.Sprintf("Tensor%v", t.Shape) }
