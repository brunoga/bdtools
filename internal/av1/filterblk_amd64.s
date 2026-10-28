//go:build amd64 && !purego

#include "textflag.h"

// A block's sub-sample filters with VPMADDWD: the intermediate values
// are words (they fit: at most 23546 and at least -7166 at any bit
// depth), the kernel's taps in pairs (dwords of two words), each pair's
// products summed by VPMADDWD. The sums and their rounding are Go's int32
// arithmetic, so the results are filterRowGo's and filterColGo's.

// func filterHAVX2(mid *int16, src *uint16, ss, w, rows int, k *int16, add int32, sh uint64)
// The horizontal filter of rows rows of w samples (w a multiple of 8;
// each row's w+7 samples from src, ss apart) into mid (w apart): mid[c]
// = (sum of k[t]*src[c+t] + add) >> sh. Sixteen outputs a step: the even
// ones from dword loads at even offsets, the odd ones at odd offsets,
// interleaved as words.
TEXT ·filterHAVX2(SB), NOSPLIT, $0-64
	MOVQ mid+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ ss+16(FP), R8
	SHLQ $1, R8
	MOVQ w+24(FP), R9
	MOVQ rows+32(FP), DX
	MOVQ k+40(FP), AX
	VPBROADCASTD 0(AX), Y8
	VPBROADCASTD 4(AX), Y9
	VPBROADCASTD 8(AX), Y10
	VPBROADCASTD 12(AX), Y11
	MOVL add+48(FP), AX
	VMOVD AX, X12
	VPBROADCASTD X12, Y12
	VMOVQ sh+56(FP), X13

hRow:
	MOVQ SI, BX
	MOVQ R9, CX

hCol:
	CMPQ CX, $16
	JLT  h8
	VPMADDWD 0(BX), Y8, Y0
	VPMADDWD 2(BX), Y8, Y1
	VPMADDWD 4(BX), Y9, Y2
	VPMADDWD 6(BX), Y9, Y3
	VPADDD   Y2, Y0, Y0
	VPADDD   Y3, Y1, Y1
	VPMADDWD 8(BX), Y10, Y2
	VPMADDWD 10(BX), Y10, Y3
	VPADDD   Y2, Y0, Y0
	VPADDD   Y3, Y1, Y1
	VPMADDWD 12(BX), Y11, Y2
	VPMADDWD 14(BX), Y11, Y3
	VPADDD   Y2, Y0, Y0
	VPADDD   Y3, Y1, Y1
	VPADDD   Y12, Y0, Y0
	VPADDD   Y12, Y1, Y1
	VPSRAD   X13, Y0, Y0
	VPSRAD   X13, Y1, Y1
	VPSLLD   $16, Y1, Y1
	VPBLENDW $0xaa, Y1, Y0, Y0
	VMOVDQU  Y0, (DI)
	ADDQ     $32, BX
	ADDQ     $32, DI
	SUBQ     $16, CX
	JNZ      hCol
	JMP      hNext

h8:
	VPMADDWD 0(BX), X8, X0
	VPMADDWD 2(BX), X8, X1
	VPMADDWD 4(BX), X9, X2
	VPMADDWD 6(BX), X9, X3
	VPADDD   X2, X0, X0
	VPADDD   X3, X1, X1
	VPMADDWD 8(BX), X10, X2
	VPMADDWD 10(BX), X10, X3
	VPADDD   X2, X0, X0
	VPADDD   X3, X1, X1
	VPMADDWD 12(BX), X11, X2
	VPMADDWD 14(BX), X11, X3
	VPADDD   X2, X0, X0
	VPADDD   X3, X1, X1
	VPADDD   X12, X0, X0
	VPADDD   X12, X1, X1
	VPSRAD   X13, X0, X0
	VPSRAD   X13, X1, X1
	VPSLLD   $16, X1, X1
	VPBLENDW $0xaa, X1, X0, X0
	VMOVDQU  X0, (DI)
	ADDQ     $16, DI

hNext:
	ADDQ R8, SI
	DECQ DX
	JNZ  hRow
	VZEROUPPER
	RET

