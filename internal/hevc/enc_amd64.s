//go:build amd64 && !purego

#include "textflag.h"

// func sadAVX2(a *uint16, as int, b *uint16, bs int, w, h int) int
// Sum of absolute differences of two w x h blocks of 16-bit samples (w a
// multiple of 8); the strides are in samples. Differences are at most 16
// bits; VPMADDWD by ones widens them to dwords as it sums pairs.
TEXT ·sadAVX2(SB), NOSPLIT, $0-56
	MOVQ a+0(FP), SI
	MOVQ as+8(FP), R8
	MOVQ b+16(FP), DI
	MOVQ bs+24(FP), R9
	MOVQ w+32(FP), CX
	MOVQ h+40(FP), DX
	SHLQ $1, R8
	SHLQ $1, R9
	VPXOR Y0, Y0, Y0
	VPXOR X4, X4, X4
	VPCMPEQW Y3, Y3, Y3
	VPSRLW $15, Y3, Y3
row:
	MOVQ CX, AX
	MOVQ SI, R10
	MOVQ DI, R11
col16:
	CMPQ AX, $16
	JL col8
	VMOVDQU (R10), Y1
	VMOVDQU (R11), Y2
	VPSUBW Y2, Y1, Y1
	VPABSW Y1, Y1
	VPMADDWD Y3, Y1, Y1
	VPADDD Y1, Y0, Y0
	ADDQ $32, R10
	ADDQ $32, R11
	SUBQ $16, AX
	JMP col16
col8:
	CMPQ AX, $8
	JL rowdone
	VMOVDQU (R10), X1
	VMOVDQU (R11), X2
	VPSUBW X2, X1, X1
	VPABSW X1, X1
	VPMADDWD X3, X1, X1
	VPADDD X1, X4, X4
rowdone:
	ADDQ R8, SI
	ADDQ R9, DI
	DECQ DX
	JNZ row
	VEXTRACTI128 $1, Y0, X1
	VPADDD X1, X0, X0
	VPADDD X4, X0, X0
	VPHADDD X0, X0, X0
	VPHADDD X0, X0, X0
	VMOVD X0, AX
	MOVQ AX, ret+48(FP)
	VZEROUPPER
	RET

// Butterfly of the dword lanes of Y_R paired by the shuffle: the sum in
// the lower lane of each pair, the difference (lower minus upper) in the
// upper, blend mask M selecting the upper lanes. Uses Y8, Y9.
#define HPAIR1(R) \
	VPSHUFD $0xB1, R, Y8 \
	VPADDD Y8, R, Y9 \
	VPSUBD R, Y8, Y8 \
	VPBLENDD $0xAA, Y8, Y9, R
#define HPAIR2(R) \
	VPSHUFD $0x4E, R, Y8 \
	VPADDD Y8, R, Y9 \
	VPSUBD R, Y8, Y8 \
	VPBLENDD $0xCC, Y8, Y9, R
#define HPAIR4(R) \
	VPERM2I128 $0x01, R, R, Y8 \
	VPADDD Y8, R, Y9 \
	VPSUBD R, Y8, Y8 \
	VPBLENDD $0xF0, Y8, Y9, R

// Butterfly of two rows: A = A + B, B = A - B. Uses Y8.
#define VPAIR(A, B) \
	VPSUBD B, A, Y8 \
	VPADDD B, A, A \
	VMOVDQA Y8, B

// func satd8AVX2(a *uint16, as int, b *uint16, bs int) int
// Sum of the absolute Hadamard transformed differences of two 8x8 blocks
// of 16-bit samples, undivided, in dwords: the rows' butterflies across
// the registers, then each row's within it.
TEXT ·satd8AVX2(SB), NOSPLIT, $0-40
	MOVQ a+0(FP), SI
	MOVQ as+8(FP), R8
	MOVQ b+16(FP), DI
	MOVQ bs+24(FP), R9
	SHLQ $1, R8
	SHLQ $1, R9
