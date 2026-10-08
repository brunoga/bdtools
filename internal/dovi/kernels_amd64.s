//go:build amd64 && !purego

#include "textflag.h"

// func cpuidAsm(leaf, sub uint32) (eax, ebx, ecx, edx uint32)
TEXT ·cpuidAsm(SB), NOSPLIT, $0-24
	MOVL leaf+0(FP), AX
	MOVL sub+4(FP), CX
	CPUID
	MOVL AX, eax+8(FP)
	MOVL BX, ebx+12(FP)
	MOVL CX, ecx+16(FP)
	MOVL DX, edx+20(FP)
	RET

// func xgetbvAsm() (eax, edx uint32)
TEXT ·xgetbvAsm(SB), NOSPLIT, $0-8
	XORL CX, CX
	XGETBV
	MOVL AX, eax+0(FP)
	MOVL DX, edx+4(FP)
	RET

// mmrTable offsets: coef[c][i] at c*672 + i*32, constant[c] at 1344+c*32,
// lo[c] at 1408+c*32, hi[c] at 1472+c*32.

// Accumulate one power of the seven terms (Y7-Y13) into Cb's (Y14) and
// Cr's (Y15) sums with coefficients i..i+6.
#define TERMS(i) \
	VFMADD231PS (i+0)*32(DI), Y7, Y14;      \
	VFMADD231PS 672+(i+0)*32(DI), Y7, Y15;  \
	VFMADD231PS (i+1)*32(DI), Y8, Y14;      \
	VFMADD231PS 672+(i+1)*32(DI), Y8, Y15;  \
	VFMADD231PS (i+2)*32(DI), Y9, Y14;      \
	VFMADD231PS 672+(i+2)*32(DI), Y9, Y15;  \
	VFMADD231PS (i+3)*32(DI), Y10, Y14;     \
	VFMADD231PS 672+(i+3)*32(DI), Y10, Y15; \
	VFMADD231PS (i+4)*32(DI), Y11, Y14;     \
	VFMADD231PS 672+(i+4)*32(DI), Y11, Y15; \
	VFMADD231PS (i+5)*32(DI), Y12, Y14;     \
	VFMADD231PS 672+(i+5)*32(DI), Y12, Y15; \
	VFMADD231PS (i+6)*32(DI), Y13, Y14;     \
	VFMADD231PS 672+(i+6)*32(DI), Y13, Y15

// Raise the powers (Y7-Y13) by one: times the terms (Y0-Y6).
#define POWER \
	VMULPS Y0, Y7, Y7;   \
	VMULPS Y1, Y8, Y8;   \
	VMULPS Y2, Y9, Y9;   \
	VMULPS Y3, Y10, Y10; \
	VMULPS Y4, Y11, Y11; \
	VMULPS Y5, Y12, Y12; \
	VMULPS Y6, Y13, Y13

// func mmrRowAVX2(t *mmrTable, ob, or, sy, sb, sr *float32, n int)
TEXT ·mmrRowAVX2(SB), NOSPLIT, $0-56
	MOVQ t+0(FP), DI
	MOVQ ob+8(FP), R8
	MOVQ or+16(FP), R9
	MOVQ sy+24(FP), R10
	MOVQ sb+32(FP), R11
	MOVQ sr+40(FP), R12
	MOVQ n+48(FP), CX
	XORQ AX, AX

loop:
	// The terms: y, b, r, yb, yr, br, ybr.
	VMOVUPS (R10)(AX*4), Y0
	VMOVUPS (R11)(AX*4), Y1
	VMOVUPS (R12)(AX*4), Y2
	VMULPS Y1, Y0, Y3
	VMULPS Y2, Y0, Y4
	VMULPS Y2, Y1, Y5
	VMULPS Y2, Y3, Y6
	VMOVAPS Y0, Y7
	VMOVAPS Y1, Y8
	VMOVAPS Y2, Y9
	VMOVAPS Y3, Y10
	VMOVAPS Y4, Y11
	VMOVAPS Y5, Y12
	VMOVAPS Y6, Y13
	VMOVUPS 1344(DI), Y14
	VMOVUPS 1376(DI), Y15
	TERMS(0)
	POWER
	TERMS(7)
	POWER
	TERMS(14)
	// Clamp and store.
	VMAXPS 1408(DI), Y14, Y14
	VMINPS 1472(DI), Y14, Y14
	VMAXPS 1440(DI), Y15, Y15
	VMINPS 1504(DI), Y15, Y15
	VMOVUPS Y14, (R8)(AX*4)
	VMOVUPS Y15, (R9)(AX*4)
	ADDQ $8, AX
	CMPQ AX, CX
	JLT loop
	VZEROUPPER
	RET