// vTaps16 sums the vertical filter's four pairs of rows (AX at the first,
// R11 the bytes a row) for 16 columns: the low words' (columns 0-3 and
// 8-11) in Y4, the high words' (4-7, 12-15) in Y5.
#define vTaps16 \
	VMOVDQU    (AX), Y0; \
	VMOVDQU    (AX)(R11*1), Y1; \
	VPUNPCKLWD Y1, Y0, Y2; \
	VPUNPCKHWD Y1, Y0, Y3; \
	VPMADDWD   Y8, Y2, Y4; \
	VPMADDWD   Y8, Y3, Y5; \
	LEAQ       (AX)(R11*2), AX; \
	VMOVDQU    (AX), Y0; \
	VMOVDQU    (AX)(R11*1), Y1; \
	VPUNPCKLWD Y1, Y0, Y2; \
	VPUNPCKHWD Y1, Y0, Y3; \
	VPMADDWD   Y9, Y2, Y2; \
	VPMADDWD   Y9, Y3, Y3; \
	VPADDD     Y2, Y4, Y4; \
	VPADDD     Y3, Y5, Y5; \
	LEAQ       (AX)(R11*2), AX; \
	VMOVDQU    (AX), Y0; \
	VMOVDQU    (AX)(R11*1), Y1; \
	VPUNPCKLWD Y1, Y0, Y2; \
	VPUNPCKHWD Y1, Y0, Y3; \
	VPMADDWD   Y10, Y2, Y2; \
	VPMADDWD   Y10, Y3, Y3; \
	VPADDD     Y2, Y4, Y4; \
	VPADDD     Y3, Y5, Y5; \
	LEAQ       (AX)(R11*2), AX; \
	VMOVDQU    (AX), Y0; \
	VMOVDQU    (AX)(R11*1), Y1; \
	VPUNPCKLWD Y1, Y0, Y2; \
	VPUNPCKHWD Y1, Y0, Y3; \
	VPMADDWD   Y11, Y2, Y2; \
	VPMADDWD   Y11, Y3, Y3; \
	VPADDD     Y2, Y4, Y4; \
	VPADDD     Y3, Y5, Y5; \
	VPADDD     Y12, Y4, Y4; \
	VPADDD     Y12, Y5, Y5; \
	VPSRAD     X13, Y4, Y4; \
	VPSRAD     X13, Y5, Y5

// vTaps8 is vTaps16 for 8 columns: 0-3 in X4, 4-7 in X5.
#define vTaps8 \
	VMOVDQU    (AX), X0; \
	VMOVDQU    (AX)(R11*1), X1; \
	VPUNPCKLWD X1, X0, X2; \
	VPUNPCKHWD X1, X0, X3; \
	VPMADDWD   X8, X2, X4; \
	VPMADDWD   X8, X3, X5; \
	LEAQ       (AX)(R11*2), AX; \
	VMOVDQU    (AX), X0; \
	VMOVDQU    (AX)(R11*1), X1; \
	VPUNPCKLWD X1, X0, X2; \
	VPUNPCKHWD X1, X0, X3; \
	VPMADDWD   X9, X2, X2; \
	VPMADDWD   X9, X3, X3; \
	VPADDD     X2, X4, X4; \
	VPADDD     X3, X5, X5; \
	LEAQ       (AX)(R11*2), AX; \
	VMOVDQU    (AX), X0; \
	VMOVDQU    (AX)(R11*1), X1; \
	VPUNPCKLWD X1, X0, X2; \
	VPUNPCKHWD X1, X0, X3; \
	VPMADDWD   X10, X2, X2; \
	VPMADDWD   X10, X3, X3; \
	VPADDD     X2, X4, X4; \
	VPADDD     X3, X5, X5; \
	LEAQ       (AX)(R11*2), AX; \
	VMOVDQU    (AX), X0; \
	VMOVDQU    (AX)(R11*1), X1; \
	VPUNPCKLWD X1, X0, X2; \
	VPUNPCKHWD X1, X0, X3; \
	VPMADDWD   X11, X2, X2; \
	VPMADDWD   X11, X3, X3; \
	VPADDD     X2, X4, X4; \
	VPADDD     X3, X5, X5; \
	VPADDD     X12, X4, X4; \
	VPADDD     X12, X5, X5; \
	VPSRAD     X13, X4, X4; \
	VPSRAD     X13, X5, X5