#define LOADROW(R) \
	VPMOVZXWD (SI), R \
	VPMOVZXWD (DI), Y8 \
	VPSUBD Y8, R, R \
	ADDQ R8, SI \
	ADDQ R9, DI
	LOADROW(Y0)
	LOADROW(Y1)
	LOADROW(Y2)
	LOADROW(Y3)
	LOADROW(Y4)
	LOADROW(Y5)
	LOADROW(Y6)
	LOADROW(Y7)
	VPAIR(Y0, Y1)
	VPAIR(Y2, Y3)
	VPAIR(Y4, Y5)
	VPAIR(Y6, Y7)
	VPAIR(Y0, Y2)
	VPAIR(Y1, Y3)
	VPAIR(Y4, Y6)
	VPAIR(Y5, Y7)
	VPAIR(Y0, Y4)
	VPAIR(Y1, Y5)
	VPAIR(Y2, Y6)
	VPAIR(Y3, Y7)
	HPAIR1(Y0)
	HPAIR1(Y1)
	HPAIR1(Y2)
	HPAIR1(Y3)
	HPAIR1(Y4)
	HPAIR1(Y5)
	HPAIR1(Y6)
	HPAIR1(Y7)
	HPAIR2(Y0)
	HPAIR2(Y1)
	HPAIR2(Y2)
	HPAIR2(Y3)
	HPAIR2(Y4)
	HPAIR2(Y5)
	HPAIR2(Y6)
	HPAIR2(Y7)
	HPAIR4(Y0)
	HPAIR4(Y1)
	HPAIR4(Y2)
	HPAIR4(Y3)
	HPAIR4(Y4)
	HPAIR4(Y5)
	HPAIR4(Y6)
	HPAIR4(Y7)
	VPABSD Y0, Y0
	VPABSD Y1, Y1
	VPABSD Y2, Y2
	VPABSD Y3, Y3
	VPABSD Y4, Y4
	VPABSD Y5, Y5
	VPABSD Y6, Y6
	VPABSD Y7, Y7
	VPADDD Y1, Y0, Y0
	VPADDD Y3, Y2, Y2
	VPADDD Y5, Y4, Y4
	VPADDD Y7, Y6, Y6
	VPADDD Y2, Y0, Y0
	VPADDD Y6, Y4, Y4
	VPADDD Y4, Y0, Y0
	VEXTRACTI128 $1, Y0, X1
	VPADDD X1, X0, X0
	VPHADDD X0, X0, X0
	VPHADDD X0, X0, X0
	VMOVD X0, AX
	MOVQ AX, ret+32(FP)
	VZEROUPPER
	RET

// Word butterfly of two registers: A = A + B, B = A - B. Uses T.
#define WPAIR(A, B, T) \
	VPSUBW B, A, T \
	VPADDW B, A, A \
	VMOVDQA T, B

// func satd16x8AVX2(a *uint16, as int, b *uint16, bs int) uint64
// The absolute Hadamard transformed differences of the two 8x8 blocks of
// a 16x8 region of samples of at most 10 bits, in 16-bit lanes (one
// block per 128-bit lane): the column transform across the row
// registers, a transpose, two stages of the row transform, and the last
// stage folded with the absolute values, |a+b| + |a-b| = 2 max(|a|, |b|),
// so no lane overflows. Returns half of each block's sum: the left
// block's in the low 32 bits, the right's in the high.
TEXT ·satd16x8AVX2(SB), NOSPLIT, $0-40
	MOVQ a+0(FP), SI
	MOVQ as+8(FP), R8
	MOVQ b+16(FP), DI
	MOVQ bs+24(FP), R9
	SHLQ $1, R8
	SHLQ $1, R9