// Float constants, as bits.
#define F1023 $0x447fc000
#define FHALF $0x3f000000
#define FINV1023 $0x3a802008
#define FINV8184 $0x39002008

// BCAST(bits, X, Y) puts a 32-bit constant in all lanes of Y (through AX).
#define BCAST(bits, x, y) \
	MOVQ bits, AX;        \
	MOVQ AX, x;           \
	VPBROADCASTD x, y

// func verticalYAVX2(out *int32, r0, r1, r2, r3 *byte, w *[4]int32, n int)
// out[x] = sum of w[k] * (rk sample x), samples P010; n a multiple of 8.
TEXT ·verticalYAVX2(SB), NOSPLIT, $0-56
	MOVQ out+0(FP), DI
	MOVQ r0+8(FP), R8
	MOVQ r1+16(FP), R9
	MOVQ r2+24(FP), R10
	MOVQ r3+32(FP), R11
	MOVQ w+40(FP), SI
	MOVQ n+48(FP), CX
	VPBROADCASTD 0(SI), Y12
	VPBROADCASTD 4(SI), Y13
	VPBROADCASTD 8(SI), Y14
	VPBROADCASTD 12(SI), Y15
	XORQ AX, AX

vyloop:
	VPMOVZXWD (R8)(AX*2), Y0
	VPMOVZXWD (R9)(AX*2), Y1
	VPMOVZXWD (R10)(AX*2), Y2
	VPMOVZXWD (R11)(AX*2), Y3
	VPSRLD $6, Y0, Y0
	VPSRLD $6, Y1, Y1
	VPSRLD $6, Y2, Y2
	VPSRLD $6, Y3, Y3
	VPMULLD Y12, Y0, Y0
	VPMULLD Y13, Y1, Y1
	VPMULLD Y14, Y2, Y2
	VPMULLD Y15, Y3, Y3
	VPADDD Y1, Y0, Y0
	VPADDD Y3, Y2, Y2
	VPADDD Y2, Y0, Y0
	VMOVDQU Y0, (DI)(AX*4)
	ADDQ $8, AX
	CMPQ AX, CX
	JLT vyloop
	VZEROUPPER
	RET

// UVROW(reg, dst for Cb, dst for Cr): split 8 interleaved P010 pairs.
#define UVROW(src, cb, cr) \
	VMOVDQU (src)(AX*4), cr; \
	VPSLLD $16, cr, cb;      \
	VPSRLD $22, cb, cb;      \
	VPSRLD $22, cr, cr

// func verticalUVAVX2(ob, or *int32, r0, r1, r2, r3 *byte, w *[4]int32, n int)
TEXT ·verticalUVAVX2(SB), NOSPLIT, $0-64
	MOVQ ob+0(FP), DI
	MOVQ or+8(FP), DX
	MOVQ r0+16(FP), R8
	MOVQ r1+24(FP), R9
	MOVQ r2+32(FP), R10
	MOVQ r3+40(FP), R11
	MOVQ w+48(FP), SI
	MOVQ n+56(FP), CX
	VPBROADCASTD 0(SI), Y12
	VPBROADCASTD 4(SI), Y13
	VPBROADCASTD 8(SI), Y14
	VPBROADCASTD 12(SI), Y15
	XORQ AX, AX

vuvloop:
	UVROW(R8, Y0, Y1)
	VPMULLD Y12, Y0, Y8
	VPMULLD Y12, Y1, Y9
	UVROW(R9, Y0, Y1)
	VPMULLD Y13, Y0, Y0
	VPMULLD Y13, Y1, Y1
	VPADDD Y0, Y8, Y8
	VPADDD Y1, Y9, Y9
	UVROW(R10, Y0, Y1)
	VPMULLD Y14, Y0, Y0
	VPMULLD Y14, Y1, Y1
	VPADDD Y0, Y8, Y8
	VPADDD Y1, Y9, Y9
	UVROW(R11, Y0, Y1)
	VPMULLD Y15, Y0, Y0
	VPMULLD Y15, Y1, Y1
	VPADDD Y0, Y8, Y8
	VPADDD Y1, Y9, Y9
	VMOVDQU Y8, (DI)(AX*4)
	VMOVDQU Y9, (DX)(AX*4)
	ADDQ $8, AX
	CMPQ AX, CX
	JLT vuvloop
	VZEROUPPER
	RET

