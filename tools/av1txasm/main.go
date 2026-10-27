// Command av1txasm turns the AV1 encoder's 1D forward transforms
// (internal/av1/transform_fwd.go) and the inverse ones
// (transform_bf.go) into AVX2 kernels that run eight of them at once, one
// per 32-bit lane:
//
//	go run ./tools/av1txasm -dir internal/av1
//
// Each butterfly statement of fdct8 to fdct64, fadst8 and fadst16 (a
// copy, a sum or difference, or a halfBtf of two cosines) becomes a few
// vector instructions on eight lanes, the same arithmetic as the Go: the
// products' low 32 bits, summed, rounded and shifted down. Copies only
// rename; every value has a 32-byte slot in the kernel's frame, reused
// after the value's last use. Two files are written:
//
//   - fwdtx_amd64.s: for each transform a column kernel (eight adjacent
//     columns of a residual, read a row at a time with a stride, scaled
//     up, transformed, rounded down and written to eight columns of the
//     column pass's output) and a row kernel (eight rows of that output,
//     transposed in, transformed and transposed out, rounded down; for
//     the ADSTs also one reading the rows right to left).
//   - fwdtx_amd64.go: their declarations and the switches that call them.
//   - invtx_amd64.s and invtx_amd64.go: the same for the inverse DCTs
//     and 8- and 16-point ADSTs (whose rotations, clamped to the spec's
//     ranges, fit 32 bits at bit depths up to 10), each pass rounded
//     and clamped as inverseTransform's.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

// the transforms made into kernels: the function, its kind (as the
// package's tx1d constants name it) and log2 of its size
var kernels = []struct {
	fn, kind string
	log2     int
}{
	{"fdct8", "tx1dDCT", 3}, {"fdct16", "tx1dDCT", 4}, {"fdct32", "tx1dDCT", 5}, {"fdct64", "tx1dDCT", 6},
	{"fadst8", "tx1dADST", 3}, {"fadst16", "tx1dADST", 4},
}

// op is one vector operation: a+b, a-b, -a, or a halfBtf of cosines k0
// and k1 (negated when n0, n1) times values a and b.
type op struct {
	kind   byte // '+', '-', 'n', 'b'
	a, b   int  // operand values
	k0, k1 int
	n0, n1 bool
	slot   int
}

// prog is a transform's dataflow: values 0..n-1 its inputs, the others
// ops[v-n]'s results; outs the values of its outputs.
type prog struct {
	n    int
	ops  []op
	outs []int
}