#define WROW(R) \
	VMOVDQU (SI), R \
	VPSUBW (DI), R, R \
	ADDQ R8, SI \
	ADDQ R9, DI
	WROW(Y0)
	WROW(Y1)
	WROW(Y2)
	WROW(Y3)
	WROW(Y4)
	WROW(Y5)
	WROW(Y6)
	WROW(Y7)
	// Columns: three stages across the rows.
	WPAIR(Y0, Y1, Y8)
	WPAIR(Y2, Y3, Y8)
	WPAIR(Y4, Y5, Y8)
	WPAIR(Y6, Y7, Y8)
	WPAIR(Y0, Y2, Y8)
	WPAIR(Y1, Y3, Y8)
	WPAIR(Y4, Y6, Y8)
	WPAIR(Y5, Y7, Y8)
	WPAIR(Y0, Y4, Y8)
	WPAIR(Y1, Y5, Y8)
	WPAIR(Y2, Y6, Y8)
	WPAIR(Y3, Y7, Y8)
	// Transpose each lane's 8x8: Y8..Y15 become its columns 0..7.
	VPUNPCKLWD Y1, Y0, Y8
	VPUNPCKHWD Y1, Y0, Y9
	VPUNPCKLWD Y3, Y2, Y10
	VPUNPCKHWD Y3, Y2, Y11
	VPUNPCKLWD Y5, Y4, Y12
	VPUNPCKHWD Y5, Y4, Y13
	VPUNPCKLWD Y7, Y6, Y14
	VPUNPCKHWD Y7, Y6, Y15
	VPUNPCKLDQ Y10, Y8, Y0
	VPUNPCKHDQ Y10, Y8, Y1
	VPUNPCKLDQ Y11, Y9, Y2
	VPUNPCKHDQ Y11, Y9, Y3
	VPUNPCKLDQ Y14, Y12, Y4
	VPUNPCKHDQ Y14, Y12, Y5
	VPUNPCKLDQ Y15, Y13, Y6
	VPUNPCKHDQ Y15, Y13, Y7
	VPUNPCKLQDQ Y4, Y0, Y8
	VPUNPCKHQDQ Y4, Y0, Y9
	VPUNPCKLQDQ Y5, Y1, Y10
	VPUNPCKHQDQ Y5, Y1, Y11
	VPUNPCKLQDQ Y6, Y2, Y12
	VPUNPCKHQDQ Y6, Y2, Y13
	VPUNPCKLQDQ Y7, Y3, Y14
	VPUNPCKHQDQ Y7, Y3, Y15
	// Rows: two stages across the columns.
	WPAIR(Y8, Y9, Y0)
	WPAIR(Y10, Y11, Y0)
	WPAIR(Y12, Y13, Y0)
	WPAIR(Y14, Y15, Y0)
	WPAIR(Y8, Y10, Y0)
	WPAIR(Y9, Y11, Y0)
	WPAIR(Y12, Y14, Y0)
	WPAIR(Y13, Y15, Y0)
	// The last stage with the absolute values.
	VPABSW Y8, Y8
	VPABSW Y9, Y9
	VPABSW Y10, Y10
	VPABSW Y11, Y11
	VPABSW Y12, Y12
	VPABSW Y13, Y13
	VPABSW Y14, Y14
	VPABSW Y15, Y15
	VPMAXSW Y12, Y8, Y8
	VPMAXSW Y13, Y9, Y9
	VPMAXSW Y14, Y10, Y10
	VPMAXSW Y15, Y11, Y11
	// Sum per lane, in dwords.
	VPCMPEQW Y0, Y0, Y0
	VPSRLW $15, Y0, Y0
	VPMADDWD Y0, Y8, Y8
	VPMADDWD Y0, Y9, Y9
	VPMADDWD Y0, Y10, Y10
	VPMADDWD Y0, Y11, Y11
	VPADDD Y9, Y8, Y8
	VPADDD Y11, Y10, Y10
	VPADDD Y10, Y8, Y8
	VPHADDD Y8, Y8, Y8
	VPHADDD Y8, Y8, Y8
	VMOVD X8, AX
	VEXTRACTI128 $1, Y8, X8
	VMOVD X8, BX
	SHLQ $32, BX
	ORQ BX, AX
	MOVQ AX, ret+32(FP)
	VZEROUPPER
	RET