// func horizontalAVX2(out, in *int32, n int)
// For i in [0, n) (n a multiple of 8), with in[i-1] and in[i+2+7] readable:
// out[2i] = clamp((in[i]+64)>>7), out[2i+1] = clamp((9(in[i]+in[i+1]) -
// in[i-1] - in[i+2] + 1024)>>11), clamped to 0..1023.
TEXT ·horizontalAVX2(SB), NOSPLIT, $0-24
	MOVQ out+0(FP), DI
	MOVQ in+8(FP), SI
	MOVQ n+16(FP), CX
	BCAST($64, X10, Y10)
	BCAST($1024, X11, Y11)
	BCAST($1023, X12, Y12)
	VPXOR Y13, Y13, Y13
	XORQ AX, AX

hloop:
	VMOVDQU -4(SI)(AX*4), Y0 // in[i-1]
	VMOVDQU (SI)(AX*4), Y1   // in[i]
	VMOVDQU 4(SI)(AX*4), Y2  // in[i+1]
	VMOVDQU 8(SI)(AX*4), Y3  // in[i+2]
	// even
	VPADDD Y10, Y1, Y4
	VPSRAD $7, Y4, Y4
	VPMAXSD Y13, Y4, Y4
	VPMINSD Y12, Y4, Y4
	// odd
	VPADDD Y2, Y1, Y5
	VPSLLD $3, Y5, Y6
	VPADDD Y6, Y5, Y5
	VPSUBD Y0, Y5, Y5
	VPSUBD Y3, Y5, Y5
	VPADDD Y11, Y5, Y5
	VPSRAD $11, Y5, Y5
	VPMAXSD Y13, Y5, Y5
	VPMINSD Y12, Y5, Y5
	// interleave
	VPUNPCKLDQ Y5, Y4, Y6
	VPUNPCKHDQ Y5, Y4, Y7
	VPERM2I128 $0x20, Y7, Y6, Y8
	VPERM2I128 $0x31, Y7, Y6, Y9
	VMOVDQU Y8, (DI)(AX*8)
	VMOVDQU Y9, 32(DI)(AX*8)
	ADDQ $8, AX
	CMPQ AX, CX
	JLT hloop
	VZEROUPPER
	RET

// TOCODE(Y): a normalised float to a P010 sample in each int32 lane,
// clamped (Y9 1023.0, Y10 0.5, Y11 zero, Y12 1023).
#define TOCODE(y) \
	VMULPS Y9, y, y;     \
	VADDPS Y10, y, y;    \
	VCVTTPS2DQ y, y;     \
	VPMAXSD Y11, y, y;   \
	VPMINSD Y12, y, y;   \
	VPSLLD $6, y, y

// func lumaRowAVX2(out, bl *byte, el *int32, lut, res *float32, hasEL, n int)
// n a multiple of 16.
TEXT ·lumaRowAVX2(SB), NOSPLIT, $0-56
	MOVQ out+0(FP), DI
	MOVQ bl+8(FP), SI
	MOVQ el+16(FP), DX
	MOVQ lut+24(FP), R8
	MOVQ res+32(FP), R9
	MOVQ hasEL+40(FP), R12
	MOVQ n+48(FP), CX
	BCAST(F1023, X9, Y9)
	BCAST(FHALF, X10, Y10)
	VPXOR Y11, Y11, Y11
	BCAST($1023, X12, Y12)
	XORQ AX, AX

lloop:
	VPMOVZXWD (SI)(AX*2), Y0
	VPSRLD $6, Y0, Y0
	VPCMPEQD Y15, Y15, Y15
	VGATHERDPS Y15, (R8)(Y0*4), Y3
	TESTQ R12, R12
	JZ lnoel1
	VMOVDQU (DX)(AX*4), Y1
	VPCMPEQD Y15, Y15, Y15
	VGATHERDPS Y15, (R9)(Y1*4), Y2
	VADDPS Y2, Y3, Y3

lnoel1:
	TOCODE(Y3)
	VPMOVZXWD 16(SI)(AX*2), Y0
	VPSRLD $6, Y0, Y0
	VPCMPEQD Y15, Y15, Y15
	VGATHERDPS Y15, (R8)(Y0*4), Y4
	TESTQ R12, R12
	JZ lnoel2
	VMOVDQU 32(DX)(AX*4), Y1
	VPCMPEQD Y15, Y15, Y15
	VGATHERDPS Y15, (R9)(Y1*4), Y2
	VADDPS Y2, Y4, Y4

