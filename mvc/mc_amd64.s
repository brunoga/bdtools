//go:build amd64 && !purego

#include "textflag.h"

// Motion compensation kernels (AVX2). Blocks are w x h with w in {4,8,16}
// (chroma: {4,8}); all pointers address the top-left sample. Sources are
// read with the 6-tap margins (2 left/top, 3 right/bottom) and up to 15
// bytes past the row end, which the picture padding guarantees.

DATA mcK1<>+0(SB)/4, $0xfffb0001 // (1, -5) as int16 pairs
GLOBL mcK1<>(SB), RODATA, $4
DATA mcK2<>+0(SB)/4, $0x00140014 // (20, 20)
GLOBL mcK2<>(SB), RODATA, $4
DATA mcK3<>+0(SB)/4, $0x0001fffb // (-5, 1)
GLOBL mcK3<>(SB), RODATA, $4
DATA mcC16<>+0(SB)/4, $0x00100010 // 16 as int16 pairs
GLOBL mcC16<>(SB), RODATA, $4
DATA mcC32<>+0(SB)/4, $0x00200020
GLOBL mcC32<>(SB), RODATA, $4
DATA mcC512<>+0(SB)/4, $0x00000200 // 512 as int32
GLOBL mcC512<>(SB), RODATA, $4

// TAP6 computes a+f - 5(b+e) + 20(c+d) into o, using t1, t2 as scratch.
// Works for X or Y registers.
#define TAP6(a, b, c, d, e, f, o, t1, t2) \
	VPADDW f, a, o; \
	VPADDW e, b, t1; \
	VPADDW d, c, t2; \
	VPSLLW $2, t1, f; \
	VPADDW f, t1, t1; \
	VPSUBW t1, o, o; \
	VPSLLW $2, t2, t1; \
	VPSLLW $4, t2, t2; \
	VPADDW t1, t2, t2; \
	VPADDW t2, o, o

// func mcCopyAsm(dst *byte, ds int, src *byte, ss int, w, h int)
TEXT ·mcCopyAsm(SB), NOSPLIT, $0-48
	MOVQ dst+0(FP), DI
	MOVQ ds+8(FP), R8
	MOVQ src+16(FP), SI
	MOVQ ss+24(FP), R9
	MOVQ w+32(FP), AX
	MOVQ h+40(FP), CX
	CMPQ AX, $16
	JEQ c16
	CMPQ AX, $8
	JEQ c8
c4:
	MOVL (SI), DX
	MOVL DX, (DI)
	ADDQ R9, SI
	ADDQ R8, DI
	DECQ CX
	JNE c4
	RET
c8:
	MOVQ (SI), DX
	MOVQ DX, (DI)
	ADDQ R9, SI
	ADDQ R8, DI
	DECQ CX
	JNE c8
	RET
c16:
	VMOVDQU (SI), X0
	VMOVDQU X0, (DI)
	ADDQ R9, SI
	ADDQ R8, DI
	DECQ CX
	JNE c16
	RET

// func mcHAsm(dst *byte, ds int, src *byte, ss int, w, h int)
// Horizontal half-sample positions (b).
TEXT ·mcHAsm(SB), NOSPLIT, $0-48
	MOVQ dst+0(FP), DI
	MOVQ ds+8(FP), R8
	MOVQ src+16(FP), SI
	MOVQ ss+24(FP), R9
	MOVQ w+32(FP), AX
	MOVQ h+40(FP), CX
	VPBROADCASTD mcC16<>(SB), Y7
	CMPQ AX, $16
	JNE h8
h16:
	VPMOVZXBW -2(SI), Y0
	VPMOVZXBW -1(SI), Y1
	VPMOVZXBW (SI), Y2
	VPMOVZXBW 1(SI), Y3
	VPMOVZXBW 2(SI), Y4
	VPMOVZXBW 3(SI), Y5
	TAP6(Y0, Y1, Y2, Y3, Y4, Y5, Y6, Y8, Y9)
	VPADDW Y7, Y6, Y6
	VPSRAW $5, Y6, Y6
	VEXTRACTI128 $1, Y6, X8
	VPACKUSWB X8, X6, X6
	VMOVDQU X6, (DI)
	ADDQ R9, SI
	ADDQ R8, DI
	DECQ CX
	JNE h16
	VZEROUPPER
	RET