// func fdctColsAVX2(dst unsafe.Pointer, src *int16, n int, pairs *int32, rnd, shift, wide int, scratch *int16)
// The transform of the columns of the n x n matrix src (n 16 or 32):
// dst[k][c] = (sum over j of T[k][j] src[j][c] + rnd) >> shift, as words
// (saturated) or, wide, dwords. pairs holds T[k][2p] and T[k][2p+1] as
// the words of dword k*n/2+p. Sixteen columns at a time: their rows
// interleaved in pairs into scratch (n words a pair), then each output
// row the sum of VPMADDWDs of the pairs with the broadcast coefficients.
TEXT ·fdctColsAVX2(SB), NOSPLIT, $0-64
	MOVQ dst+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ n+16(FP), R9
	MOVQ pairs+24(FP), R8
	MOVQ rnd+32(FP), AX
	MOVQ shift+40(FP), BX
	MOVQ wide+48(FP), R10
	MOVQ scratch+56(FP), R11
	VMOVD AX, X13
	VPBROADCASTD X13, Y13
	VMOVQ BX, X14
	XORQ R12, R12            // c0
group:
	// Interleave the rows of columns c0..c0+15 in pairs.
	MOVQ R9, DX
	SHLQ $1, DX              // row bytes
	LEAQ (SI)(R12*2), AX     // row 0 at c0
	MOVQ R11, BX
	MOVQ R9, CX
	SHRQ $1, CX              // pairs
ileave:
	VMOVDQU (AX), Y5
	VPUNPCKLWD (AX)(DX*1), Y5, Y6
	VPUNPCKHWD (AX)(DX*1), Y5, Y7
	VMOVDQU Y6, (BX)
	VMOVDQU Y7, 32(BX)
	LEAQ (AX)(DX*2), AX
	ADDQ $64, BX
	DECQ CX
	JNZ ileave
	// Each output row.
	XORQ R13, R13            // k
	MOVQ R8, AX              // the pairs of row k
krow:
	VPXOR Y0, Y0, Y0
	VPXOR Y1, Y1, Y1
	MOVQ R11, BX
	MOVQ R9, CX
	SHRQ $1, CX
pairloop:
	VPBROADCASTD (AX), Y2
	VPMADDWD (BX), Y2, Y3
	VPADDD Y3, Y0, Y0
	VPMADDWD 32(BX), Y2, Y4
	VPADDD Y4, Y1, Y1
	ADDQ $4, AX
	ADDQ $64, BX
	DECQ CX
	JNZ pairloop
	VPADDD Y13, Y0, Y0
	VPADDD Y13, Y1, Y1
	VPSRAD X14, Y0, Y0
	VPSRAD X14, Y1, Y1
	// dst row k, column c0.
	MOVQ R13, BX
	IMULQ R9, BX
	ADDQ R12, BX
	TESTQ R10, R10
	JNZ wide
	VPACKSSDW Y1, Y0, Y0     // per lane: columns 0-3 then 4-7 (8-11, 12-15)
	VMOVDQU Y0, (DI)(BX*2)
	JMP next
wide:
	VPERM2I128 $0x20, Y1, Y0, Y2  // columns 0-7
	VPERM2I128 $0x31, Y1, Y0, Y3  // columns 8-15
	VMOVDQU Y2, (DI)(BX*4)
	VMOVDQU Y3, 32(DI)(BX*4)
next:
	INCQ R13
	CMPQ R13, R9
	JL krow
	ADDQ $16, R12
	CMPQ R12, R9
	JL group
	VZEROUPPER
	RET