func parseFn(fd *ast.FuncDecl, n int) *prog {
	p := &prog{n: n}
	env := map[string]map[int]int{"in": {}, "out": {}, "step": {}}
	for i := 0; i < n; i++ {
		env["in"][i] = i
	}
	fail := func(nd ast.Node, why string) {
		log.Fatalf("%s: %s at %v", fd.Name.Name, why, nd.Pos())
	}
	index := func(e ast.Expr) (string, int) {
		ix, ok := e.(*ast.IndexExpr)
		if !ok {
			fail(e, "not an element")
		}
		id, ok := ix.X.(*ast.Ident)
		if !ok {
			fail(e, "not an array")
		}
		lit, ok := ix.Index.(*ast.BasicLit)
		if !ok {
			fail(e, "not a constant index")
		}
		i, _ := strconv.Atoi(lit.Value)
		return id.Name, i
	}
	value := func(e ast.Expr) int {
		a, i := index(e)
		v, ok := env[a][i]
		if !ok {
			fail(e, "read before written")
		}
		return v
	}
	cos := func(e ast.Expr) (int, bool) {
		neg := false
		if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.SUB {
			neg, e = true, u.X
		}
		a, k := index(e)
		if a != "cospi" {
			fail(e, "not a cosine")
		}
		return k, neg
	}
	emit := func(o op) int {
		p.ops = append(p.ops, o)
		return n + len(p.ops) - 1
	}
	var eval func(e ast.Expr) int
	eval = func(e ast.Expr) int {
		switch x := e.(type) {
		case *ast.IndexExpr:
			return value(x)
		case *ast.UnaryExpr:
			if x.Op != token.SUB {
				fail(e, "unary "+x.Op.String())
			}
			return emit(op{kind: 'n', a: eval(x.X)})
		case *ast.BinaryExpr:
			// -a + b is b - a
			if u, ok := x.X.(*ast.UnaryExpr); ok && u.Op == token.SUB && x.Op == token.ADD {
				return emit(op{kind: '-', a: eval(x.Y), b: eval(u.X)})
			}
			switch x.Op {
			case token.ADD:
				return emit(op{kind: '+', a: eval(x.X), b: eval(x.Y)})
			case token.SUB:
				return emit(op{kind: '-', a: eval(x.X), b: eval(x.Y)})
			}
			fail(e, "operator "+x.Op.String())
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); !ok || id.Name != "halfBtf" || len(x.Args) != 5 {
				fail(e, "call")
			}
			k0, n0 := cos(x.Args[0])
			k1, n1 := cos(x.Args[2])
			return emit(op{kind: 'b', k0: k0, n0: n0, a: eval(x.Args[1]), k1: k1, n1: n1, b: eval(x.Args[3])})
		}
		fail(e, "expression")
		return 0
	}
	for _, s := range fd.Body.List {
		switch st := s.(type) {
		case *ast.AssignStmt:
			if len(st.Lhs) != 1 {
				fail(s, "multiple assignment")
			}
			if id, ok := st.Lhs[0].(*ast.Ident); ok && id.Name == "cospi" {
				continue
			}
			a, i := index(st.Lhs[0])
			env[a][i] = eval(st.Rhs[0])
		case *ast.DeclStmt: // var step [n]int32
		default:
			fail(s, "statement")
		}
	}
	for i := 0; i < n; i++ {
		v, ok := env["out"][i]
		if !ok {
			fail(fd, "output not written")
		}
		p.outs = append(p.outs, v)
	}
	return p
}

// alloc gives each value a slot, the inputs 0..n-1, reusing a slot after
// its value's last use; it returns how many there are.
func (p *prog) alloc() int {
	nv := p.n + len(p.ops)
	last := make([]int, nv)
	for i := range last {
		last[i] = -1
	}
	for i, o := range p.ops {
		last[o.a] = i
		if o.kind != 'n' {
			last[o.b] = i
		}
	}
	for _, v := range p.outs {
		last[v] = len(p.ops)
	}
	slot := make([]int, nv)
	var free []int
	nSlots := p.n
	for i := 0; i < p.n; i++ {
		slot[i] = i
		if last[i] < 0 {
			free = append(free, i)
		}
	}
	for i := range p.ops {
		o := &p.ops[i]
		for _, v := range []int{o.a, o.b} {
			if (o.kind != 'n' || v == o.a) && last[v] == i {
				free = append(free, slot[v])
				last[v] = -2 // freed once
			}
		}
		var s int
		if len(free) > 0 {
			best := 0
			for j := range free {
				if free[j] < free[best] {
					best = j
				}
			}
			s = free[best]
			free = append(free[:best], free[best+1:]...)
		} else {
			s = nSlots
			nSlots++
		}
		slot[p.n+i] = s
		o.slot = s
		if last[p.n+i] < 0 { // never read
			free = append(free, s)
		}
	}
	for i := range p.ops {
		p.ops[i].a = slot[p.ops[i].a]
		if p.ops[i].kind != 'n' {
			p.ops[i].b = slot[p.ops[i].b]
		}
	}
	for i, v := range p.outs {
		p.outs[i] = slot[v]
	}
	return nSlots
}

type asm struct {
	bytes.Buffer
	label int
}

func (w *asm) f(format string, a ...any) { fmt.Fprintf(&w.Buffer, "\t"+format+"\n", a...) }

func at(slot int) string { return fmt.Sprintf("%d(SP)", slot*32) }

