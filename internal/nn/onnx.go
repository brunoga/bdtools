package nn

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Graph is a network published as an ONNX model: its nodes in the order
// they run, and the constant tensors they read (the model's initializers
// and its Constant nodes' values), by name.
type Graph struct {
	Nodes   []Node
	Consts  map[string]*Tensor
	Inputs  []string
	Outputs []string
}

// Node is one operation of a Graph: its ONNX operator, the names of the
// tensors it reads and makes, and its attributes.
type Node struct {
	Op      string
	In, Out []string
	Attr    map[string]Attr
}

// Attr is a node's attribute. Only the fields of its kind are set.
type Attr struct {
	F    float32
	I    int64
	S    string
	T    *Tensor
	Fs   []float32
	Is   []int64
	Kind int // ONNX AttributeType: 1 float, 2 int, 3 string, 4 tensor, 6 floats, 7 ints
}

// Int is the attribute i of a node, or def when the node has none.
func (n *Node) Int(name string, def int) int {
	if a, ok := n.Attr[name]; ok {
		return int(a.I)
	}
	return def
}

// Ints is the int list attribute of a node, or nil.
func (n *Node) Ints(name string) []int {
	a, ok := n.Attr[name]
	if !ok {
		return nil
	}
	out := make([]int, len(a.Is))
	for i, v := range a.Is {
		out[i] = int(v)
	}
	return out
}

// Float is the float attribute of a node, or def when the node has none.
func (n *Node) Float(name string, def float32) float32 {
	if a, ok := n.Attr[name]; ok {
		return a.F
	}
	return def
}

