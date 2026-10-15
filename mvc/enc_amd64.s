//go:build amd64 && !purego

#include "textflag.h"

// func sadAsm(src *byte, ss int, ref *byte, rs int, w, h int) int
// Sum of absolute differences of a w x h block (w 8 or 16).
TEXT ·sadAsm(SB), NOSPLIT, $0-56
	MOVQ src+0(FP), SI
	MOVQ ss+8(FP), R8
	MOVQ ref+16(FP), DI
	MOVQ rs+24(FP), R9
	MOVQ w+32(FP), AX
	MOVQ h+40(FP), CX
	VPXOR X2, X2, X2
	CMPQ AX, $16
	JNE  s8
s16:
	VMOVDQU (SI), X0
	VPSADBW (DI), X0, X0
	VPADDQ  X0, X2, X2
	ADDQ    R8, SI
	ADDQ    R9, DI
	DECQ    CX
	JNE     s16
	JMP     done
s8:
	VMOVQ   (SI), X0
	VMOVQ   (DI), X1
	VPSADBW X1, X0, X0
	VPADDQ  X0, X2, X2
	ADDQ    R8, SI
	ADDQ    R9, DI
	DECQ    CX
	JNE     s8
done:
	VPSRLDQ $8, X2, X3
	VPADDQ  X3, X2, X2
	VMOVQ   X2, AX
	MOVQ    AX, ret+48(FP)
	RET

// func satdAsm(src *byte, ss int, pred *byte, ps int, w, h int) int
// Sum over the 4x4 blocks of a w x h block (w 8 or 16, h a multiple of
// 4) of the absolute Hadamard transformed differences, undivided. Each
// pass handles two 4x4 blocks side by side: four rows of eight
// differences get the vertical butterfly across registers, then the
// horizontal one by pairwise adds and subtracts, which pack two
// registers into one so no coefficient is counted twice.
TEXT ·satdAsm(SB), NOSPLIT, $0-56
	MOVQ src+0(FP), SI
	MOVQ ss+8(FP), R8
	MOVQ pred+16(FP), DI
	MOVQ ps+24(FP), R9
	MOVQ w+32(FP), AX
	MOVQ h+40(FP), CX
	VPXOR    X7, X7, X7
	VPCMPEQW X6, X6, X6
	VPSRLW   $15, X6, X6
	SHRQ     $2, CX
rows:
	MOVQ AX, DX
	MOVQ SI, R10
	MOVQ DI, R11
cols:
	VPMOVZXBW (R10), X0
	VPMOVZXBW (R11), X4
	VPSUBW    X4, X0, X0
	VPMOVZXBW (R10)(R8*1), X1
	VPMOVZXBW (R11)(R9*1), X4
	VPSUBW    X4, X1, X1
	LEAQ      (R10)(R8*2), R12
	LEAQ      (R11)(R9*2), R13
	VPMOVZXBW (R12), X2
	VPMOVZXBW (R13), X4
	VPSUBW    X4, X2, X2
	VPMOVZXBW (R12)(R8*1), X3
	VPMOVZXBW (R13)(R9*1), X4
	VPSUBW    X4, X3, X3
	VPADDW    X1, X0, X4
	VPSUBW    X1, X0, X5
	VPADDW    X3, X2, X1
	VPSUBW    X3, X2, X3
	VPADDW    X1, X4, X0
	VPSUBW    X1, X4, X2
	VPADDW    X3, X5, X1
	VPSUBW    X3, X5, X3
	VPHADDW   X1, X0, X4
	VPHSUBW   X1, X0, X5
	VPHADDW   X3, X2, X0
	VPHSUBW   X3, X2, X1
	VPHADDW   X5, X4, X2
	VPHSUBW   X5, X4, X3
	VPHADDW   X1, X0, X4
	VPHSUBW   X1, X0, X5
	VPABSW    X2, X2
	VPABSW    X3, X3
	VPABSW    X4, X4
	VPABSW    X5, X5
	VPADDW    X3, X2, X2
	VPADDW    X5, X4, X4
	VPADDW    X4, X2, X2
	VPMADDWD  X6, X2, X2
	VPADDD    X2, X7, X7
	ADDQ      $8, R10
	ADDQ      $8, R11
	SUBQ      $8, DX
	JNZ       cols
	LEAQ (SI)(R8*4), SI
	LEAQ (DI)(R9*4), DI
	DECQ CX
	JNZ  rows
	VPSHUFD $0x4e, X7, X0
	VPADDD  X0, X7, X7
	VPSHUFD $0xb1, X7, X0
	VPADDD  X0, X7, X7
	VMOVD   X7, AX
	MOVQ    AX, ret+48(FP)
	RET