// body is the transform on the slots: Y15 zero, Y14 the cosines'
// rounding, X13 cos bit, CX the cosines.
func (w *asm) body(p *prog) {
	for _, o := range p.ops {
		switch o.kind {
		case '+':
			w.f("VMOVDQU %s, Y0", at(o.a))
			w.f("VPADDD %s, Y0, Y0", at(o.b))
		case '-':
			w.f("VMOVDQU %s, Y0", at(o.a))
			w.f("VPSUBD %s, Y0, Y0", at(o.b))
		case 'n':
			w.f("VPSUBD %s, Y15, Y0", at(o.a))
		case 'b':
			w.f("VPBROADCASTD %d(CX), Y1", o.k0*4)
			w.f("VPMULLD %s, Y1, Y0", at(o.a))
			w.f("VPBROADCASTD %d(CX), Y2", o.k1*4)
			w.f("VPMULLD %s, Y2, Y1", at(o.b))
			switch {
			case !o.n0 && !o.n1:
				w.f("VPADDD Y1, Y0, Y0")
			case !o.n0 && o.n1:
				w.f("VPSUBD Y1, Y0, Y0")
			case o.n0 && !o.n1:
				w.f("VPSUBD Y0, Y1, Y0")
			default:
				w.f("VPADDD Y1, Y0, Y0")
				w.f("VPSUBD Y0, Y15, Y0")
			}
			w.f("VPADDD Y14, Y0, Y0")
			w.f("VPSRAD X13, Y0, Y0")
		}
		w.f("VMOVDQU Y0, %s", at(o.slot))
	}
}

// transpose transposes the 8x8 dwords of Y0..Y7 into Y0..Y7, through
// Y8..Y15.
func (w *asm) transpose() {
	for i := 0; i < 8; i += 2 {
		w.f("VPUNPCKLDQ Y%d, Y%d, Y%d", i+1, i, 8+i)
		w.f("VPUNPCKHDQ Y%d, Y%d, Y%d", i+1, i, 9+i)
	}
	// Y8 = r0r1 lo, Y9 = r0r1 hi, Y10 = r2r3 lo, ...
	for _, g := range [][2]int{{8, 0}, {12, 4}} {
		b := g[0]
		o := g[1]
		w.f("VPUNPCKLQDQ Y%d, Y%d, Y%d", b+2, b, o)
		w.f("VPUNPCKHQDQ Y%d, Y%d, Y%d", b+2, b, o+1)
		w.f("VPUNPCKLQDQ Y%d, Y%d, Y%d", b+3, b+1, o+2)
		w.f("VPUNPCKHQDQ Y%d, Y%d, Y%d", b+3, b+1, o+3)
	}
	// Y0..Y3 rows 0-3's columns k, Y4..Y7 rows 4-7's, in 128-bit halves
	for k := 0; k < 4; k++ {
		w.f("VPERM2I128 $0x20, Y%d, Y%d, Y%d", 4+k, k, 8+k)
		w.f("VPERM2I128 $0x31, Y%d, Y%d, Y%d", 4+k, k, 12+k)
	}
	for k := 0; k < 4; k++ {
		w.f("VMOVDQA Y%d, Y%d", 8+k, k)
		w.f("VMOVDQA Y%d, Y%d", 12+k, 4+k)
	}
}

// consts sets up the body's registers from the arguments.
func (w *asm) consts() {
	w.f("VPXOR Y15, Y15, Y15")
	w.f("MOVQ cosBit+40(FP), CX")
	w.f("VMOVQ CX, X13")
	w.f("DECQ CX")
	w.f("MOVL $1, BX")
	w.f("SHLL CX, BX")
	w.f("VMOVD BX, X14")
	w.f("VPBROADCASTD X14, Y14")
	w.f("MOVQ cospi+32(FP), CX")
}

const args = "src *int32, srcStride int, dst *int32, dstStride int, cospi *int32, cosBit, up, down int"