// func quantAVX2(c *int32, n int, scale, add, qbits int) int
// Quantises the n (a multiple of 8) coefficients at c in place:
// sign(c) * min((|c| * scale + add) >> qbits, 32767), returning how many
// are not zero. The products stay below 2^31 for the transforms'
// coefficients (at most 16 bits and a little) and the scales (at most
// 26214).
TEXT ·quantAVX2(SB), NOSPLIT, $0-48
	MOVQ c+0(FP), DI
	MOVQ n+8(FP), CX
	MOVQ scale+16(FP), AX
	MOVQ add+24(FP), BX
	MOVQ qbits+32(FP), DX
	VMOVD AX, X10
	VPBROADCASTD X10, Y10
	VMOVD BX, X11
	VPBROADCASTD X11, Y11
	VMOVQ DX, X12
	MOVL $32767, AX
	VMOVD AX, X13
	VPBROADCASTD X13, Y13
	VPXOR Y14, Y14, Y14
	XORQ R8, R8               // the zero levels
	MOVQ CX, R9
qloop:
	VMOVDQU (DI), Y0
	VPABSD Y0, Y1
	VPMULLD Y10, Y1, Y1
	VPADDD Y11, Y1, Y1
	VPSRLD X12, Y1, Y1
	VPMINSD Y13, Y1, Y1
	VPSIGND Y0, Y1, Y1
	VMOVDQU Y1, (DI)
	VPCMPEQD Y14, Y1, Y2
	VMOVMSKPS Y2, AX
	POPCNTL AX, AX
	ADDQ AX, R8
	ADDQ $32, DI
	SUBQ $8, CX
	JNZ qloop
	SUBQ R8, R9
	MOVQ R9, ret+40(FP)
	VZEROUPPER
	RET

// func fdct8ColsAVX2(dst unsafe.Pointer, src *int16, pairs *int32, rnd, shift, wide int)
// fdctColsAVX2 for an 8x8 matrix, in 128-bit registers: the four row
// pairs interleaved in X4-X11 (low and high halves of the columns).
TEXT ·fdct8ColsAVX2(SB), NOSPLIT, $0-48
	MOVQ dst+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ pairs+16(FP), R8
	MOVQ rnd+24(FP), AX
	MOVQ shift+32(FP), BX
	MOVQ wide+40(FP), R10
	VMOVD AX, X13
	VPBROADCASTD X13, X13
	VMOVQ BX, X14
	VMOVDQU 0(SI), X0
	VPUNPCKLWD 16(SI), X0, X4
	VPUNPCKHWD 16(SI), X0, X5
	VMOVDQU 32(SI), X0
	VPUNPCKLWD 48(SI), X0, X6
	VPUNPCKHWD 48(SI), X0, X7
	VMOVDQU 64(SI), X0
	VPUNPCKLWD 80(SI), X0, X8
	VPUNPCKHWD 80(SI), X0, X9
	VMOVDQU 96(SI), X0
	VPUNPCKLWD 112(SI), X0, X10
	VPUNPCKHWD 112(SI), X0, X11
	MOVQ $8, CX
k8:
	VPBROADCASTD 0(R8), X2
	VPMADDWD X4, X2, X0
	VPMADDWD X5, X2, X1
	VPBROADCASTD 4(R8), X2
	VPMADDWD X6, X2, X3
	VPADDD X3, X0, X0
	VPMADDWD X7, X2, X3
	VPADDD X3, X1, X1
	VPBROADCASTD 8(R8), X2
	VPMADDWD X8, X2, X3
	VPADDD X3, X0, X0
	VPMADDWD X9, X2, X3
	VPADDD X3, X1, X1
	VPBROADCASTD 12(R8), X2
	VPMADDWD X10, X2, X3
	VPADDD X3, X0, X0
	VPMADDWD X11, X2, X3
	VPADDD X3, X1, X1
	VPADDD X13, X0, X0
	VPADDD X13, X1, X1
	VPSRAD X14, X0, X0
	VPSRAD X14, X1, X1
	TESTQ R10, R10
	JNZ wide8
	VPACKSSDW X1, X0, X0
	VMOVDQU X0, (DI)
	ADDQ $16, DI
	JMP next8