// The 4x4 zigzag (8.5.6) of a block's quantised levels, as permutes of
// its raster rows 0-7 (lo) and 8-15 (hi): lo' takes lo[0 1 4 . 5 2 3 6]
// with hi[0] third, hi' takes hi[1 4 5 2 . 3 6 7] with lo[7] fourth.
DATA zzP1<>+0(SB)/4, $0
DATA zzP1<>+4(SB)/4, $1
DATA zzP1<>+8(SB)/4, $4
DATA zzP1<>+12(SB)/4, $0
DATA zzP1<>+16(SB)/4, $5
DATA zzP1<>+20(SB)/4, $2
DATA zzP1<>+24(SB)/4, $3
DATA zzP1<>+28(SB)/4, $6
GLOBL zzP1<>(SB), RODATA, $32
DATA zzP3<>+0(SB)/4, $1
DATA zzP3<>+4(SB)/4, $4
DATA zzP3<>+8(SB)/4, $5
DATA zzP3<>+12(SB)/4, $2
DATA zzP3<>+16(SB)/4, $0
DATA zzP3<>+20(SB)/4, $3
DATA zzP3<>+24(SB)/4, $6
DATA zzP3<>+28(SB)/4, $7
GLOBL zzP3<>(SB), RODATA, $32
DATA zzP4<>+0(SB)/4, $7
DATA zzP4<>+4(SB)/4, $7
DATA zzP4<>+8(SB)/4, $7
DATA zzP4<>+12(SB)/4, $7
DATA zzP4<>+16(SB)/4, $7
DATA zzP4<>+20(SB)/4, $7
DATA zzP4<>+24(SB)/4, $7
DATA zzP4<>+28(SB)/4, $7
GLOBL zzP4<>(SB), RODATA, $32

// The forward 4x4 core transform of four blocks side by side, held as
// rows in Y0-Y3 (16 words: block 0's four, block 1's, ...): the
// butterfly down the columns, lane by lane. Leaves the rows in Y0-Y3,
// using Y4-Y7.
#define FDCT4ROWS \
	VPADDW Y3, Y0, Y4 \
	VPADDW Y2, Y1, Y5 \
	VPSUBW Y2, Y1, Y6 \
	VPSUBW Y3, Y0, Y7 \
	VPADDW Y5, Y4, Y0 \
	VPSUBW Y5, Y4, Y2 \
	VPSLLW $1, Y7, Y1 \
	VPADDW Y6, Y1, Y1 \
	VPSLLW $1, Y6, Y3 \
	VPSUBW Y3, Y7, Y3

// Quantises the eight signed dwords of Y_W in place: sign(W) *
// ((|W| * mf + f) >> qbits), with mf at (R9)+off, f in Y15, qbits in X13.
#define QUANT8(off, YW) \
	VPABSD  YW, Y8 \
	VPMULLD off(R9), Y8, Y8 \
	VPADDD  Y15, Y8, Y8 \
	VPSRLD  X13, Y8, Y8 \
	VPSIGND YW, Y8, YW

// Writes the levels of two blocks (raster rows in Y0-Y3, one block per
// 128-bit lane: sel 0x20 the low lanes, 0x31 the high) in zigzag order
// to (DX) and 64(DX).
#define ZIGZAGSTORE(sel, dst) \
	VPERM2I128 $sel, Y1, Y0, Y8 \
	VPERM2I128 $sel, Y3, Y2, Y9 \
	VMOVDQU    zzP1<>(SB), Y10 \
	VPERMD     Y8, Y10, Y10 \
	VPERMD     Y9, Y14, Y11 \
	VPBLENDD   $0x08, Y11, Y10, Y10 \
	VMOVDQU    Y10, dst(DX) \
	VMOVDQU    zzP3<>(SB), Y10 \
	VPERMD     Y9, Y10, Y10 \
	VMOVDQU    zzP4<>(SB), Y11 \
	VPERMD     Y8, Y11, Y11 \
	VPBLENDD   $0x10, Y11, Y10, Y10 \
	VMOVDQU    Y10, (dst+32)(DX)