// LoadONNX reads an ONNX model's graph. Its tensors must be float32 or
// int64 (which become float32), stored in the file.
func LoadONNX(b []byte) (*Graph, error) {
	g := &Graph{Consts: map[string]*Tensor{}}
	err := fields(b, func(f int, v []byte, _ uint64) error {
		if f == 7 { // ModelProto.graph
			return g.read(v)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("nn: onnx: %w", err)
	}
	if len(g.Nodes) == 0 {
		return nil, errors.New("nn: onnx: no graph")
	}
	return g, nil
}

func (g *Graph) read(b []byte) error {
	return fields(b, func(f int, v []byte, _ uint64) error {
		switch f {
		case 1: // node
			n, err := readNode(v)
			if err != nil {
				return err
			}
			if n.Op == "Constant" {
				if a, ok := n.Attr["value"]; ok && a.T != nil {
					g.Consts[n.Out[0]] = a.T
					return nil
				}
				return fmt.Errorf("constant %s without a tensor value", n.Out[0])
			}
			g.Nodes = append(g.Nodes, n)
		case 5: // initializer
			name, t, err := readTensor(v)
			if err != nil {
				return err
			}
			g.Consts[name] = t
		case 11, 12: // input, output: ValueInfoProto, its name is field 1
			return fields(v, func(f2 int, s []byte, _ uint64) error {
				if f2 == 1 {
					if f == 11 {
						g.Inputs = append(g.Inputs, string(s))
					} else {
						g.Outputs = append(g.Outputs, string(s))
					}
				}
				return nil
			})
		}
		return nil
	})
}

func readNode(b []byte) (Node, error) {
	n := Node{Attr: map[string]Attr{}}
	err := fields(b, func(f int, v []byte, _ uint64) error {
		switch f {
		case 1:
			n.In = append(n.In, string(v))
		case 2:
			n.Out = append(n.Out, string(v))
		case 4:
			n.Op = string(v)
		case 5:
			name, a, err := readAttr(v)
			if err != nil {
				return err
			}
			n.Attr[name] = a
		}
		return nil
	})
	return n, err
}

func readAttr(b []byte) (string, Attr, error) {
	var name string
	var a Attr
	err := fields(b, func(f int, v []byte, x uint64) error {
		switch f {
		case 1:
			name = string(v)
		case 2:
			a.F = math.Float32frombits(uint32(x))
		case 3:
			a.I = int64(x)
		case 4:
			a.S = string(v)
		case 5:
			_, t, err := readTensor(v)
			a.T = t
			return err
		case 7:
			if v == nil { // one float, not packed
				a.Fs = append(a.Fs, math.Float32frombits(uint32(x)))
				return nil
			}
			for len(v) >= 4 {
				a.Fs = append(a.Fs, math.Float32frombits(binary.LittleEndian.Uint32(v)))
				v = v[4:]
			}
		case 8:
			if v == nil {
				a.Is = append(a.Is, int64(x))
				return nil
			}
			for len(v) > 0 {
				u, k := binary.Uvarint(v)
				if k <= 0 {
					return errors.New("bad varint")
				}
				a.Is = append(a.Is, int64(u))
				v = v[k:]
			}
		case 20:
			a.Kind = int(x)
		}
		return nil
	})
	return name, a, err
}

// readTensor reads a TensorProto of float32 (data type 1) or int64 (7).
func readTensor(b []byte) (string, *Tensor, error) {
	var name string
	var dims []int
	var typ int
	var raw []byte
	var fl []float32
	var in []int64
	err := fields(b, func(f int, v []byte, x uint64) error {
		switch f {
		case 1:
			if v == nil {
				dims = append(dims, int(x))
				return nil
			}
			for len(v) > 0 {
				u, k := binary.Uvarint(v)
				if k <= 0 {
					return errors.New("bad varint")
				}
				dims = append(dims, int(u))
				v = v[k:]
			}
		case 2:
			typ = int(x)
		case 4:
			if v == nil {
				fl = append(fl, math.Float32frombits(uint32(x)))
				return nil
			}
			for len(v) >= 4 {
				fl = append(fl, math.Float32frombits(binary.LittleEndian.Uint32(v)))
				v = v[4:]
			}
		case 7:
			if v == nil {
				in = append(in, int64(x))
				return nil
			}
			for len(v) > 0 {
				u, k := binary.Uvarint(v)
				if k <= 0 {
					return errors.New("bad varint")
				}
				in = append(in, int64(u))
				v = v[k:]
			}
		case 8:
			name = string(v)
		case 9:
			raw = v
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	t := &Tensor{Shape: dims, Data: make([]float32, Numel(dims))}
	switch typ {
	case 1:
		switch {
		case raw != nil:
			if len(raw) != 4*len(t.Data) {
				return "", nil, fmt.Errorf("tensor %s: %d bytes for %v", name, len(raw), dims)
			}
			for i := range t.Data {
				t.Data[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
			}
		case len(fl) == len(t.Data):
			copy(t.Data, fl)
		default:
			return "", nil, fmt.Errorf("tensor %s: %d floats for %v", name, len(fl), dims)
		}
	case 7:
		switch {
		case raw != nil:
			if len(raw) != 8*len(t.Data) {
				return "", nil, fmt.Errorf("tensor %s: %d bytes for %v", name, len(raw), dims)
			}
			for i := range t.Data {
				t.Data[i] = float32(int64(binary.LittleEndian.Uint64(raw[8*i:])))
			}
		case len(in) == len(t.Data):
			for i, v := range in {
				t.Data[i] = float32(v)
			}
		default:
			return "", nil, fmt.Errorf("tensor %s: %d ints for %v", name, len(in), dims)
		}
	default:
		return "", nil, fmt.Errorf("tensor %s: data type %d", name, typ)
	}
	return name, t, nil
}

// fields calls f for each field of a protocol buffer message: its number,
// and its bytes (length-delimited fields) or its value (varints and fixed
// fields, with v nil).
func fields(b []byte, f func(num int, v []byte, x uint64) error) error {
	for len(b) > 0 {
		key, k := binary.Uvarint(b)
		if k <= 0 {
			return errors.New("bad field key")
		}
		b = b[k:]
		num := int(key >> 3)
		var v []byte
		var x uint64
		switch key & 7 {
		case 0:
			x, k = binary.Uvarint(b)
			if k <= 0 {
				return errors.New("bad varint")
			}
			b = b[k:]
		case 1:
			if len(b) < 8 {
				return errors.New("short fixed64")
			}
			x, b = binary.LittleEndian.Uint64(b), b[8:]
		case 2:
			l, k := binary.Uvarint(b)
			if k <= 0 || uint64(len(b)-k) < l {
				return errors.New("bad length")
			}
			v, b = b[k:k+int(l)], b[k+int(l):]
			if v == nil {
				v = []byte{}
			}
		case 5:
			if len(b) < 4 {
				return errors.New("short fixed32")
			}
			x, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		default:
			return fmt.Errorf("wire type %d", key&7)
		}
		if err := f(num, v, x); err != nil {
			return err
		}
	}
	return nil
}