h8:
	VPMOVZXBW -2(SI), X0
	VPMOVZXBW -1(SI), X1
	VPMOVZXBW (SI), X2
	VPMOVZXBW 1(SI), X3
	VPMOVZXBW 2(SI), X4
	VPMOVZXBW 3(SI), X5
	TAP6(X0, X1, X2, X3, X4, X5, X6, X8, X9)
	VPADDW X7, X6, X6
	VPSRAW $5, X6, X6
	VPACKUSWB X6, X6, X6
	CMPQ AX, $8
	JNE h4
	VMOVQ X6, (DI)
	JMP h8n
h4:
	VMOVD X6, (DI)
h8n:
	ADDQ R9, SI
	ADDQ R8, DI
	DECQ CX
	JNE h8
	VZEROUPPER
	RET

// func mcVAsm(dst *byte, ds int, src *byte, ss int, w, h int)
// Vertical half-sample positions (h).
TEXT ·mcVAsm(SB), NOSPLIT, $0-48
	MOVQ dst+0(FP), DI
	MOVQ ds+8(FP), R8
	MOVQ src+16(FP), SI
	MOVQ ss+24(FP), R9
	MOVQ w+32(FP), AX
	MOVQ h+40(FP), CX
	VPBROADCASTD mcC16<>(SB), Y7
	LEAQ (R9)(R9*2), R10      // 3*ss
	MOVQ SI, DX
	SUBQ R9, DX
	SUBQ R9, DX               // row -2
	CMPQ AX, $16
	JNE v8
	VPMOVZXBW (DX), Y0
	VPMOVZXBW (DX)(R9*1), Y1
	VPMOVZXBW (DX)(R9*2), Y2
	VPMOVZXBW (DX)(R10*1), Y3
	LEAQ (DX)(R9*4), DX       // row 2
	VPMOVZXBW (DX), Y4
v16:
	VPMOVZXBW (DX)(R9*1), Y5  // row y+3
	ADDQ R9, DX
	VMOVDQA Y5, Y10
	TAP6(Y0, Y1, Y2, Y3, Y4, Y5, Y6, Y8, Y9)
	VPADDW Y7, Y6, Y6
	VPSRAW $5, Y6, Y6
	VEXTRACTI128 $1, Y6, X8
	VPACKUSWB X8, X6, X6
	VMOVDQU X6, (DI)
	VMOVDQA Y1, Y0
	VMOVDQA Y2, Y1
	VMOVDQA Y3, Y2
	VMOVDQA Y4, Y3
	VMOVDQA Y10, Y4
	ADDQ R8, DI
	DECQ CX
	JNE v16
	VZEROUPPER
	RET
v8:
	VPMOVZXBW (DX), X0
	VPMOVZXBW (DX)(R9*1), X1
	VPMOVZXBW (DX)(R9*2), X2
	VPMOVZXBW (DX)(R10*1), X3
	LEAQ (DX)(R9*4), DX
	VPMOVZXBW (DX), X4
v8l:
	VPMOVZXBW (DX)(R9*1), X5
	ADDQ R9, DX
	VMOVDQA X5, X10
	TAP6(X0, X1, X2, X3, X4, X5, X6, X8, X9)
	VPADDW X7, X6, X6
	VPSRAW $5, X6, X6
	VPACKUSWB X6, X6, X6
	CMPQ AX, $8
	JNE v4
	VMOVQ X6, (DI)
	JMP v8n
v4:
	VMOVD X6, (DI)
v8n:
	VMOVDQA X1, X0
	VMOVDQA X2, X1
	VMOVDQA X3, X2
	VMOVDQA X4, X3
	VMOVDQA X10, X4
	ADDQ R8, DI
	DECQ CX
	JNE v8l
	VZEROUPPER
	RET

// func mcJAsm(dst *byte, ds int, src *byte, ss int, w, h int)
// Centre half-sample positions (j): horizontal 6-tap to 16-bit
// intermediates for rows -2..h+2, then vertical 6-tap in 32 bits.
TEXT ·mcJAsm(SB), $704-48
	MOVQ dst+0(FP), DI
	MOVQ ds+8(FP), R8
	MOVQ src+16(FP), SI
	MOVQ ss+24(FP), R9
	MOVQ w+32(FP), AX
	MOVQ h+40(FP), CX
	MOVQ SI, DX
	SUBQ R9, DX
	SUBQ R9, DX               // row -2
	LEAQ 5(CX), R10           // rows to filter
	LEAQ tmp-704(SP), R11
	CMPQ AX, $16
	JNE j8p
