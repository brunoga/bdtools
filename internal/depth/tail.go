package depth

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/brunoga/bdtools/internal/nn"
)

// TailNames is the head's tail (each a layer's .weight and, when it has
// one, .bias), which TailInput feeds and which can be learnt anew
// (tools/depthtrain -tail): the first two features' projections and
// resizing, their convolutions into the fusion, the last two fusion
// blocks and the output convolutions.
var TailNames = []string{
	"head.projects.0", "head.resize_layers.0", "head.scratch.layer1_rn",
	"head.projects.1", "head.resize_layers.1", "head.scratch.layer2_rn",
	"head.scratch.refinenet2.resConfUnit1.conv1", "head.scratch.refinenet2.resConfUnit1.conv2",
	"head.scratch.refinenet2.resConfUnit2.conv1", "head.scratch.refinenet2.resConfUnit2.conv2",
	"head.scratch.refinenet2.out_conv",
	"head.scratch.refinenet1.resConfUnit1.conv1", "head.scratch.refinenet1.resConfUnit1.conv2",
	"head.scratch.refinenet1.resConfUnit2.conv1", "head.scratch.refinenet1.resConfUnit2.conv2",
	"head.scratch.refinenet1.out_conv",
	"head.scratch.output_conv1", "head.scratch.output_conv2.0", "head.scratch.output_conv2.2",
}

const tailMagic = "BDTL1"

// WriteTail writes a tail's weights: the magic, their count, then each
// one's name (its length, uint32, then its bytes), its dimensions (their
// count, then each, uint32) and its values (float32), little-endian, in
// name order.
func WriteTail(w io.Writer, weights map[string]*nn.Tensor) error {
	b := bufio.NewWriter(w)
	_, _ = b.WriteString(tailMagic)
	names := make([]string, 0, len(weights))
	for n := range weights {
		names = append(names, n)
	}
	slices.Sort(names)
	le := binary.LittleEndian
	_ = binary.Write(b, le, uint32(len(names))) //nolint:gosec // a few dozen
	for _, n := range names {
		t := weights[n]
		_ = binary.Write(b, le, uint32(len(n))) //nolint:gosec // a name
		_, _ = b.WriteString(n)
		_ = binary.Write(b, le, uint32(len(t.Shape))) //nolint:gosec // at most 4
		for _, d := range t.Shape {
			_ = binary.Write(b, le, uint32(d)) //nolint:gosec // a weight's size
		}
		if err := binary.Write(b, le, t.Data); err != nil {
			return err
		}
	}
	return b.Flush()
}

// ReadTail reads a tail's weights (WriteTail's).
func ReadTail(path string) (map[string]*nn.Tensor, error) {
	f, err := os.Open(path) //nolint:gosec // the user's file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	w, err := DecodeTail(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return w, nil
}

// DecodeTail reads a tail's weights from r.
func DecodeTail(r io.Reader) (map[string]*nn.Tensor, error) {
	magic := make([]byte, len(tailMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != tailMagic {
		return nil, errors.New("not depth network tail weights")
	}
	le := binary.LittleEndian
	var n uint32
	if err := binary.Read(r, le, &n); err != nil || n > 1000 {
		return nil, errors.New("bad tail weights")
	}
	out := map[string]*nn.Tensor{}
	for range n {
		var l uint32
		if err := binary.Read(r, le, &l); err != nil || l > 256 {
			return nil, errors.New("bad tail weights")
		}
		name := make([]byte, l)
		if _, err := io.ReadFull(r, name); err != nil {
			return nil, err
		}
		var nd uint32
		if err := binary.Read(r, le, &nd); err != nil || nd > 4 {
			return nil, errors.New("bad tail weights")
		}
		dims := make([]uint32, nd)
		if err := binary.Read(r, le, dims); err != nil {
			return nil, err
		}
		shape := make([]int, nd)
		for i, d := range dims {
			shape[i] = int(d)
		}
		t := nn.New(shape...)
		if err := binary.Read(r, le, t.Data); err != nil {
			return nil, err
		}
		out[string(name)] = t
	}
	return out, nil
}

// SetTail puts a tail's weights (ReadTail's) in place of the
// checkpoint's: each must be one the model has, of the same shape.
func (m *Model) SetTail(w map[string]*nn.Tensor) error {
	for n, t := range w {
		old, ok := m.w[n]
		if !ok || !slices.Equal(old.Shape, t.Shape) {
			return fmt.Errorf("depth: the tail's %s is not the model's", n)
		}
	}
	for n, t := range w {
		m.w[n] = t
		if d, ok := m.dw[n]; ok {
			m.be.Free(d)
			delete(m.dw, n)
		}
	}
	return nil
}
