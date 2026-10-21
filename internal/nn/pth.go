package nn

// Reading a PyTorch checkpoint (torch.save's zip format): a pickle of the
// state dict, data.pkl, whose tensors refer to their storages by key, each
// storage a file of its own beside it (little endian). The pickle is read
// by a small machine that knows the opcodes torch.save writes and the
// globals a state dict holds (_rebuild_tensor_v2, OrderedDict and the
// storage classes); anything else is refused, nothing in it is run.

import (
	"archive/zip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strings"
)

// LoadPTH reads the tensors of a checkpoint's state dict, by name. A
// checkpoint whose state dict is under a key ("state_dict", "model")
// gives that one's.
func LoadPTH(file string) (map[string]*Tensor, error) {
	z, err := zip.OpenReader(file)
	if err != nil {
		return nil, fmt.Errorf("nn: %s: %w", file, err)
	}
	defer func() { _ = z.Close() }()
	var pkl *zip.File
	files := map[string]*zip.File{}
	for _, f := range z.File {
		files[f.Name] = f
		if path.Base(f.Name) == "data.pkl" {
			pkl = f
		}
	}
	if pkl == nil {
		return nil, fmt.Errorf("nn: %s: no data.pkl (not a torch.save zip)", file)
	}
	root := strings.TrimSuffix(pkl.Name, "data.pkl")
	b, err := readZip(pkl)
	if err != nil {
		return nil, err
	}
	u := &unpickler{data: b, memo: map[int]any{}, storage: func(key string) ([]byte, error) {
		f, ok := files[root+"data/"+key]
		if !ok {
			return nil, fmt.Errorf("no storage %q", key)
		}
		return readZip(f)
	}, storages: map[string][]float32{}}
	v, err := u.run()
	if err != nil {
		return nil, fmt.Errorf("nn: %s: %w", file, err)
	}
	d, ok := v.(*pdict)
	if !ok {
		return nil, fmt.Errorf("nn: %s: the checkpoint is not a dict", file)
	}
	for _, k := range []string{"state_dict", "model"} {
		if inner, ok := d.get(k).(*pdict); ok {
			d = inner
		}
	}
	out := map[string]*Tensor{}
	for i, k := range d.keys {
		name, ok := k.(string)
		if !ok {
			continue
		}
		if t, ok := d.vals[i].(*Tensor); ok {
			out[name] = t
		}
	}
	return out, nil
}

