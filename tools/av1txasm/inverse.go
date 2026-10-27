package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

// the inverse transforms made into kernels (transform_bf.go's)
var invKernels = []struct {
	fn, kind string
	log2     int
}{
	{"idct8", "tx1dDCT", 3}, {"idct16", "tx1dDCT", 4}, {"idct32", "tx1dDCT", 5}, {"idct64", "tx1dDCT", 6},
	{"iadst8", "tx1dADST", 3}, {"iadst16", "tx1dADST", 4},
}

// parseInv reads an unrolled inverse butterfly: x := t[i] loads, rot12
// rotations by constants, clamped sums and differences, and t[i] = x or
// -x stores.
func parseInv(fd *ast.FuncDecl, n int) *prog {
	p := &prog{n: n, outs: make([]int, n)}
	for i := range p.outs {
		p.outs[i] = -1
	}
	env := map[string]int{}
	fail := func(nd ast.Node, why string) {
		log.Fatalf("%s: %s at %v", fd.Name.Name, why, nd.Pos())
	}
	emit := func(o op) int {
		p.ops = append(p.ops, o)
		return n + len(p.ops) - 1
	}
	val := func(e ast.Expr) int {
		id, ok := e.(*ast.Ident)
		if !ok {
			fail(e, "not a variable")
		}
		v, ok := env[id.Name]
		if !ok {
			fail(e, "read before written")
		}
		return v
	}
	var lit func(e ast.Expr) int
	lit = func(e ast.Expr) int {
		if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.SUB {
			return -lit(u.X)
		}
		b, ok := e.(*ast.BasicLit)
		if !ok {
			fail(e, "not a constant")
		}
		v, _ := strconv.Atoi(b.Value)
		return v
	}
	tIndex := func(e ast.Expr) int {
		ix, ok := e.(*ast.IndexExpr)
		if !ok {
			fail(e, "not t[i]")
		}
		if id, ok := ix.X.(*ast.Ident); !ok || id.Name != "t" {
			fail(e, "not t[i]")
		}
		return lit(ix.Index)
	}
	// min(max(a op b, lo), hi)
	clamped := func(e ast.Expr) int {
		mn, ok := e.(*ast.CallExpr)
		if !ok || mn.Fun.(*ast.Ident).Name != "min" {
			fail(e, "not min")
		}
		mx, ok := mn.Args[0].(*ast.CallExpr)
		if !ok || mx.Fun.(*ast.Ident).Name != "max" {
			fail(e, "not max")
		}
		be, ok := mx.Args[0].(*ast.BinaryExpr)
		if !ok {
			fail(e, "not a sum")
		}
		k := byte('P')
		if be.Op == token.SUB {
			k = 'M'
		} else if be.Op != token.ADD {
			fail(e, "operator")
		}
		return emit(op{kind: k, a: val(be.X), b: val(be.Y)})
	}
	for _, s := range fd.Body.List {
		st, ok := s.(*ast.AssignStmt)
		if !ok {
			fail(s, "statement")
		}
		if st.Tok == token.DEFINE {
			env[st.Lhs[0].(*ast.Ident).Name] = tIndex(st.Rhs[0])
			continue
		}
		if _, ok := st.Lhs[0].(*ast.IndexExpr); ok {
			i := tIndex(st.Lhs[0])
			if u, ok := st.Rhs[0].(*ast.UnaryExpr); ok && u.Op == token.SUB {
				p.outs[i] = emit(op{kind: 'n', a: val(u.X)})
			} else {
				p.outs[i] = val(st.Rhs[0])
			}
			continue
		}
		var vs []int
		if call, ok := st.Rhs[0].(*ast.CallExpr); ok && call.Fun.(*ast.Ident).Name == "rot12" {
			x, y, c, sn := val(call.Args[0]), val(call.Args[1]), lit(call.Args[2]), lit(call.Args[3])
			vs = []int{
				emit(op{kind: 'r', a: x, b: y, k0: c, k1: sn}),
				emit(op{kind: 's', a: x, b: y, k0: c, k1: sn}),
			}
		} else {
			for _, r := range st.Rhs {
				vs = append(vs, clamped(r))
			}
		}
		if len(vs) != len(st.Lhs) {
			fail(s, "assignment count")
		}
		for i, l := range st.Lhs {
			env[l.(*ast.Ident).Name] = vs[i]
		}
	}
	for i, v := range p.outs {
		if v < 0 {
			fail(fd, fmt.Sprintf("t[%d] not stored", i))
		}
	}
	return p
}

// the inverse kernels' constants, an offset in invc<> each
type constTable struct {
	off  map[int]int
	vals []int
}

func (c *constTable) at(v int) string {
	if c.off == nil {
		c.off = map[int]int{}
	}
	o, ok := c.off[v]
	if !ok {
		o = len(c.vals) * 4
		c.off[v] = o
		c.vals = append(c.vals, v)
	}
	return fmt.Sprintf("invc<>+%d(SB)", o)
}