// func fdctQuantLumaAsm(src *byte, ss int, pred *byte, mfT *int32, qbits, f int, out *int32) uint64
// Transforms and quantises the 16 4x4 blocks of a macroblock's luma
// residual (src, stride ss, less pred, stride 16): the levels in zigzag
// order per raster block at out, 16 dwords each. mfT holds, per
// coefficient column j of 4, the multipliers of rows 0-3 twice (8
// dwords). Returns the raster blocks with a level (16 bits) and the
// number of levels (bits 16 up).
//
// A band of four rows is four blocks in 16 words: the butterfly down the
// columns, a transpose of each block (word unpacks, then dword and qword
// ones that also put the blocks back in order), the butterfly again,
// then per column of the result (= row of the transposed block) the
// quantiser on dwords, and a dword transpose back to raster rows for the
// zigzag.
TEXT ·fdctQuantLumaAsm(SB), NOSPLIT, $0-64
	MOVQ src+0(FP), SI
	MOVQ ss+8(FP), R8
	MOVQ pred+16(FP), DI
	MOVQ mfT+24(FP), R9
	MOVQ qbits+32(FP), AX
	MOVQ AX, X13
	MOVQ f+40(FP), AX
	MOVQ AX, X15
	VPBROADCASTD X15, Y15
	MOVQ out+48(FP), DX
	VPXOR Y14, Y14, Y14
	XORQ  R10, R10 // blocks with levels
	XORQ  R11, R11 // zero levels
	XORQ  R12, R12 // block index
	MOVQ  $4, CX
band:
	LEAQ      (SI)(R8*2), R13
	VPMOVZXBW (SI), Y0
	VPMOVZXBW (DI), Y4
	VPSUBW    Y4, Y0, Y0
	VPMOVZXBW (SI)(R8*1), Y1
	VPMOVZXBW 16(DI), Y4
	VPSUBW    Y4, Y1, Y1
	VPMOVZXBW (R13), Y2
	VPMOVZXBW 32(DI), Y4
	VPSUBW    Y4, Y2, Y2
	VPMOVZXBW (R13)(R8*1), Y3
	VPMOVZXBW 48(DI), Y4
	VPSUBW    Y4, Y3, Y3
	FDCT4ROWS
	// transpose each block: rows 0-3 (Y0-Y3) to columns 0-3 (Y4-Y7)
	VPUNPCKLWD  Y1, Y0, Y4
	VPUNPCKHWD  Y1, Y0, Y5
	VPUNPCKLWD  Y3, Y2, Y6
	VPUNPCKHWD  Y3, Y2, Y7
	VPUNPCKLDQ  Y6, Y4, Y0
	VPUNPCKHDQ  Y6, Y4, Y1
	VPUNPCKLDQ  Y7, Y5, Y2
	VPUNPCKHDQ  Y7, Y5, Y3
	VPUNPCKLQDQ Y2, Y0, Y4
	VPUNPCKHQDQ Y2, Y0, Y5
	VPUNPCKLQDQ Y3, Y1, Y6
	VPUNPCKHQDQ Y3, Y1, Y7
	VMOVDQA Y4, Y0
	VMOVDQA Y5, Y1
	VMOVDQA Y6, Y2
	VMOVDQA Y7, Y3
	FDCT4ROWS
	// Y0-Y3: column j of each block (its row j transposed), four blocks
	VMOVDQA Y0, Y4
	VMOVDQA Y1, Y5
	VMOVDQA Y2, Y6
	VMOVDQA Y3, Y7
	// blocks 0 and 1: the low lanes
	VPMOVSXWD X4, Y0
	VPMOVSXWD X5, Y1
	VPMOVSXWD X6, Y2
	VPMOVSXWD X7, Y3
	QUANT8(0, Y0)
	QUANT8(32, Y1)
	QUANT8(64, Y2)
	QUANT8(96, Y3)
	// back to raster rows: row m = [c0[m] c1[m] c2[m] c3[m]]
	VPUNPCKLDQ  Y1, Y0, Y8
	VPUNPCKHDQ  Y1, Y0, Y9
	VPUNPCKLDQ  Y3, Y2, Y10
	VPUNPCKHDQ  Y3, Y2, Y11
	VPUNPCKLQDQ Y10, Y8, Y0
	VPUNPCKHQDQ Y10, Y8, Y1
	VPUNPCKLQDQ Y11, Y9, Y2
	VPUNPCKHQDQ Y11, Y9, Y3
	VPOR      Y1, Y0, Y8
	VPOR      Y3, Y2, Y9
	VPOR      Y9, Y8, Y8
	VPCMPEQD  Y14, Y8, Y8
	VMOVMSKPS Y8, AX
	MOVQ      AX, BX
	ANDQ      $15, BX
	CMPQ      BX, $15
	JEQ       lo0zero
	BTSQ      R12, R10