j16p:
	VPMOVZXBW -2(DX), Y0
	VPMOVZXBW -1(DX), Y1
	VPMOVZXBW (DX), Y2
	VPMOVZXBW 1(DX), Y3
	VPMOVZXBW 2(DX), Y4
	VPMOVZXBW 3(DX), Y5
	TAP6(Y0, Y1, Y2, Y3, Y4, Y5, Y6, Y8, Y9)
	VMOVDQU Y6, (R11)
	ADDQ R9, DX
	ADDQ $32, R11
	DECQ R10
	JNE j16p
	JMP j2
j8p:
	VPMOVZXBW -2(DX), X0
	VPMOVZXBW -1(DX), X1
	VPMOVZXBW (DX), X2
	VPMOVZXBW 1(DX), X3
	VPMOVZXBW 2(DX), X4
	VPMOVZXBW 3(DX), X5
	TAP6(X0, X1, X2, X3, X4, X5, X6, X8, X9)
	VMOVDQU X6, (R11)
	ADDQ R9, DX
	ADDQ $32, R11
	DECQ R10
	JNE j8p
j2:
	// vertical pass
	LEAQ tmp-704(SP), R11
	VPBROADCASTD mcK1<>(SB), Y12
	VPBROADCASTD mcK2<>(SB), Y13
	VPBROADCASTD mcK3<>(SB), Y14
	VPBROADCASTD mcC512<>(SB), Y15
	CMPQ AX, $16
	JNE j8v
j16v:
	VMOVDQU (R11), Y0
	VMOVDQU 32(R11), Y1
	VMOVDQU 64(R11), Y2
	VMOVDQU 96(R11), Y3
	VMOVDQU 128(R11), Y4
	VMOVDQU 160(R11), Y5
	VPUNPCKLWD Y1, Y0, Y6
	VPUNPCKHWD Y1, Y0, Y7
	VPMADDWD Y12, Y6, Y6
	VPMADDWD Y12, Y7, Y7
	VPUNPCKLWD Y3, Y2, Y8
	VPUNPCKHWD Y3, Y2, Y9
	VPMADDWD Y13, Y8, Y8
	VPMADDWD Y13, Y9, Y9
	VPADDD Y8, Y6, Y6
	VPADDD Y9, Y7, Y7
	VPUNPCKLWD Y5, Y4, Y8
	VPUNPCKHWD Y5, Y4, Y9
	VPMADDWD Y14, Y8, Y8
	VPMADDWD Y14, Y9, Y9
	VPADDD Y8, Y6, Y6
	VPADDD Y9, Y7, Y7
	VPADDD Y15, Y6, Y6
	VPADDD Y15, Y7, Y7
	VPSRAD $10, Y6, Y6
	VPSRAD $10, Y7, Y7
	VPACKSSDW Y7, Y6, Y6
	VEXTRACTI128 $1, Y6, X8
	VPACKUSWB X8, X6, X6
	VMOVDQU X6, (DI)
	ADDQ $32, R11
	ADDQ R8, DI
	DECQ CX
	JNE j16v
	VZEROUPPER
	RET
j8v:
	VMOVDQU (R11), X0
	VMOVDQU 32(R11), X1
	VMOVDQU 64(R11), X2
	VMOVDQU 96(R11), X3
	VMOVDQU 128(R11), X4
	VMOVDQU 160(R11), X5
	VPUNPCKLWD X1, X0, X6
	VPUNPCKHWD X1, X0, X7
	VPMADDWD X12, X6, X6
	VPMADDWD X12, X7, X7
	VPUNPCKLWD X3, X2, X8
	VPUNPCKHWD X3, X2, X9
	VPMADDWD X13, X8, X8
	VPMADDWD X13, X9, X9
	VPADDD X8, X6, X6
	VPADDD X9, X7, X7
	VPUNPCKLWD X5, X4, X8
	VPUNPCKHWD X5, X4, X9
	VPMADDWD X14, X8, X8
	VPMADDWD X14, X9, X9
	VPADDD X8, X6, X6
	VPADDD X9, X7, X7
	VPADDD X15, X6, X6
	VPADDD X15, X7, X7
	VPSRAD $10, X6, X6
	VPSRAD $10, X7, X7
	VPACKSSDW X7, X6, X6
	VPACKUSWB X6, X6, X6
	CMPQ AX, $8
	JNE j4s
	VMOVQ X6, (DI)
	JMP j8n
