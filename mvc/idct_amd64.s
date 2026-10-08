//go:build amd64 && !purego

#include "textflag.h"

// Inverse transforms (AVX2) on the transposed coefficient layout: memory
// row j of a block holds column j, so the first 1-D pass runs on loaded
// rows, followed by one transpose and the second pass. Coefficients are
// cleared after use.

DATA idC32<>+0(SB)/4, $0x00200020
GLOBL idC32<>(SB), RODATA, $4

// IDCT4 is the 4-point inverse transform on four vectors (in place).
#define IDCT4(d0, d1, d2, d3, t0, t1, t2) \
	VPADDW d2, d0, t0; \
	VPSUBW d2, d0, d0; \
	VPSRAW $1, d1, t1; \
	VPSUBW d3, t1, t1; \
	VPSRAW $1, d3, t2; \
	VPADDW d1, t2, t2; \
	VPADDW t2, t0, d2; \
	VPSUBW t2, t0, d3; \
	VPADDW t1, d0, d1; \
	VPSUBW t1, d0, d0; \
	VMOVDQA d2, t0; \
	VMOVDQA d0, d2; \
	VMOVDQA t0, d0

// TRANSPOSE4 transposes 4x4 word blocks held in 4 vectors (per 64-bit
// group of lanes), result in o0..o3.
#define TRANSPOSE4(r0, r1, r2, r3, o0, o1, o2, o3, t0, t1) \
	VPUNPCKLWD r1, r0, o0; \
	VPUNPCKHWD r1, r0, o1; \
	VPUNPCKLWD r3, r2, o2; \
	VPUNPCKHWD r3, r2, o3; \
	VPUNPCKLDQ o2, o0, t0; \
	VPUNPCKHDQ o2, o0, t1; \
	VPUNPCKLDQ o3, o1, o2; \
	VPUNPCKHDQ o3, o1, o3; \
	VPUNPCKLQDQ o2, t0, o0; \
	VPUNPCKHQDQ o2, t0, o1; \
	VPUNPCKLQDQ o3, t1, o2; \
	VPUNPCKHQDQ o3, t1, o3

// ADDROW16 adds residual words in ry (already scaled) to 16 pixels at
// (DI); rx/tx are the XMM names of ry/ty.
#define ADDROW16(ry, rx, ty, tx) \
	VPMOVZXBW (DI), ty; \
	VPADDW ty, ry, ry; \
	VEXTRACTI128 $1, ry, tx; \
	VPACKUSWB tx, rx, rx; \
	VMOVDQU rx, (DI)

// ROUND6 computes (r+32)>>6 with saturation.
#define ROUND6(r, c32) \
	VPADDSW c32, r, r; \
	VPSRAW $6, r, r

// func idctRow4Asm(dst *byte, stride int, c *int16)
// Four 4x4 blocks in a row (coefficient row stride 16 words).
TEXT ·idctRow4Asm(SB), NOSPLIT, $0-24
	MOVQ dst+0(FP), DI
	MOVQ stride+8(FP), R8
	MOVQ c+16(FP), SI
	VMOVDQU (SI), Y0
	VMOVDQU 32(SI), Y1
	VMOVDQU 64(SI), Y2
	VMOVDQU 96(SI), Y3
	VPXOR Y15, Y15, Y15
	VMOVDQU Y15, (SI)
	VMOVDQU Y15, 32(SI)
	VMOVDQU Y15, 64(SI)
	VMOVDQU Y15, 96(SI)
	VPBROADCASTD idC32<>(SB), Y14
	IDCT4(Y0, Y1, Y2, Y3, Y4, Y5, Y6)
	TRANSPOSE4(Y0, Y1, Y2, Y3, Y7, Y8, Y9, Y10, Y4, Y5)
	IDCT4(Y7, Y8, Y9, Y10, Y4, Y5, Y6)
	ROUND6(Y7, Y14)
	ROUND6(Y8, Y14)
	ROUND6(Y9, Y14)
	ROUND6(Y10, Y14)
	ADDROW16(Y7, X7, Y4, X4)
	ADDQ R8, DI
	ADDROW16(Y8, X8, Y4, X4)
	ADDQ R8, DI
	ADDROW16(Y9, X9, Y4, X4)
	ADDQ R8, DI
	ADDROW16(Y10, X10, Y4, X4)
	VZEROUPPER
	RET