func main() {
	dir := flag.String("dir", "internal/av1", "the package's directory")
	flag.Parse()
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, filepath.Join(*dir, "transform_fwd.go"), nil, 0)
	if err != nil {
		log.Fatal(err)
	}
	fns := map[string]*ast.FuncDecl{}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			fns[fd.Name.Name] = fd
		}
	}
	var s asm
	var g bytes.Buffer
	_, _ = s.WriteString("// Code generated by tools/av1txasm from transform_fwd.go. DO NOT EDIT.\n\n//go:build amd64 && !purego\n\n#include \"textflag.h\"\n")
	g.WriteString("// Code generated by tools/av1txasm from transform_fwd.go. DO NOT EDIT.\n\n//go:build amd64 && !purego\n\npackage av1\n")
	var cols, rows bytes.Buffer
	for _, k := range kernels {
		fd := fns[k.fn]
		if fd == nil {
			log.Fatalf("no %s", k.fn)
		}
		n := 1 << k.log2
		p := parseFn(fd, n)
		nSlots := p.alloc()
		keep := min(n, 32)
		frame := nSlots * 32
		name := k.fn + "Col"
		// the column kernel: in[i] the residual's row i, << up; out[i]
		// rounded down by down to dst's row i
		fmt.Fprintf(&s, "\n// func %sAVX2(%s)\nTEXT ·%sAVX2(SB), 0, $%d-64\n", name, args, name, frame)
		s.f("MOVQ src+0(FP), SI")
		s.f("MOVQ srcStride+8(FP), R8")
		s.f("MOVQ up+48(FP), AX")
		s.f("VMOVQ AX, X12")
		for i := 0; i < n; i++ {
			s.f("VMOVDQU (SI), Y0")
			s.f("VPSLLD X12, Y0, Y0")
			s.f("VMOVDQU Y0, %s", at(i))
			if i < n-1 {
				s.f("ADDQ R8, SI")
			}
		}
		s.consts()
		s.body(p)
		s.outCol(keep, p)
		fmt.Fprintf(&g, "\n//go:noescape\nfunc %sAVX2(%s)\n", name, args)
		fmt.Fprintf(&cols, "\tcase %s<<3 | %d:\n\t\t%sAVX2(src, srcStride, dst, dstStride, cospi, cosBit, up, down)\n", k.kind, k.log2, name)
		flips := []bool{false}
		if k.kind == "tx1dADST" {
			flips = append(flips, true)
		}
		for _, flip := range flips {
			name := k.fn + "Row"
			if flip {
				name += "Flip"
			}
			// the row kernel: eight rows of n, transposed in (reversed
			// when flip), transformed, keep of each transposed out
			fmt.Fprintf(&s, "\n// func %sAVX2(%s)\nTEXT ·%sAVX2(SB), 0, $%d-64\n", name, args, name, frame)
			s.f("MOVQ src+0(FP), SI")
			s.f("MOVQ srcStride+8(FP), R8")
			for b := 0; b < n; b += 8 {
				s.f("MOVQ SI, DI")
				for j := 0; j < 8; j++ {
					s.f("VMOVDQU %d(DI), Y%d", b*4, j)
					if j < 7 {
						s.f("ADDQ R8, DI")
					}
				}
				s.transpose()
				for j := 0; j < 8; j++ {
					i := b + j
					if flip {
						i = n - 1 - i
					}
					s.f("VMOVDQU Y%d, %s", j, at(i))
				}
			}
			s.consts()
			s.body(p)
			s.outRow(keep, p)
			fmt.Fprintf(&g, "\n//go:noescape\nfunc %sAVX2(%s)\n", name, args)
			cond := "!flip"
			if flip {
				cond = "flip"
			}
			if k.kind == "tx1dDCT" {
				fmt.Fprintf(&rows, "\tcase %s<<3 | %d:\n\t\t%sAVX2(src, srcStride, dst, dstStride, cospi, cosBit, 0, down)\n", k.kind, k.log2, name)
			} else {
				fmt.Fprintf(&rows, "\tcase %s<<3 | %d && %s:\n\t\t%sAVX2(src, srcStride, dst, dstStride, cospi, cosBit, 0, down)\n", k.kind, k.log2, cond, name)
			}
		}
	}
	fmt.Fprintf(&g, `
// fwdColAVX2 is the column pass of a 1D forward transform of a kind and
// log2 of the size on eight adjacent columns of a residual (its rows
// srcStride bytes apart, from src): each scaled up by up, transformed and
// rounded down by down into the first min(size, 32) rows of dst (dstStride
// bytes apart). It reports whether there is a kernel.
func fwdColAVX2(kind, log2 int, %s) bool {
	switch kind<<3 | log2 {
%s	default:
		return false
	}
	return true
}

// fwdRowAVX2 is the row pass: eight rows (srcStride bytes apart, from
// src) transformed, read right to left when flip, rounded down by down
// into the first min(size, 32) columns of dst's eight rows. It reports
// whether there is a kernel.
func fwdRowAVX2(kind, log2 int, flip bool, %s) bool {
	switch {
%s	default:
		return false
	}
	return true
}
`, args, cols.String(), args, rowCases(rows.String()))
	if err := os.WriteFile(filepath.Join(*dir, "fwdtx_amd64.s"), s.Bytes(), 0o644); err != nil { //nolint:gosec // Go source, read by everyone
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*dir, "fwdtx_amd64.go"), g.Bytes(), 0o644); err != nil { //nolint:gosec // Go source, read by everyone
		log.Fatal(err)
	}
	writeInverse(*dir)
}