j4s:
	VMOVD X6, (DI)
j8n:
	ADDQ $32, R11
	ADDQ R8, DI
	DECQ CX
	JNE j8v
	VZEROUPPER
	RET

// func mcAvgAsm(dst *byte, ds int, a *byte, as int, b *byte, bs int, w, h int)
TEXT ·mcAvgAsm(SB), NOSPLIT, $0-64
	MOVQ dst+0(FP), DI
	MOVQ ds+8(FP), R8
	MOVQ a+16(FP), SI
	MOVQ as+24(FP), R9
	MOVQ b+32(FP), DX
	MOVQ bs+40(FP), R10
	MOVQ w+48(FP), AX
	MOVQ h+56(FP), CX
	CMPQ AX, $16
	JNE a8
a16:
	VMOVDQU (SI), X0
	VPAVGB (DX), X0, X0
	VMOVDQU X0, (DI)
	ADDQ R9, SI
	ADDQ R10, DX
	ADDQ R8, DI
	DECQ CX
	JNE a16
	RET
a8:
	VMOVQ (SI), X0
	VMOVQ (DX), X1
	VPAVGB X1, X0, X0
	CMPQ AX, $8
	JNE a4
	VMOVQ X0, (DI)
	JMP a8n
a4:
	VMOVD X0, (DI)
a8n:
	ADDQ R9, SI
	ADDQ R10, DX
	ADDQ R8, DI
	DECQ CX
	JNE a8
	RET

// func mcW1Asm(dst *byte, ds int, src *byte, ss int, w, h int, wt, rnd, logWD, off int)
// Explicit weighted prediction from one list: ((p*wt + rnd) >> logWD) + off.
TEXT ·mcW1Asm(SB), NOSPLIT, $0-80
	MOVQ dst+0(FP), DI
	MOVQ ds+8(FP), R8
	MOVQ src+16(FP), SI
	MOVQ ss+24(FP), R9
	MOVQ w+32(FP), AX
	MOVQ h+40(FP), CX
	MOVQ wt+48(FP), DX
	VMOVQ DX, X5
	VPBROADCASTW X5, Y5
	MOVQ rnd+56(FP), DX
	VMOVQ DX, X6
	VPBROADCASTW X6, Y6
	MOVQ logWD+64(FP), DX
	VMOVQ DX, X7
	MOVQ off+72(FP), DX
	VMOVQ DX, X8
	VPBROADCASTW X8, Y8
	CMPQ AX, $16
	JNE w8
w16:
	VPMOVZXBW (SI), Y0
	VPMULLW Y5, Y0, Y0
	VPADDW Y6, Y0, Y0
	VPSRAW X7, Y0, Y0
	VPADDW Y8, Y0, Y0
	VEXTRACTI128 $1, Y0, X1
	VPACKUSWB X1, X0, X0
	VMOVDQU X0, (DI)
	ADDQ R9, SI
	ADDQ R8, DI
	DECQ CX
	JNE w16
	VZEROUPPER
	RET
w8:
	VPMOVZXBW (SI), X0
	VPMULLW X5, X0, X0
	VPADDW X6, X0, X0
	VPSRAW X7, X0, X0
	VPADDW X8, X0, X0
	VPACKUSWB X0, X0, X0
	CMPQ AX, $8
	JNE w4
	VMOVQ X0, (DI)
	JMP w8n
w4:
	VMOVD X0, (DI)
w8n:
	ADDQ R9, SI
	ADDQ R8, DI
	DECQ CX
	JNE w8
	VZEROUPPER
	RET

// func mcW2Asm(dst *byte, ds int, a *byte, as int, b *byte, bs int, w, h int, w01, rnd, sh, off int)
// Bi-predictive weighting: ((a*w0 + b*w1 + rnd) >> sh) + off, w01 = w0 | w1<<16.
TEXT ·mcW2Asm(SB), NOSPLIT, $0-96
	MOVQ dst+0(FP), DI
	MOVQ ds+8(FP), R8
	MOVQ a+16(FP), SI
	MOVQ as+24(FP), R9
	MOVQ b+32(FP), DX
	MOVQ bs+40(FP), R10
	MOVQ w+48(FP), AX
	MOVQ h+56(FP), CX
	MOVQ w01+64(FP), R11
	VMOVQ R11, X5
	VPBROADCASTD X5, Y5
	MOVQ rnd+72(FP), R11
	VMOVQ R11, X6
	VPBROADCASTD X6, Y6
	MOVQ sh+80(FP), R11
	VMOVQ R11, X7
	MOVQ off+88(FP), R11
	VMOVQ R11, X8
	VPBROADCASTW X8, Y8
	CMPQ AX, $16
	JNE b8