// IDCT8 is the 8-point inverse transform on d0..d7 (in place); t0..t3
// are scratch. Register pressure: 12 vectors.
#define IDCT8(d0, d1, d2, d3, d4, d5, d6, d7, t0, t1, t2, t3) \
	VPADDW d4, d0, t0; \
	VPSUBW d4, d0, t1; \
	VPSRAW $1, d2, t2; \
	VPSUBW d6, t2, t2; \
	VPSRAW $1, d6, t3; \
	VPADDW d2, t3, t3; \
	VPADDW t3, t0, d0; \
	VPSUBW t3, t0, d6; \
	VPADDW t2, t1, d2; \
	VPSUBW t2, t1, d4; \
	VPSUBW d3, d5, t0; \
	VPSUBW d7, t0, t0; \
	VPSRAW $1, d7, t1; \
	VPSUBW t1, t0, t0; \
	VPADDW d7, d1, t1; \
	VPSUBW d3, t1, t1; \
	VPSRAW $1, d3, t2; \
	VPSUBW t2, t1, t1; \
	VPSUBW d1, d7, t2; \
	VPADDW d5, t2, t2; \
	VPSRAW $1, d5, t3; \
	VPADDW t3, t2, t2; \
	VPADDW d5, d3, t3; \
	VPADDW d1, t3, t3; \
	VPSRAW $1, d1, d1; \
	VPADDW d1, t3, t3; \
	VPSRAW $2, t3, d1; \
	VPADDW t0, d1, d1; \
	VPSRAW $2, t0, d7; \
	VPSUBW d7, t3, d7; \
	VPSRAW $2, t2, d3; \
	VPADDW t1, d3, d3; \
	VPSRAW $2, t1, d5; \
	VPSUBW t2, d5, d5; \
	VPADDW d7, d0, t0; \
	VPSUBW d7, d0, d7; \
	VPADDW d5, d2, t1; \
	VPSUBW d5, d2, d2; \
	VPADDW d3, d4, t2; \
	VPSUBW d3, d4, d5; \
	VPADDW d1, d6, t3; \
	VPSUBW d1, d6, d4; \
	VMOVDQA t0, d0; \
	VMOVDQA t1, d1; \
	VMOVDQA d2, d6; \
	VMOVDQA t2, d2; \
	VMOVDQA t3, d3

// TRANSPOSE8 transposes 8x8 word blocks in r0..r7 (per 128-bit lane) into
// o0..o7.
#define TRANSPOSE8(r0, r1, r2, r3, r4, r5, r6, r7, o0, o1, o2, o3, o4, o5, o6, o7) \
	VPUNPCKLWD r1, r0, o0; \
	VPUNPCKHWD r1, r0, o1; \
	VPUNPCKLWD r3, r2, o2; \
	VPUNPCKHWD r3, r2, o3; \
	VPUNPCKLWD r5, r4, o4; \
	VPUNPCKHWD r5, r4, o5; \
	VPUNPCKLWD r7, r6, o6; \
	VPUNPCKHWD r7, r6, o7; \
	VPUNPCKLDQ o2, o0, r0; \
	VPUNPCKHDQ o2, o0, r1; \
	VPUNPCKLDQ o3, o1, r2; \
	VPUNPCKHDQ o3, o1, r3; \
	VPUNPCKLDQ o6, o4, r4; \
	VPUNPCKHDQ o6, o4, r5; \
	VPUNPCKLDQ o7, o5, r6; \
	VPUNPCKHDQ o7, o5, r7; \
	VPUNPCKLQDQ r4, r0, o0; \
	VPUNPCKHQDQ r4, r0, o1; \
	VPUNPCKLQDQ r5, r1, o2; \
	VPUNPCKHQDQ r5, r1, o3; \
	VPUNPCKLQDQ r6, r2, o4; \
	VPUNPCKHQDQ r6, r2, o5; \
	VPUNPCKLQDQ r7, r3, o6; \
	VPUNPCKHQDQ r7, r3, o7