// invBody is an inverse transform on the slots: Y15 zero, Y14 2048, Y12
// and Y11 the clamp's bounds.
func (w *asm) invBody(p *prog, ct *constTable) {
	for _, o := range p.ops {
		switch o.kind {
		case 'P', 'M':
			w.f("VMOVDQU %s, Y0", at(o.a))
			if o.kind == 'P' {
				w.f("VPADDD %s, Y0, Y0", at(o.b))
			} else {
				w.f("VPSUBD %s, Y0, Y0", at(o.b))
			}
			w.f("VPMAXSD Y12, Y0, Y0")
			w.f("VPMINSD Y11, Y0, Y0")
		case 'n':
			w.f("VPSUBD %s, Y15, Y0", at(o.a))
		case 'r', 's':
			// r: x*c - y*s; s: x*s + y*c
			ka, kb := o.k0, o.k1
			if o.kind == 's' {
				ka, kb = o.k1, o.k0
			}
			w.f("VPBROADCASTD %s, Y1", ct.at(ka))
			w.f("VPMULLD %s, Y1, Y0", at(o.a))
			w.f("VPBROADCASTD %s, Y2", ct.at(kb))
			w.f("VPMULLD %s, Y2, Y1", at(o.b))
			if o.kind == 'r' {
				w.f("VPSUBD Y1, Y0, Y0")
			} else {
				w.f("VPADDD Y1, Y0, Y0")
			}
			w.f("VPADDD Y14, Y0, Y0")
			w.f("VPSRAD $12, Y0, Y0")
		default:
			log.Fatalf("op %c in an inverse", o.kind)
		}
		w.f("VMOVDQU Y0, %s", at(o.slot))
	}
}

const invArgs = "src *int32, srcStride int, dst *int32, dstStride int, lo, hi, rnd, shift, clo, chi, rect int"

func (w *asm) invConsts(ct *constTable) {
	w.f("VPXOR Y15, Y15, Y15")
	w.f("VPBROADCASTD %s, Y14", ct.at(2048))
	w.f("VPBROADCASTD lo+32(FP), Y12")
	w.f("VPBROADCASTD hi+40(FP), Y11")
}

// invPost loads the output's rounding into Y8, its shift into X9 and
// its clamp's bounds into Y10 and Y13.
func (w *asm) invPost() {
	w.f("VPBROADCASTD rnd+48(FP), Y8")
	w.f("VMOVQ shift+56(FP), X9")
	w.f("VPBROADCASTD clo+64(FP), Y10")
	w.f("VPBROADCASTD chi+72(FP), Y13")
}

func (w *asm) invRound(r int) {
	w.f("VPADDD Y8, Y%d, Y%d", r, r)
	w.f("VPSRAD X9, Y%d, Y%d", r, r)
	w.f("VPMAXSD Y10, Y%d, Y%d", r, r)
	w.f("VPMINSD Y13, Y%d, Y%d", r, r)
}