b16:
	VPMOVZXBW (SI), Y0
	VPMOVZXBW (DX), Y1
	VPUNPCKLWD Y1, Y0, Y2
	VPUNPCKHWD Y1, Y0, Y3
	VPMADDWD Y5, Y2, Y2
	VPMADDWD Y5, Y3, Y3
	VPADDD Y6, Y2, Y2
	VPADDD Y6, Y3, Y3
	VPSRAD X7, Y2, Y2
	VPSRAD X7, Y3, Y3
	VPACKSSDW Y3, Y2, Y2
	VPADDW Y8, Y2, Y2
	VEXTRACTI128 $1, Y2, X3
	VPACKUSWB X3, X2, X2
	VMOVDQU X2, (DI)
	ADDQ R9, SI
	ADDQ R10, DX
	ADDQ R8, DI
	DECQ CX
	JNE b16
	VZEROUPPER
	RET
b8:
	VPMOVZXBW (SI), X0
	VPMOVZXBW (DX), X1
	VPUNPCKLWD X1, X0, X2
	VPUNPCKHWD X1, X0, X3
	VPMADDWD X5, X2, X2
	VPMADDWD X5, X3, X3
	VPADDD X6, X2, X2
	VPADDD X6, X3, X3
	VPSRAD X7, X2, X2
	VPSRAD X7, X3, X3
	VPACKSSDW X3, X2, X2
	VPADDW X8, X2, X2
	VPACKUSWB X2, X2, X2
	CMPQ AX, $8
	JNE b4
	VMOVQ X2, (DI)
	JMP b8n
b4:
	VMOVD X2, (DI)
b8n:
	ADDQ R9, SI
	ADDQ R10, DX
	ADDQ R8, DI
	DECQ CX
	JNE b8
	VZEROUPPER
	RET

// func chromaMCAsm(dst *byte, ds int, src *byte, ss int, w, h int, wab, wcd int)
// Bilinear chroma interpolation; wab = wa | wb<<8, wcd = wc | wd<<8 (signed
// bytes), w in {4, 8}.
TEXT ·chromaMCAsm(SB), NOSPLIT, $0-64
	MOVQ dst+0(FP), DI
	MOVQ ds+8(FP), R8
	MOVQ src+16(FP), SI
	MOVQ ss+24(FP), R9
	MOVQ w+32(FP), AX
	MOVQ h+40(FP), CX
	MOVQ wab+48(FP), DX
	VMOVQ DX, X5
	VPBROADCASTW X5, X5
	MOVQ wcd+56(FP), DX
	VMOVQ DX, X6
	VPBROADCASTW X6, X6
	VPBROADCASTD mcC32<>(SB), X7
	VMOVDQU (SI), X0
	VMOVDQU 1(SI), X1
	VPUNPCKLBW X1, X0, X0     // pairs (s[x], s[x+1]) of row 0
cm:
	ADDQ R9, SI
	VMOVDQU (SI), X1
	VMOVDQU 1(SI), X2
	VPUNPCKLBW X2, X1, X1     // next row
	VPMADDUBSW X5, X0, X3
	VPMADDUBSW X6, X1, X4
	VPADDW X4, X3, X3
	VPADDW X7, X3, X3
	VPSRAW $6, X3, X3
	VPACKUSWB X3, X3, X3
	CMPQ AX, $8
	JNE cm4
	VMOVQ X3, (DI)
	JMP cmn
cm4:
	VMOVD X3, (DI)
cmn:
	VMOVDQA X1, X0
	ADDQ R8, DI
	DECQ CX
	JNE cm
	RET

// func xgetbvAsm() (eax, edx uint32)
TEXT ·xgetbvAsm(SB), NOSPLIT, $0-8
	XORL CX, CX
	XGETBV
	MOVL AX, eax+0(FP)
	MOVL DX, edx+4(FP)
	RET