lo0zero:
	SHRQ      $4, AX
	CMPQ      AX, $15
	JEQ       lo1zero
	LEAQ      1(R12), BX
	BTSQ      BX, R10
lo1zero:
	VPCMPEQD  Y14, Y0, Y8
	VMOVMSKPS Y8, AX
	POPCNTQ   AX, AX
	ADDQ      AX, R11
	VPCMPEQD  Y14, Y1, Y8
	VMOVMSKPS Y8, AX
	POPCNTQ   AX, AX
	ADDQ      AX, R11
	VPCMPEQD  Y14, Y2, Y8
	VMOVMSKPS Y8, AX
	POPCNTQ   AX, AX
	ADDQ      AX, R11
	VPCMPEQD  Y14, Y3, Y8
	VMOVMSKPS Y8, AX
	POPCNTQ   AX, AX
	ADDQ      AX, R11
	ZIGZAGSTORE(0x20, 0)
	ZIGZAGSTORE(0x31, 64)
	// blocks 2 and 3: the high lanes
	VEXTRACTI128 $1, Y4, X0
	VEXTRACTI128 $1, Y5, X1
	VEXTRACTI128 $1, Y6, X2
	VEXTRACTI128 $1, Y7, X3
	VPMOVSXWD X0, Y0
	VPMOVSXWD X1, Y1
	VPMOVSXWD X2, Y2
	VPMOVSXWD X3, Y3
	QUANT8(0, Y0)
	QUANT8(32, Y1)
	QUANT8(64, Y2)
	QUANT8(96, Y3)
	VPUNPCKLDQ  Y1, Y0, Y8
	VPUNPCKHDQ  Y1, Y0, Y9
	VPUNPCKLDQ  Y3, Y2, Y10
	VPUNPCKHDQ  Y3, Y2, Y11
	VPUNPCKLQDQ Y10, Y8, Y0
	VPUNPCKHQDQ Y10, Y8, Y1
	VPUNPCKLQDQ Y11, Y9, Y2
	VPUNPCKHQDQ Y11, Y9, Y3
	VPOR      Y1, Y0, Y8
	VPOR      Y3, Y2, Y9
	VPOR      Y9, Y8, Y8
	VPCMPEQD  Y14, Y8, Y8
	VMOVMSKPS Y8, AX
	MOVQ      AX, BX
	ANDQ      $15, BX
	CMPQ      BX, $15
	JEQ       hi0zero
	LEAQ      2(R12), BX
	BTSQ      BX, R10
hi0zero:
	SHRQ      $4, AX
	CMPQ      AX, $15
	JEQ       hi1zero
	LEAQ      3(R12), BX
	BTSQ      BX, R10
