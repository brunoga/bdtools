package textdet

import "github.com/brunoga/bdtools/internal/nn"

// The network's input normalisation: ImageNet's mean and spread of each
// of red, green and blue.
var (
	mean = [3]float32{0.485, 0.456, 0.406}
	std  = [3]float32{0.229, 0.224, 0.225}
)

// Input is the network's input for a grey picture w x h (row stride
// stride): [3, H, W], H and W the next multiples of 32, the picture in
// every channel and zero (after normalisation) beyond it.
func Input(y []byte, w, h, stride int) *nn.Tensor {
	H, W := (h+31)/32*32, (w+31)/32*32
	x := nn.New(3, H, W)
	for c := range 3 {
		m, s := mean[c], std[c]
		for r := range h {
			row := y[r*stride : r*stride+w]
			dst := x.Data[(c*H+r)*W:]
			for i, v := range row {
				dst[i] = (float32(v)/255 - m) / s
			}
		}
	}
	return x
}