func readZip(f *zip.File) ([]byte, error) {
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// The pickle's values.
type (
	pglobal struct{ module, name string }
	ptuple  []any
	plist   []any
	pdict   struct {
		keys, vals []any
	}
	// pstorage is a persistent id's storage reference.
	pstorage struct {
		kind, key string
		numel     int
	}
)

func (d *pdict) set(k, v any) {
	for i, x := range d.keys {
		if x == k {
			d.vals[i] = v
			return
		}
	}
	d.keys, d.vals = append(d.keys, k), append(d.vals, v)
}

func (d *pdict) get(k any) any {
	for i, x := range d.keys {
		if x == k {
			return d.vals[i]
		}
	}
	return nil
}

type unpickler struct {
	data     []byte
	pos      int
	stack    []any
	marks    []int
	memo     map[int]any
	storage  func(key string) ([]byte, error)
	storages map[string][]float32 // read, as float32
}

func (u *unpickler) byte1() (byte, error) {
	if u.pos >= len(u.data) {
		return 0, io.ErrUnexpectedEOF
	}
	u.pos++
	return u.data[u.pos-1], nil
}

func (u *unpickler) bytes(n int) ([]byte, error) {
	if n < 0 || u.pos+n > len(u.data) {
		return nil, io.ErrUnexpectedEOF
	}
	u.pos += n
	return u.data[u.pos-n : u.pos], nil
}

func (u *unpickler) line() (string, error) {
	i := strings.IndexByte(string(u.data[u.pos:]), '\n')
	if i < 0 {
		return "", io.ErrUnexpectedEOF
	}
	s := string(u.data[u.pos : u.pos+i])
	u.pos += i + 1
	return s, nil
}

func (u *unpickler) push(v any) { u.stack = append(u.stack, v) }

func (u *unpickler) pop() (any, error) {
	if len(u.stack) == 0 {
		return nil, errors.New("pickle: stack underflow")
	}
	v := u.stack[len(u.stack)-1]
	u.stack = u.stack[:len(u.stack)-1]
	return v, nil
}

func (u *unpickler) top() (any, error) {
	if len(u.stack) == 0 {
		return nil, errors.New("pickle: stack underflow")
	}
	return u.stack[len(u.stack)-1], nil
}

// popMark pops the values pushed since the last MARK.
func (u *unpickler) popMark() ([]any, error) {
	if len(u.marks) == 0 {
		return nil, errors.New("pickle: no mark")
	}
	m := u.marks[len(u.marks)-1]
	u.marks = u.marks[:len(u.marks)-1]
	items := append([]any(nil), u.stack[m:]...)
	u.stack = u.stack[:m]
	return items, nil
}

func (u *unpickler) uint(n int) (int, error) {
	b, err := u.bytes(n)
	if err != nil {
		return 0, err
	}
	v := 0
	for i := n - 1; i >= 0; i-- {
		v = v<<8 | int(b[i])
	}
	return v, nil
}

func (u *unpickler) run() (any, error) {
	for {
		op, err := u.byte1()
		if err != nil {
			return nil, err
		}
		switch op {
		case 0x80: // PROTO
			if _, err := u.byte1(); err != nil {
				return nil, err
			}
		case 0x95: // FRAME
			if _, err := u.bytes(8); err != nil {
				return nil, err
			}
		case '.': // STOP
			return u.pop()
		case '(': // MARK
			u.marks = append(u.marks, len(u.stack))
		case '}': // EMPTY_DICT
			u.push(&pdict{})
		case ']': // EMPTY_LIST
			u.push(&plist{})
		case ')': // EMPTY_TUPLE
			u.push(ptuple{})
		case 'N': // NONE
			u.push(nil)
		case 0x88: // NEWTRUE
			u.push(true)
		case 0x89: // NEWFALSE
			u.push(false)
		case 'K': // BININT1
			v, err := u.uint(1)
			if err != nil {
				return nil, err
			}
			u.push(v)
		case 'M': // BININT2
			v, err := u.uint(2)
			if err != nil {
				return nil, err
			}
			u.push(v)
		case 'J': // BININT
			v, err := u.uint(4)
			if err != nil {
				return nil, err
			}
			u.push(int(int32(uint32(v)))) //nolint:gosec // a signed 32 bit value
		case 0x8a: // LONG1
			n, err := u.uint(1)
			if err != nil {
				return nil, err
			}
			b, err := u.bytes(n)
			if err != nil {
				return nil, err
			}
			var v int64
			for i := len(b) - 1; i >= 0; i-- {
				v = v<<8 | int64(b[i])
			}
			if n > 0 && n < 8 && b[n-1]&0x80 != 0 {
				v -= 1 << (8 * uint(n)) //nolint:gosec // n < 8
			}
			u.push(int(v))
		case 'G': // BINFLOAT
			b, err := u.bytes(8)
			if err != nil {
				return nil, err
			}
			u.push(math.Float64frombits(binary.BigEndian.Uint64(b)))
		case 'X', 0x8c: // BINUNICODE, SHORT_BINUNICODE
			w := 4
			if op == 0x8c {
				w = 1
			}
			n, err := u.uint(w)
			if err != nil {
				return nil, err
			}
			b, err := u.bytes(n)
			if err != nil {
				return nil, err
			}
			u.push(string(b))
		case 'c': // GLOBAL
			mod, err := u.line()
			if err != nil {
				return nil, err
			}
			name, err := u.line()
			if err != nil {
				return nil, err
			}
			u.push(pglobal{mod, name})
		case 'q', 'r': // BINPUT, LONG_BINPUT
			w := 1
			if op == 'r' {
				w = 4
			}
			i, err := u.uint(w)
			if err != nil {
				return nil, err
			}
			v, err := u.top()
			if err != nil {
				return nil, err
			}
			u.memo[i] = v
		case 'h', 'j': // BINGET, LONG_BINGET
			w := 1
			if op == 'j' {
				w = 4
			}
			i, err := u.uint(w)
			if err != nil {
				return nil, err
			}
			v, ok := u.memo[i]
			if !ok {
				return nil, fmt.Errorf("pickle: memo %d unset", i)
			}
			u.push(v)
		case 't': // TUPLE
			items, err := u.popMark()
			if err != nil {
				return nil, err
			}
			u.push(ptuple(items))
		case 0x85, 0x86, 0x87: // TUPLE1, TUPLE2, TUPLE3
			n := int(op - 0x84)
			if len(u.stack) < n {
				return nil, errors.New("pickle: stack underflow")
			}
			items := append(ptuple(nil), u.stack[len(u.stack)-n:]...)
			u.stack = u.stack[:len(u.stack)-n]
			u.push(items)
		case 'a', 'e': // APPEND, APPENDS
			var items []any
			if op == 'a' {
				v, err := u.pop()
				if err != nil {
					return nil, err
				}
				items = []any{v}
			} else if items, err = u.popMark(); err != nil {
				return nil, err
			}
			l, err := u.top()
			if err != nil {
				return nil, err
			}
			if pl, ok := l.(*plist); ok {
				*pl = append(*pl, items...)
			}
		case 's', 'u': // SETITEM, SETITEMS
			var items []any
			if op == 's' {
				v, err := u.pop()
				if err != nil {
					return nil, err
				}
				k, err := u.pop()
				if err != nil {
					return nil, err
				}
				items = []any{k, v}
			} else if items, err = u.popMark(); err != nil {
				return nil, err
			}
			d, err := u.top()
			if err != nil {
				return nil, err
			}
			pd, ok := d.(*pdict)
			if !ok {
				return nil, errors.New("pickle: SETITEMS on a non-dict")
			}
			for i := 0; i+1 < len(items); i += 2 {
				pd.set(items[i], items[i+1])
			}
		case 'b': // BUILD: an object's state; an OrderedDict's is ignored
			if _, err := u.pop(); err != nil {
				return nil, err
			}
		case 'Q': // BINPERSID
			pid, err := u.pop()
			if err != nil {
				return nil, err
			}
			s, err := persistent(pid)
			if err != nil {
				return nil, err
			}
			u.push(s)
		case 'R': // REDUCE
			args, err := u.pop()
			if err != nil {
				return nil, err
			}
			fn, err := u.pop()
			if err != nil {
				return nil, err
			}
			v, err := u.reduce(fn, args)
			if err != nil {
				return nil, err
			}
			u.push(v)
		default:
			return nil, fmt.Errorf("pickle: opcode 0x%02x not supported", op)
		}
	}
}

// persistent reads a storage's persistent id: ('storage', class, key,
// location, numel).
func persistent(pid any) (*pstorage, error) {
	t, ok := pid.(ptuple)
	if !ok || len(t) < 5 || t[0] != "storage" {
		return nil, errors.New("pickle: a persistent id that is not a storage")
	}
	g, ok1 := t[1].(pglobal)
	key, ok2 := t[2].(string)
	n, ok3 := t[4].(int)
	if !ok1 || !ok2 || !ok3 {
		return nil, errors.New("pickle: a malformed storage id")
	}
	return &pstorage{kind: g.name, key: key, numel: n}, nil
}

func (u *unpickler) reduce(fn, args any) (any, error) {
	g, ok := fn.(pglobal)
	if !ok {
		return nil, errors.New("pickle: REDUCE of a non-global")
	}
	a, _ := args.(ptuple)
	switch g.module + "." + g.name {
	case "collections.OrderedDict", "builtins.dict":
		return &pdict{}, nil
	case "torch._utils._rebuild_tensor_v2":
		return u.rebuildTensor(a)
	}
	return nil, fmt.Errorf("pickle: %s.%s is not a state dict's", g.module, g.name)
}

// rebuildTensor is _rebuild_tensor_v2(storage, offset, size, stride, ...):
// the tensor copied out of its storage (contiguous, as a state dict's
// are; any other stride is honoured).
func (u *unpickler) rebuildTensor(a ptuple) (*Tensor, error) {
	if len(a) < 4 {
		return nil, errors.New("pickle: a short _rebuild_tensor_v2")
	}
	s, ok := a[0].(*pstorage)
	off, ok2 := a[1].(int)
	size, ok3 := a[2].(ptuple)
	stride, ok4 := a[3].(ptuple)
	if !ok || !ok2 || !ok3 || !ok4 || len(size) != len(stride) {
		return nil, errors.New("pickle: a malformed tensor")
	}
	data, err := u.storageData(s)
	if err != nil {
		return nil, err
	}
	shape := make([]int, len(size))
	strides := make([]int, len(size))
	for i := range size {
		shape[i], _ = size[i].(int)
		strides[i], _ = stride[i].(int)
	}
	t := New(shape...)
	idx := make([]int, len(shape))
	for i := range t.Data {
		p := off
		for d := range idx {
			p += idx[d] * strides[d]
		}
		if p < 0 || p >= len(data) {
			return nil, errors.New("pickle: a tensor beyond its storage")
		}
		t.Data[i] = data[p]
		for d := len(idx) - 1; d >= 0; d-- {
			idx[d]++
			if idx[d] < shape[d] {
				break
			}
			idx[d] = 0
		}
	}
	return t, nil
}

func (u *unpickler) storageData(s *pstorage) ([]float32, error) {
	if d, ok := u.storages[s.key]; ok {
		return d, nil
	}
	b, err := u.storage(s.key)
	if err != nil {
		return nil, err
	}
	var d []float32
	switch s.kind {
	case "FloatStorage":
		if len(b) < 4*s.numel {
			return nil, errors.New("pickle: a short storage")
		}
		d = make([]float32, s.numel)
		for i := range d {
			d[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
		}
	case "HalfStorage":
		if len(b) < 2*s.numel {
			return nil, errors.New("pickle: a short storage")
		}
		d = make([]float32, s.numel)
		for i := range d {
			d[i] = halfToFloat(binary.LittleEndian.Uint16(b[2*i:]))
		}
	case "BFloat16Storage":
		if len(b) < 2*s.numel {
			return nil, errors.New("pickle: a short storage")
		}
		d = make([]float32, s.numel)
		for i := range d {
			d[i] = math.Float32frombits(uint32(binary.LittleEndian.Uint16(b[2*i:])) << 16)
		}
	default:
		return nil, fmt.Errorf("pickle: %s tensors are not supported", s.kind)
	}
	u.storages[s.key] = d
	return d, nil
}

// halfToFloat converts an IEEE half precision value.
func halfToFloat(h uint16) float32 {
	sign := uint32(h>>15) << 31
	e := int32(h>>10) & 0x1f
	m := uint32(h) & 0x3ff
	switch {
	case e == 0 && m == 0:
		return math.Float32frombits(sign)
	case e == 0: // subnormal: normalised
		e = 1
		for m&0x400 == 0 {
			m <<= 1
			e--
		}
		m &= 0x3ff
	case e == 0x1f:
		return math.Float32frombits(sign | 0xff<<23 | m<<13)
	}
	return math.Float32frombits(sign | uint32(e+127-15)<<23 | m<<13) //nolint:gosec // a biased exponent
}