hi1zero:
	VPCMPEQD  Y14, Y0, Y8
	VMOVMSKPS Y8, AX
	POPCNTQ   AX, AX
	ADDQ      AX, R11
	VPCMPEQD  Y14, Y1, Y8
	VMOVMSKPS Y8, AX
	POPCNTQ   AX, AX
	ADDQ      AX, R11
	VPCMPEQD  Y14, Y2, Y8
	VMOVMSKPS Y8, AX
	POPCNTQ   AX, AX
	ADDQ      AX, R11
	VPCMPEQD  Y14, Y3, Y8
	VMOVMSKPS Y8, AX
	POPCNTQ   AX, AX
	ADDQ      AX, R11
	ZIGZAGSTORE(0x20, 128)
	ZIGZAGSTORE(0x31, 192)
	ADDQ $256, DX
	ADDQ $4, R12
	LEAQ (SI)(R8*4), SI
	ADDQ $64, DI
	DECQ CX
	JNZ  band
	MOVQ $256, AX
	SUBQ R11, AX
	SHLQ $16, AX
	ORQ  R10, AX
	MOVQ AX, ret+56(FP)
	VZEROUPPER
	RET

// Finds which of the two blocks in the raster rows Y0-Y3 (one block per
// 128-bit lane) have a level, setting bits R12+b0 and R12+b1 of R10, and
// adds the zero levels to R11. Uses Y8, AX, BX.
#define NONZERO2(b0, b1, skip0, skip1) \
	VPOR      Y1, Y0, Y8 \
	VPOR      Y3, Y2, Y9 \
	VPOR      Y9, Y8, Y8 \
	VPCMPEQD  Y14, Y8, Y8 \
	VMOVMSKPS Y8, AX \
	MOVQ      AX, BX \
	ANDQ      $15, BX \
	CMPQ      BX, $15 \
	JEQ       skip0 \
	LEAQ      b0(R12), BX \
	BTSQ      BX, R10 \
skip0: \
	SHRQ      $4, AX \
	CMPQ      AX, $15 \
	JEQ       skip1 \
	LEAQ      b1(R12), BX \
	BTSQ      BX, R10 \
skip1: \
	VPCMPEQD  Y14, Y0, Y8 \
	VMOVMSKPS Y8, AX \
	POPCNTQ   AX, AX \
	ADDQ      AX, R11 \
	VPCMPEQD  Y14, Y1, Y8 \
	VMOVMSKPS Y8, AX \
	POPCNTQ   AX, AX \
	ADDQ      AX, R11 \
	VPCMPEQD  Y14, Y2, Y8 \
	VMOVMSKPS Y8, AX \
	POPCNTQ   AX, AX \
	ADDQ      AX, R11 \
	VPCMPEQD  Y14, Y3, Y8 \
	VMOVMSKPS Y8, AX \
	POPCNTQ   AX, AX \
	ADDQ      AX, R11

// Turns the columns of four blocks in Y0-Y3 (dwords) back into raster
// rows, in Y0-Y3, using Y8-Y11.
#define RASTERROWS \
	VPUNPCKLDQ  Y1, Y0, Y8 \
	VPUNPCKHDQ  Y1, Y0, Y9 \
	VPUNPCKLDQ  Y3, Y2, Y10 \
	VPUNPCKHDQ  Y3, Y2, Y11 \
	VPUNPCKLQDQ Y10, Y8, Y0 \
	VPUNPCKHQDQ Y10, Y8, Y1 \
	VPUNPCKLQDQ Y11, Y9, Y2 \
	VPUNPCKHQDQ Y11, Y9, Y3