// rowCases turns the row switch's "case kind<<3 | log2" into conditions
// on the arguments.
func rowCases(s string) string {
	var out bytes.Buffer
	for _, line := range bytes.Split([]byte(s), []byte("\n")) {
		if bytes.HasPrefix(line, []byte("\tcase ")) {
			rest := bytes.TrimSuffix(bytes.TrimPrefix(line, []byte("\tcase ")), []byte(":"))
			parts := bytes.SplitN(rest, []byte(" && "), 2)
			kl := bytes.SplitN(parts[0], []byte("<<3 | "), 2)
			cond := fmt.Sprintf("kind == %s && log2 == %s", kl[0], kl[1])
			if len(parts) == 2 {
				cond += " && " + string(parts[1])
			}
			fmt.Fprintf(&out, "\tcase %s:\n", cond)
			continue
		}
		if len(line) > 0 {
			out.Write(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// round loads down's rounding into Y8 and its count into X9.
func (w *asm) round() {
	w.label++
	w.f("MOVQ down+56(FP), CX")
	w.f("VMOVQ CX, X9")
	w.f("VPXOR Y8, Y8, Y8")
	w.f("TESTQ CX, CX")
	w.f("JZ noround%d", w.label)
	w.f("DECQ CX")
	w.f("MOVL $1, BX")
	w.f("SHLL CX, BX")
	w.f("VMOVD BX, X8")
	w.f("VPBROADCASTD X8, Y8")
	fmt.Fprintf(&w.Buffer, "noround%d:\n", w.label)
}

// outCol writes the first keep outputs, rounded, a row of dst each.
func (w *asm) outCol(keep int, p *prog) {
	w.round()
	w.f("MOVQ dst+16(FP), DI")
	w.f("MOVQ dstStride+24(FP), R9")
	for i := 0; i < keep; i++ {
		w.f("VMOVDQU %s, Y0", at(p.outs[i]))
		w.f("VPADDD Y8, Y0, Y0")
		w.f("VPSRAD X9, Y0, Y0")
		w.f("VMOVDQU Y0, (DI)")
		if i < keep-1 {
			w.f("ADDQ R9, DI")
		}
	}
	w.f("VZEROUPPER")
	w.f("RET")
}

// outRow transposes the first keep outputs back into eight rows of dst,
// rounded.
func (w *asm) outRow(keep int, p *prog) {
	w.f("MOVQ dst+16(FP), DX")
	w.f("MOVQ dstStride+24(FP), R9")
	for b := 0; b < keep; b += 8 {
		for j := 0; j < 8; j++ {
			w.f("VMOVDQU %s, Y%d", at(p.outs[b+j]), j)
		}
		w.transpose()
		w.round()
		w.f("MOVQ DX, DI")
		for j := 0; j < 8; j++ {
			w.f("VPADDD Y8, Y%d, Y%d", j, j)
			w.f("VPSRAD X9, Y%d, Y%d", j, j)
			w.f("VMOVDQU Y%d, %d(DI)", j, b*4)
			if j < 7 {
				w.f("ADDQ R9, DI")
			}
		}
	}
	w.f("VZEROUPPER")
	w.f("RET")
}