// func idct8x8RowAsm(dst *byte, stride int, c *int16)
// Two 8x8 blocks in a row (coefficient row stride 16 words).
TEXT ·idct8x8RowAsm(SB), NOSPLIT, $0-24
	MOVQ dst+0(FP), DI
	MOVQ stride+8(FP), R8
	MOVQ c+16(FP), SI
	VMOVDQU (SI), Y0
	VMOVDQU 32(SI), Y1
	VMOVDQU 64(SI), Y2
	VMOVDQU 96(SI), Y3
	VMOVDQU 128(SI), Y4
	VMOVDQU 160(SI), Y5
	VMOVDQU 192(SI), Y6
	VMOVDQU 224(SI), Y7
	VPXOR Y15, Y15, Y15
	VMOVDQU Y15, (SI)
	VMOVDQU Y15, 32(SI)
	VMOVDQU Y15, 64(SI)
	VMOVDQU Y15, 96(SI)
	VMOVDQU Y15, 128(SI)
	VMOVDQU Y15, 160(SI)
	VMOVDQU Y15, 192(SI)
	VMOVDQU Y15, 224(SI)
	IDCT8(Y0, Y1, Y2, Y3, Y4, Y5, Y6, Y7, Y8, Y9, Y10, Y11)
	TRANSPOSE8(Y0, Y1, Y2, Y3, Y4, Y5, Y6, Y7, Y8, Y9, Y10, Y11, Y12, Y13, Y14, Y15)
	IDCT8(Y8, Y9, Y10, Y11, Y12, Y13, Y14, Y15, Y0, Y1, Y2, Y3)
	VPBROADCASTD idC32<>(SB), Y0
	ROUND6(Y8, Y0)
	ROUND6(Y9, Y0)
	ROUND6(Y10, Y0)
	ROUND6(Y11, Y0)
	ROUND6(Y12, Y0)
	ROUND6(Y13, Y0)
	ROUND6(Y14, Y0)
	ROUND6(Y15, Y0)
	ADDROW16(Y8, X8, Y1, X1)
	ADDQ R8, DI
	ADDROW16(Y9, X9, Y1, X1)
	ADDQ R8, DI
	ADDROW16(Y10, X10, Y1, X1)
	ADDQ R8, DI
	ADDROW16(Y11, X11, Y1, X1)
	ADDQ R8, DI
	ADDROW16(Y12, X12, Y1, X1)
	ADDQ R8, DI
	ADDROW16(Y13, X13, Y1, X1)
	ADDQ R8, DI
	ADDROW16(Y14, X14, Y1, X1)
	ADDQ R8, DI
	ADDROW16(Y15, X15, Y1, X1)
	VZEROUPPER
	RET

// ADDROW8 adds 8 residual words in r (XMM) to 8 pixels at (DI).
#define ADDROW8(r, t) \
	VPMOVZXBW (DI), t; \
	VPADDW t, r, r; \
	VPACKUSWB r, r, r; \
	VMOVQ r, (DI)

// func idct8x8AddAsm(dst *byte, stride int, c *int16, cs int)
// One 8x8 block with coefficient row stride cs words.
TEXT ·idct8x8AddAsm(SB), NOSPLIT, $0-32
	MOVQ dst+0(FP), DI
	MOVQ stride+8(FP), R8
	MOVQ c+16(FP), SI
	MOVQ cs+24(FP), R9
	ADDQ R9, R9               // bytes per row
	LEAQ (R9)(R9*2), R10      // 3 rows
	VPXOR X15, X15, X15
	VMOVDQU (SI), X0
	VMOVDQU X15, (SI)
	VMOVDQU (SI)(R9*1), X1
	VMOVDQU X15, (SI)(R9*1)
	VMOVDQU (SI)(R9*2), X2
	VMOVDQU X15, (SI)(R9*2)
	VMOVDQU (SI)(R10*1), X3
	VMOVDQU X15, (SI)(R10*1)
	LEAQ (SI)(R9*4), SI
	VMOVDQU (SI), X4
	VMOVDQU X15, (SI)
	VMOVDQU (SI)(R9*1), X5
	VMOVDQU X15, (SI)(R9*1)
	VMOVDQU (SI)(R9*2), X6
	VMOVDQU X15, (SI)(R9*2)
	VMOVDQU (SI)(R10*1), X7
	VMOVDQU X15, (SI)(R10*1)
	IDCT8(X0, X1, X2, X3, X4, X5, X6, X7, X8, X9, X10, X11)
	TRANSPOSE8(X0, X1, X2, X3, X4, X5, X6, X7, X8, X9, X10, X11, X12, X13, X14, X15)
	IDCT8(X8, X9, X10, X11, X12, X13, X14, X15, X0, X1, X2, X3)
	VPBROADCASTD idC32<>(SB), X0
	ROUND6(X8, X0)
	ROUND6(X9, X0)
	ROUND6(X10, X0)
	ROUND6(X11, X0)
	ROUND6(X12, X0)
	ROUND6(X13, X0)
	ROUND6(X14, X0)
	ROUND6(X15, X0)
	ADDROW8(X8, X1)
	ADDQ R8, DI
	ADDROW8(X9, X1)
	ADDQ R8, DI
	ADDROW8(X10, X1)
	ADDQ R8, DI
	ADDROW8(X11, X1)
	ADDQ R8, DI
	ADDROW8(X12, X1)
	ADDQ R8, DI
	ADDROW8(X13, X1)
	ADDQ R8, DI
	ADDROW8(X14, X1)
	ADDQ R8, DI
	ADDROW8(X15, X1)
	RET

// func idctChromaAsm(cb, cr *byte, stride int, c *int16)
// Both chroma components: c holds Cb (64 words, row stride 8) then Cr.
// Each pass handles one row of 4x4 blocks of both components (16 lanes:
// Cb block 0, Cb block 1, Cr block 0, Cr block 1).
TEXT ·idctChromaAsm(SB), NOSPLIT, $0-32
	MOVQ cb+0(FP), DI
	MOVQ cr+8(FP), DX
	MOVQ stride+16(FP), R8
	MOVQ c+24(FP), SI
	VPXOR Y15, Y15, Y15
	VPBROADCASTD idC32<>(SB), Y14
	MOVQ $2, R9