func writeInverse(dir string) {
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, filepath.Join(dir, "transform_bf.go"), nil, 0)
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
	var g, cols, rows bytes.Buffer
	var ct constTable
	_, _ = s.WriteString("// Code generated by tools/av1txasm from transform_bf.go. DO NOT EDIT.\n\n//go:build amd64 && !purego\n\n#include \"textflag.h\"\n")
	g.WriteString("// Code generated by tools/av1txasm from transform_bf.go. DO NOT EDIT.\n\n//go:build amd64 && !purego\n\npackage av1\n")
	for _, k := range invKernels {
		fd := fns[k.fn]
		if fd == nil {
			log.Fatalf("no %s", k.fn)
		}
		n := 1 << k.log2
		p := parseInv(fd, n)
		frame := p.alloc() * 32

		// the column kernel: eight columns of h rows (in place)
		name := k.fn + "Col"
		fmt.Fprintf(&s, "\n// func %sAVX2(%s)\nTEXT ·%sAVX2(SB), 0, $%d-88\n", name, invArgs, name, frame)
		s.f("MOVQ src+0(FP), SI")
		s.f("MOVQ srcStride+8(FP), R8")
		for i := 0; i < n; i++ {
			s.f("VMOVDQU (SI), Y0")
			s.f("VMOVDQU Y0, %s", at(i))
			if i < n-1 {
				s.f("ADDQ R8, SI")
			}
		}
		s.invConsts(&ct)
		s.invBody(p, &ct)
		s.invPost()
		s.f("MOVQ dst+16(FP), DI")
		s.f("MOVQ dstStride+24(FP), R9")
		for i := 0; i < n; i++ {
			s.f("VMOVDQU %s, Y0", at(p.outs[i]))
			s.invRound(0)
			s.f("VMOVDQU Y0, (DI)")
			if i < n-1 {
				s.f("ADDQ R9, DI")
			}
		}
		s.f("VZEROUPPER")
		s.f("RET")
		fmt.Fprintf(&g, "\n//go:noescape\nfunc %sAVX2(%s)\n", name, invArgs)
		fmt.Fprintf(&cols, "\tcase %s<<3 | %d:\n\t\t%sAVX2(src, srcStride, dst, dstStride, lo, hi, rnd, shift, clo, chi, rect)\n", k.kind, k.log2, name)

		// the row kernel: eight rows of min(n, 32) coefficients
		// transposed in (the rest zero, scaled by 2896/4096 when rect),
		// transformed and transposed out
		name = k.fn + "Row"
		fmt.Fprintf(&s, "\n// func %sAVX2(%s)\nTEXT ·%sAVX2(SB), 0, $%d-88\n", name, invArgs, name, frame)
		s.f("MOVQ src+0(FP), SI")
		s.f("MOVQ srcStride+8(FP), R8")
		s.f("MOVQ rect+80(FP), DX")
		for b := 0; b < n; b += 8 {
			if b >= 32 {
				s.f("VPXOR Y0, Y0, Y0")
				for j := 0; j < 8; j++ {
					s.f("VMOVDQU Y0, %s", at(b+j))
				}
				continue
			}
			s.f("MOVQ SI, DI")
			for j := 0; j < 8; j++ {
				s.f("VMOVDQU %d(DI), Y%d", b*4, j)
				if j < 7 {
					s.f("ADDQ R8, DI")
				}
			}
			s.transpose()
			s.label++
			s.f("TESTQ DX, DX")
			s.f("JZ norect%d", s.label)
			s.f("VPBROADCASTD %s, Y8", ct.at(2896))
			s.f("VPBROADCASTD %s, Y9", ct.at(2048))
			for j := 0; j < 8; j++ {
				s.f("VPMULLD Y8, Y%d, Y%d", j, j)
				s.f("VPADDD Y9, Y%d, Y%d", j, j)
				s.f("VPSRAD $12, Y%d, Y%d", j, j)
			}
			fmt.Fprintf(&s.Buffer, "norect%d:\n", s.label)
			for j := 0; j < 8; j++ {
				s.f("VMOVDQU Y%d, %s", j, at(b+j))
			}
		}
		s.invConsts(&ct)
		s.invBody(p, &ct)
		s.f("MOVQ dst+16(FP), DX")
		s.f("MOVQ dstStride+24(FP), R9")
		for b := 0; b < n; b += 8 {
			for j := 0; j < 8; j++ {
				s.f("VMOVDQU %s, Y%d", at(p.outs[b+j]), j)
			}
			s.transpose()
			s.invPost()
			s.f("MOVQ DX, DI")
			for j := 0; j < 8; j++ {
				s.invRound(j)
				s.f("VMOVDQU Y%d, %d(DI)", j, b*4)
				if j < 7 {
					s.f("ADDQ R9, DI")
				}
			}
		}
		s.f("VZEROUPPER")
		s.f("RET")
		fmt.Fprintf(&g, "\n//go:noescape\nfunc %sAVX2(%s)\n", name, invArgs)
		fmt.Fprintf(&rows, "\tcase %s<<3 | %d:\n\t\t%sAVX2(src, srcStride, dst, dstStride, lo, hi, rnd, shift, clo, chi, rect)\n", k.kind, k.log2, name)
	}
	_, _ = s.WriteString("\n")
	for i, v := range ct.vals {
		fmt.Fprintf(&s.Buffer, "DATA invc<>+%d(SB)/4, $%d\n", i*4, v)
	}
	fmt.Fprintf(&s.Buffer, "GLOBL invc<>(SB), RODATA|NOPTR, $%d\n", len(ct.vals)*4)
	fmt.Fprintf(&g, `
// invColAVX2 is the column pass of a 1D inverse transform of a kind and
// log2 of the size on eight adjacent columns (rows srcStride bytes apart,
// from src): transformed with its intermediate results clamped to [lo,
// hi], then each rounded down by shift (rnd its rounding) and clamped to
// [clo, chi] into dst's rows. It reports whether there is a kernel.
func invColAVX2(kind, log2 int, %s) bool {
	switch kind<<3 | log2 {
%s	default:
		return false
	}
	return true
}

// invRowAVX2 is the row pass: eight rows of min(size, 32) coefficients
// (srcStride bytes apart, from src), the rest zero, scaled by 2896/4096
// first when rect is not 0, transformed, rounded and clamped as
// invColAVX2's into eight rows of dst. It reports whether there is a
// kernel.
func invRowAVX2(kind, log2 int, %s) bool {
	switch kind<<3 | log2 {
%s	default:
		return false
	}
	return true
}

// invAVX2 reports whether there are kernels for an inverse transform of
// a kind and log2 of the size.
func invAVX2(kind, log2 int) bool {
	return (kind == tx1dDCT && log2 >= 3) || (kind == tx1dADST && (log2 == 3 || log2 == 4))
}
`, invArgs, cols.String(), invArgs, rows.String())
	if err := os.WriteFile(filepath.Join(dir, "invtx_amd64.s"), s.Bytes(), 0o644); err != nil { //nolint:gosec // Go source, read by everyone
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "invtx_amd64.go"), g.Bytes(), 0o644); err != nil { //nolint:gosec // Go source, read by everyone
		log.Fatal(err)
	}
}