// func filterVClipAVX2(dst *uint16, ds int, mid *int16, w, h int, k *int16, add int32, sh uint64, maxV int32)
// The vertical filter of a w by h block (w a multiple of 8) from mid's
// rows (w apart, h+7 of them) into dst (ds apart), clipped to 0..maxV:
// dst[c] = (sum of k[t]*mid[c+t*w] + add) >> sh. Each pair of rows is
// interleaved as words for VPMADDWD with a pair of taps.
TEXT ·filterVClipAVX2(SB), NOSPLIT, $0-68
	MOVQ dst+0(FP), DI
	MOVQ ds+8(FP), R8
	MOVQ mid+16(FP), SI
	MOVQ w+24(FP), R9
	MOVQ R9, R11
	SHLQ $1, R11
	MOVQ h+32(FP), DX
	MOVQ k+40(FP), AX
	VPBROADCASTD 0(AX), Y8
	VPBROADCASTD 4(AX), Y9
	VPBROADCASTD 8(AX), Y10
	VPBROADCASTD 12(AX), Y11
	MOVL add+48(FP), AX
	VMOVD AX, X12
	VPBROADCASTD X12, Y12
	VMOVQ sh+56(FP), X13
	SHLQ $1, R8
	MOVL maxV+64(FP), AX
	VMOVD AX, X14
	VPBROADCASTW X14, Y14

vcRow:
	MOVQ SI, BX
	MOVQ DI, R10
	MOVQ R9, CX

vcCol:
	MOVQ BX, AX
	CMPQ CX, $16
	JLT  vc8
	vTaps16
	VPACKUSDW Y5, Y4, Y4
	VPMINUW   Y14, Y4, Y4
	VMOVDQU   Y4, (R10)
	ADDQ      $32, BX
	ADDQ      $32, R10
	SUBQ      $16, CX
	JNZ       vcCol
	JMP       vcNext

vc8:
	vTaps8
	VPACKUSDW X5, X4, X4
	VPMINUW   X14, X4, X4
	VMOVDQU   X4, (R10)

vcNext:
	ADDQ R11, SI
	ADDQ R8, DI
	DECQ DX
	JNZ  vcRow
	VZEROUPPER
	RET

// func filterV32AVX2(dst *int32, ds int, mid *int16, w, h int, k *int16, add int32, sh uint64)
// filterVClipAVX2 into dwords, unclipped (a compound's predictions).
TEXT ·filterV32AVX2(SB), NOSPLIT, $0-64
	MOVQ dst+0(FP), DI
	MOVQ ds+8(FP), R8
	MOVQ mid+16(FP), SI
	MOVQ w+24(FP), R9
	MOVQ R9, R11
	SHLQ $1, R11
	MOVQ h+32(FP), DX
	MOVQ k+40(FP), AX
	VPBROADCASTD 0(AX), Y8
	VPBROADCASTD 4(AX), Y9
	VPBROADCASTD 8(AX), Y10
	VPBROADCASTD 12(AX), Y11
	MOVL add+48(FP), AX
	VMOVD AX, X12
	VPBROADCASTD X12, Y12
	VMOVQ sh+56(FP), X13
	SHLQ $2, R8

v32Row:
	MOVQ SI, BX
	MOVQ DI, R10
	MOVQ R9, CX

v32Col:
	MOVQ BX, AX
	CMPQ CX, $16
	JLT  v328
	vTaps16
	VPERM2I128 $0x20, Y5, Y4, Y0
	VPERM2I128 $0x31, Y5, Y4, Y1
	VMOVDQU    Y0, (R10)
	VMOVDQU    Y1, 32(R10)
	ADDQ       $32, BX
	ADDQ       $64, R10
	SUBQ       $16, CX
	JNZ        v32Col
	JMP        v32Next

v328:
	vTaps8
	VMOVDQU X4, (R10)
	VMOVDQU X5, 16(R10)

v32Next:
	ADDQ R11, SI
	ADDQ R8, DI
	DECQ DX
	JNZ  v32Row
	VZEROUPPER
	RET