cr:
	// rows 0..3 of the block row: Cb words at (SI), Cr at 128(SI)
	VMOVDQU (SI), X0
	VINSERTI128 $1, 128(SI), Y0, Y0
	VMOVDQU 16(SI), X1
	VINSERTI128 $1, 144(SI), Y1, Y1
	VMOVDQU 32(SI), X2
	VINSERTI128 $1, 160(SI), Y2, Y2
	VMOVDQU 48(SI), X3
	VINSERTI128 $1, 176(SI), Y3, Y3
	VMOVDQU Y15, (SI)
	VMOVDQU Y15, 32(SI)
	VMOVDQU Y15, 128(SI)
	VMOVDQU Y15, 160(SI)
	IDCT4(Y0, Y1, Y2, Y3, Y4, Y5, Y6)
	TRANSPOSE4(Y0, Y1, Y2, Y3, Y7, Y8, Y9, Y10, Y4, Y5)
	IDCT4(Y7, Y8, Y9, Y10, Y4, Y5, Y6)
	ROUND6(Y7, Y14)
	ROUND6(Y8, Y14)
	ROUND6(Y9, Y14)
	ROUND6(Y10, Y14)
	// add: lane 0 -> Cb row, lane 1 -> Cr row
#define ADDCHROMA(ry, rx, ty, tx) \
	VMOVQ (DI), tx; \
	VPINSRQ $1, (DX), tx, tx; \
	VPMOVZXBW tx, ty; \
	VPADDW ty, ry, ry; \
	VEXTRACTI128 $1, ry, tx; \
	VPACKUSWB tx, rx, rx; \
	VMOVQ rx, (DI); \
	VPEXTRQ $1, rx, (DX)
	ADDCHROMA(Y7, X7, Y4, X4)
	ADDQ R8, DI
	ADDQ R8, DX
	ADDCHROMA(Y8, X8, Y4, X4)
	ADDQ R8, DI
	ADDQ R8, DX
	ADDCHROMA(Y9, X9, Y4, X4)
	ADDQ R8, DI
	ADDQ R8, DX
	ADDCHROMA(Y10, X10, Y4, X4)
	ADDQ R8, DI
	ADDQ R8, DX
	ADDQ $64, SI
	DECQ R9
	JNE cr
	VZEROUPPER
	RET

// func addConst16Asm(dst *byte, stride int, v *int16, rows int)
TEXT ·addConst16Asm(SB), NOSPLIT, $0-32
	MOVQ dst+0(FP), DI
	MOVQ stride+8(FP), R8
	MOVQ v+16(FP), SI
	MOVQ rows+24(FP), CX
	VMOVDQU (SI), Y0
ac:
	VPMOVZXBW (DI), Y1
	VPADDW Y0, Y1, Y1
	VEXTRACTI128 $1, Y1, X2
	VPACKUSWB X2, X1, X1
	VMOVDQU X1, (DI)
	ADDQ R8, DI
	DECQ CX
	JNE ac
	VZEROUPPER
	RET

// func chromaAddDCAsm(cb, cr *byte, stride int, dc *int16)
// dc holds the 2x2 DC-only residual constants of Cb (4 words) then Cr:
// rows 0-3 of each plane get dc[0] (left half) and dc[1] (right half),
// rows 4-7 dc[2] and dc[3].
TEXT ·chromaAddDCAsm(SB), NOSPLIT, $0-32
	MOVQ cb+0(FP), DI
	MOVQ cr+8(FP), DX
	MOVQ stride+16(FP), R8
	MOVQ dc+24(FP), SI
	MOVQ $2, R9
cd:
	// lanes 0-3 dc[c][2h], 4-7 dc[c][2h+1] for Cb (X0) and Cr (X1)
	VPBROADCASTW (SI), X0
	VPBROADCASTW 2(SI), X2
	VPBLENDW $0xf0, X2, X0, X0
	VPBROADCASTW 8(SI), X1
	VPBROADCASTW 10(SI), X2
	VPBLENDW $0xf0, X2, X1, X1
	VINSERTI128 $1, X1, Y0, Y0
	MOVQ $4, CX
cdr:
	VMOVQ (DI), X1
	VPINSRQ $1, (DX), X1, X1
	VPMOVZXBW X1, Y1
	VPADDW Y0, Y1, Y1
	VEXTRACTI128 $1, Y1, X2
	VPACKUSWB X2, X1, X1
	VMOVQ X1, (DI)
	VPEXTRQ $1, X1, (DX)
	ADDQ R8, DI
	ADDQ R8, DX
	DECQ CX
	JNE cdr
	ADDQ $4, SI
	DECQ R9
	JNE cd
	VZEROUPPER
	RET