// func chromaMC2Asm(cb, cr *byte, ds int, scb, scr *byte, ss int, w, h int, wab, wcd int)
// Bilinear chroma interpolation of both components, Cb in the low and Cr
// in the high 128-bit lane; wab = wa | wb<<8, wcd = wc | wd<<8 (signed
// bytes), w in {4, 8}. Full-sample positions copy, and vertical
// full-sample positions use the horizontal filter only.
TEXT ·chromaMC2Asm(SB), NOSPLIT, $0-80
	MOVQ cb+0(FP), DI
	MOVQ cr+8(FP), DX
	MOVQ ds+16(FP), R8
	MOVQ scb+24(FP), SI
	MOVQ scr+32(FP), R11
	MOVQ ss+40(FP), R9
	MOVQ w+48(FP), AX
	MOVQ h+56(FP), CX
	MOVQ wab+64(FP), BX
	MOVQ wcd+72(FP), R10
	TESTQ R10, R10
	JNE c2d
	CMPQ BX, $64
	JEQ c2copy
	// horizontal only
	VMOVQ BX, X5
	VPBROADCASTW X5, Y5
	VPBROADCASTD mcC32<>(SB), Y7
c2h:
	VMOVDQU (SI), X0
	VINSERTI128 $1, (R11), Y0, Y0
	VMOVDQU 1(SI), X1
	VINSERTI128 $1, 1(R11), Y1, Y1
	VPUNPCKLBW Y1, Y0, Y0     // pairs (s[x], s[x+1]) of both planes
	VPMADDUBSW Y5, Y0, Y3
	VPADDW Y7, Y3, Y3
	VPSRAW $6, Y3, Y3
	VPACKUSWB Y3, Y3, Y3
	VEXTRACTI128 $1, Y3, X4
	CMPQ AX, $8
	JNE c2h4
	VMOVQ X3, (DI)
	VMOVQ X4, (DX)
	JMP c2hn
c2h4:
	VMOVD X3, (DI)
	VMOVD X4, (DX)
c2hn:
	ADDQ R9, SI
	ADDQ R9, R11
	ADDQ R8, DI
	ADDQ R8, DX
	DECQ CX
	JNE c2h
	VZEROUPPER
	RET
c2copy:
	CMPQ AX, $8
	JNE c2copy4
c2c8:
	MOVQ (SI), BX
	MOVQ BX, (DI)
	MOVQ (R11), BX
	MOVQ BX, (DX)
	ADDQ R9, SI
	ADDQ R9, R11
	ADDQ R8, DI
	ADDQ R8, DX
	DECQ CX
	JNE c2c8
	RET
c2copy4:
	MOVL (SI), BX
	MOVL BX, (DI)
	MOVL (R11), BX
	MOVL BX, (DX)
	ADDQ R9, SI
	ADDQ R9, R11
	ADDQ R8, DI
	ADDQ R8, DX
	DECQ CX
	JNE c2copy4
	RET
c2d:
	VMOVQ BX, X5
	VPBROADCASTW X5, Y5
	VMOVQ R10, X6
	VPBROADCASTW X6, Y6
	VPBROADCASTD mcC32<>(SB), Y7
	VMOVDQU (SI), X0
	VINSERTI128 $1, (R11), Y0, Y0
	VMOVDQU 1(SI), X1
	VINSERTI128 $1, 1(R11), Y1, Y1
	VPUNPCKLBW Y1, Y0, Y0     // pairs of row 0
c2m:
	ADDQ R9, SI
	ADDQ R9, R11
	VMOVDQU (SI), X1
	VINSERTI128 $1, (R11), Y1, Y1
	VMOVDQU 1(SI), X2
	VINSERTI128 $1, 1(R11), Y2, Y2
	VPUNPCKLBW Y2, Y1, Y1     // next row
	VPMADDUBSW Y5, Y0, Y3
	VPMADDUBSW Y6, Y1, Y4
	VPADDW Y4, Y3, Y3
	VPADDW Y7, Y3, Y3
	VPSRAW $6, Y3, Y3
	VPACKUSWB Y3, Y3, Y3
	VEXTRACTI128 $1, Y3, X4
	CMPQ AX, $8
	JNE c2m4
	VMOVQ X3, (DI)
	VMOVQ X4, (DX)
	JMP c2mn
c2m4:
	VMOVD X3, (DI)
	VMOVD X4, (DX)
c2mn:
	VMOVDQA Y1, Y0
	ADDQ R8, DI
	ADDQ R8, DX
	DECQ CX
	JNE c2m
	VZEROUPPER
	RET