// func fdctQuantChromaAsm(src *byte, ss int, pred *byte, mfT *int32, qbits, f int, out *int32, dc *int32) uint64
// fdctQuantLumaAsm for one chroma component's 8x8: its four blocks'
// levels in zigzag order at out (16 dwords each, the first a zero), the
// transformed DC coefficients, unquantised, at dc (dwords 0, 4, 8 and
// 12 of 16). Returns the blocks with an AC level (4 bits) and the number
// of AC levels (bits 16 up). The two bands of rows are the two 128-bit
// lanes of a band of four blocks.
TEXT ·fdctQuantChromaAsm(SB), NOSPLIT, $0-72
	MOVQ src+0(FP), SI
	MOVQ ss+8(FP), R8
	MOVQ pred+16(FP), DI
	MOVQ mfT+24(FP), R9
	MOVQ qbits+32(FP), AX
	MOVQ AX, X13
	MOVQ f+40(FP), AX
	MOVQ AX, X15
	VPBROADCASTD X15, Y15
	MOVQ out+48(FP), DX
	MOVQ dc+56(FP), R13
	VPXOR Y14, Y14, Y14
	XORQ  R10, R10
	XORQ  R11, R11
	XORQ  R12, R12
	LEAQ (SI)(R8*4), R14 // rows 4-7
	LEAQ (SI)(R8*2), R15 // rows 2-3
	VPMOVZXBW   (SI), X0
	VPMOVZXBW   (R14), X8
	VINSERTI128 $1, X8, Y0, Y0
	VPMOVZXBW   (DI), X4
	VPMOVZXBW   32(DI), X8
	VINSERTI128 $1, X8, Y4, Y4
	VPSUBW      Y4, Y0, Y0
	VPMOVZXBW   (SI)(R8*1), X1
	VPMOVZXBW   (R14)(R8*1), X8
	VINSERTI128 $1, X8, Y1, Y1
	VPMOVZXBW   8(DI), X4
	VPMOVZXBW   40(DI), X8
	VINSERTI128 $1, X8, Y4, Y4
	VPSUBW      Y4, Y1, Y1
	LEAQ        (R14)(R8*2), R14 // rows 6-7
	VPMOVZXBW   (R15), X2
	VPMOVZXBW   (R14), X8
	VINSERTI128 $1, X8, Y2, Y2
	VPMOVZXBW   16(DI), X4
	VPMOVZXBW   48(DI), X8
	VINSERTI128 $1, X8, Y4, Y4
	VPSUBW      Y4, Y2, Y2
	VPMOVZXBW   (R15)(R8*1), X3
	VPMOVZXBW   (R14)(R8*1), X8
	VINSERTI128 $1, X8, Y3, Y3
	VPMOVZXBW   24(DI), X4
	VPMOVZXBW   56(DI), X8
	VINSERTI128 $1, X8, Y4, Y4
	VPSUBW      Y4, Y3, Y3
	FDCT4ROWS
	VPUNPCKLWD  Y1, Y0, Y4
	VPUNPCKHWD  Y1, Y0, Y5
	VPUNPCKLWD  Y3, Y2, Y6
	VPUNPCKHWD  Y3, Y2, Y7
	VPUNPCKLDQ  Y6, Y4, Y0
	VPUNPCKHDQ  Y6, Y4, Y1
	VPUNPCKLDQ  Y7, Y5, Y2
	VPUNPCKHDQ  Y7, Y5, Y3
	VPUNPCKLQDQ Y2, Y0, Y4
	VPUNPCKHQDQ Y2, Y0, Y5
	VPUNPCKLQDQ Y3, Y1, Y6
	VPUNPCKHQDQ Y3, Y1, Y7
	VMOVDQA Y4, Y0
	VMOVDQA Y5, Y1
	VMOVDQA Y6, Y2
	VMOVDQA Y7, Y3
	FDCT4ROWS
	VMOVDQA Y0, Y4
	VMOVDQA Y1, Y5
	VMOVDQA Y2, Y6
	VMOVDQA Y3, Y7
	// blocks 0 and 1
	VPMOVSXWD X4, Y0
	VPMOVSXWD X5, Y1
	VPMOVSXWD X6, Y2
	VPMOVSXWD X7, Y3
	VMOVDQU   Y0, (R13)
	QUANT8(0, Y0)
	QUANT8(32, Y1)
	QUANT8(64, Y2)
	QUANT8(96, Y3)
	RASTERROWS
	VPBLENDD $0x11, Y14, Y0, Y0 // the DC is coded apart
	NONZERO2(0, 1, lo0zero, lo1zero)
	ZIGZAGSTORE(0x20, 0)
	ZIGZAGSTORE(0x31, 64)
	// blocks 2 and 3
	VEXTRACTI128 $1, Y4, X0
	VEXTRACTI128 $1, Y5, X1
	VEXTRACTI128 $1, Y6, X2
	VEXTRACTI128 $1, Y7, X3
	VPMOVSXWD X0, Y0
	VPMOVSXWD X1, Y1
	VPMOVSXWD X2, Y2
	VPMOVSXWD X3, Y3
	VMOVDQU   Y0, 32(R13)
	QUANT8(0, Y0)
	QUANT8(32, Y1)
	QUANT8(64, Y2)
	QUANT8(96, Y3)
	RASTERROWS
	VPBLENDD $0x11, Y14, Y0, Y0
	NONZERO2(2, 3, hi0zero, hi1zero)
	ZIGZAGSTORE(0x20, 128)
	ZIGZAGSTORE(0x31, 192)
	MOVQ $64, AX
	SUBQ R11, AX
	SHLQ $16, AX
	ORQ  R10, AX
	MOVQ AX, ret+64(FP)
	VZEROUPPER
	RET