lnoel2:
	TOCODE(Y4)
	VPACKUSDW Y4, Y3, Y5
	VPERMQ $0xD8, Y5, Y5
	VMOVDQU Y5, (DI)(AX*2)
	ADDQ $16, AX
	CMPQ AX, CX
	JLT lloop
	VZEROUPPER
	RET

// func chromaStoreAVX2(out *byte, ob, or *float32, eb, er *int32, resB, resR *float32, hasEL, n int)
// n a multiple of 8.
TEXT ·chromaStoreAVX2(SB), NOSPLIT, $0-72
	MOVQ out+0(FP), DI
	MOVQ ob+8(FP), SI
	MOVQ or+16(FP), BX
	MOVQ eb+24(FP), DX
	MOVQ er+32(FP), R13
	MOVQ resB+40(FP), R8
	MOVQ resR+48(FP), R9
	MOVQ hasEL+56(FP), R12
	MOVQ n+64(FP), CX
	BCAST(F1023, X9, Y9)
	BCAST(FHALF, X10, Y10)
	VPXOR Y11, Y11, Y11
	BCAST($1023, X12, Y12)
	XORQ AX, AX

csloop:
	VMOVUPS (SI)(AX*4), Y3
	VMOVUPS (BX)(AX*4), Y4
	TESTQ R12, R12
	JZ csnoel
	VMOVDQU (DX)(AX*4), Y0
	VPCMPEQD Y15, Y15, Y15
	VGATHERDPS Y15, (R8)(Y0*4), Y2
	VADDPS Y2, Y3, Y3
	VMOVDQU (R13)(AX*4), Y0
	VPCMPEQD Y15, Y15, Y15
	VGATHERDPS Y15, (R9)(Y0*4), Y2
	VADDPS Y2, Y4, Y4

csnoel:
	TOCODE(Y3)
	TOCODE(Y4)
	VPSLLD $16, Y4, Y4
	VPOR Y4, Y3, Y3
	VMOVDQU Y3, (DI)(AX*4)
	ADDQ $8, AX
	CMPQ AX, CX
	JLT csloop
	VZEROUPPER
	RET

// LUMAPAIR(row, dst): the [1 2 1] luma sum at eight chroma samples from
// x = AX of a row (dwords of two P010 luma samples; the one before is at -4).
#define LUMAPAIR(row, dst) \
	VMOVDQU (row)(AX*4), Y1;   \
	VMOVDQU -4(row)(AX*4), Y2; \
	VPSLLD $16, Y1, Y3;        \
	VPSRLD $22, Y3, Y3;        \
	VPSRLD $22, Y1, Y1;        \
	VPSRLD $22, Y2, Y2;        \
	VPADDD Y3, Y3, Y3;         \
	VPADDD Y1, Y3, Y3;         \
	VPADDD Y2, Y3, dst

// func chromaPrepAVX2(sy, sb, sr *float32, y0, y1, uv *byte, n int)
// For chroma samples [0, n) (n a multiple of 8) with the luma sample before
// the first readable.
TEXT ·chromaPrepAVX2(SB), NOSPLIT, $0-56
	MOVQ sy+0(FP), DI
	MOVQ sb+8(FP), DX
	MOVQ sr+16(FP), BX
	MOVQ y0+24(FP), R8
	MOVQ y1+32(FP), R9
	MOVQ uv+40(FP), R10
	MOVQ n+48(FP), CX
	BCAST(FINV8184, X13, Y13)
	BCAST(FINV1023, X14, Y14)
	XORQ AX, AX

cploop:
	LUMAPAIR(R8, Y4)
	LUMAPAIR(R9, Y5)
	VPADDD Y5, Y4, Y4
	VCVTDQ2PS Y4, Y4
	VMULPS Y13, Y4, Y4
	VMOVUPS Y4, (DI)(AX*4)
	UVROW(R10, Y6, Y7)
	VCVTDQ2PS Y6, Y6
	VCVTDQ2PS Y7, Y7
	VMULPS Y14, Y6, Y6
	VMULPS Y14, Y7, Y7
	VMOVUPS Y6, (DX)(AX*4)
	VMOVUPS Y7, (BX)(AX*4)
	ADDQ $8, AX
	CMPQ AX, CX
	JLT cploop
	VZEROUPPER
	RET