wide8:
	VMOVDQU X0, (DI)
	VMOVDQU X1, 16(DI)
	ADDQ $32, DI
next8:
	ADDQ $16, R8
	DECQ CX
	JNZ k8
	VZEROUPPER
	RET

// func ssdAVX2(a *uint16, as int, b *uint16, bs int, w, h int) int
// Sum of squared differences of two w x h blocks of samples of at most 10
// bits (w a multiple of 16): VPMADDWD of the differences by themselves,
// in dwords per row (at most 16 * 2 * 1023^2 a register lane per row),
// added up in qwords.
TEXT ·ssdAVX2(SB), NOSPLIT, $0-56
	MOVQ a+0(FP), SI
	MOVQ as+8(FP), R8
	MOVQ b+16(FP), DI
	MOVQ bs+24(FP), R9
	MOVQ w+32(FP), CX
	MOVQ h+40(FP), DX
	SHLQ $1, R8
	SHLQ $1, R9
	VPXOR Y0, Y0, Y0          // qword sums
	VPXOR Y5, Y5, Y5
srow:
	MOVQ CX, AX
	MOVQ SI, R10
	MOVQ DI, R11
	VPXOR Y3, Y3, Y3          // the row's dword sums
scol:
	VMOVDQU (R10), Y1
	VPSUBW (R11), Y1, Y1
	VPMADDWD Y1, Y1, Y1
	VPADDD Y1, Y3, Y3
	ADDQ $32, R10
	ADDQ $32, R11
	SUBQ $16, AX
	JNZ scol
	VPUNPCKLDQ Y5, Y3, Y4
	VPADDQ Y4, Y0, Y0
	VPUNPCKHDQ Y5, Y3, Y4
	VPADDQ Y4, Y0, Y0
	ADDQ R8, SI
	ADDQ R9, DI
	DECQ DX
	JNZ srow
	VEXTRACTI128 $1, Y0, X1
	VPADDQ X1, X0, X0
	VPSHUFD $0x4E, X0, X1
	VPADDQ X1, X0, X0
	VMOVQ X0, AX
	MOVQ AX, ret+48(FP)
	VZEROUPPER
	RET

// func avgAVX2(dst *uint16, ds int, a *uint16, as int, b *uint16, bs int, w, h int)
// The rounded average of two w x h blocks of words (w a multiple of 8).
TEXT ·avgAVX2(SB), NOSPLIT, $0-64
	MOVQ dst+0(FP), DI
	MOVQ ds+8(FP), R8
	MOVQ a+16(FP), SI
	MOVQ as+24(FP), R9
	MOVQ b+32(FP), DX
	MOVQ bs+40(FP), R10
	MOVQ w+48(FP), CX
	MOVQ h+56(FP), R11
	SHLQ $1, R8
	SHLQ $1, R9
	SHLQ $1, R10
arow:
	XORQ AX, AX
	MOVQ CX, BX
a16:
	CMPQ BX, $16
	JL a8
	VMOVDQU (SI)(AX*1), Y0
	VPAVGW (DX)(AX*1), Y0, Y0
	VMOVDQU Y0, (DI)(AX*1)
	ADDQ $32, AX
	SUBQ $16, BX
	JMP a16
a8:
	TESTQ BX, BX
	JZ anext
	VMOVDQU (SI)(AX*1), X0
	VPAVGW (DX)(AX*1), X0, X0
	VMOVDQU X0, (DI)(AX*1)
anext:
	ADDQ R8, DI
	ADDQ R9, SI
	ADDQ R10, DX
	DECQ R11
	JNZ arow
	VZEROUPPER
	RET