// func satdAvgAsm(src *byte, ss int, a *byte, as int, b *byte, bs int, w, h int) int
// satdAsm against the average of two blocks a and b (rounded up: a
// quarter-sample prediction from the half-sample planes), without
// forming it.
TEXT ·satdAvgAsm(SB), NOSPLIT, $0-72
	MOVQ src+0(FP), SI
	MOVQ ss+8(FP), R8
	MOVQ a+16(FP), DI
	MOVQ as+24(FP), R9
	MOVQ b+32(FP), R14
	MOVQ bs+40(FP), R15
	MOVQ w+48(FP), AX
	MOVQ h+56(FP), CX
	VPXOR    X7, X7, X7
	VPCMPEQW X6, X6, X6
	VPSRLW   $15, X6, X6
	SHRQ     $2, CX
avrows:
	MOVQ AX, DX
	MOVQ SI, R10
	MOVQ DI, R11
	MOVQ R14, BX
avcols:
	VPMOVZXBW (R10), X0
	VMOVQ     (R11), X4
	VMOVQ     (BX), X5
	VPAVGB    X5, X4, X4
	VPMOVZXBW X4, X4
	VPSUBW    X4, X0, X0
	VPMOVZXBW (R10)(R8*1), X1
	VMOVQ     (R11)(R9*1), X4
	VMOVQ     (BX)(R15*1), X5
	VPAVGB    X5, X4, X4
	VPMOVZXBW X4, X4
	VPSUBW    X4, X1, X1
	LEAQ      (R10)(R8*2), R12
	LEAQ      (R11)(R9*2), R13
	VPMOVZXBW (R12), X2
	VMOVQ     (R13), X4
	VMOVQ     (BX)(R15*2), X5
	VPAVGB    X5, X4, X4
	VPMOVZXBW X4, X4
	VPSUBW    X4, X2, X2
	VPMOVZXBW (R12)(R8*1), X3
	VMOVQ     (R13)(R9*1), X4
	LEAQ      (BX)(R15*2), R12
	VMOVQ     (R12)(R15*1), X5
	VPAVGB    X5, X4, X4
	VPMOVZXBW X4, X4
	VPSUBW    X4, X3, X3
	VPADDW    X1, X0, X4
	VPSUBW    X1, X0, X5
	VPADDW    X3, X2, X1
	VPSUBW    X3, X2, X3
	VPADDW    X1, X4, X0
	VPSUBW    X1, X4, X2
	VPADDW    X3, X5, X1
	VPSUBW    X3, X5, X3
	VPHADDW   X1, X0, X4
	VPHSUBW   X1, X0, X5
	VPHADDW   X3, X2, X0
	VPHSUBW   X3, X2, X1
	VPHADDW   X5, X4, X2
	VPHSUBW   X5, X4, X3
	VPHADDW   X1, X0, X4
	VPHSUBW   X1, X0, X5
	VPABSW    X2, X2
	VPABSW    X3, X3
	VPABSW    X4, X4
	VPABSW    X5, X5
	VPADDW    X3, X2, X2
	VPADDW    X5, X4, X4
	VPADDW    X4, X2, X2
	VPMADDWD  X6, X2, X2
	VPADDD    X2, X7, X7
	ADDQ      $8, R10
	ADDQ      $8, R11
	ADDQ      $8, BX
	SUBQ      $8, DX
	JNZ       avcols
	LEAQ (SI)(R8*4), SI
	LEAQ (DI)(R9*4), DI
	LEAQ (R14)(R15*4), R14
	DECQ CX
	JNZ  avrows
	VPSHUFD $0x4e, X7, X0
	VPADDD  X0, X7, X7
	VPSHUFD $0xb1, X7, X0
	VPADDD  X0, X7, X7
	VMOVD   X7, AX
	MOVQ    AX, ret+64(FP)
	RET
