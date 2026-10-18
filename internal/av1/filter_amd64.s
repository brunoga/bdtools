//go:build amd64 && !purego

#include "textflag.h"

// The sub-sample filters, 8 outputs at a time: each tap's 8 samples (or
// intermediate values) widened to dwords, multiplied by the tap and
// summed, then rounded and shifted (VPMULLD and VPSRAD: Go's int32
// arithmetic).

// func filterRowAVX2(out *int32, row *uint16, n int, k *int32, taps int, add int32, sh uint64)
TEXT ·filterRowAVX2(SB), NOSPLIT, $0-56
	MOVQ out+0(FP), DI
	MOVQ row+8(FP), SI
	MOVQ n+16(FP), CX
	MOVQ k+24(FP), R8
	MOVQ taps+32(FP), R9
	MOVL         add+40(FP), AX
	VMOVQ        AX, X10
	VPBROADCASTD X10, Y10
	VMOVQ sh+48(FP), X11

rowLoop:
	VMOVDQA Y10, Y0
	MOVQ SI, R10
	MOVQ R8, R11
	MOVQ R9, R12

rowTap:
	VPMOVZXWD    (R10), Y1
	VPBROADCASTD (R11), Y2
	VPMULLD      Y2, Y1, Y1
	VPADDD       Y1, Y0, Y0
	ADDQ         $2, R10
	ADDQ         $4, R11
	DECQ         R12
	JNZ          rowTap

	VPSRAD  X11, Y0, Y0
	VMOVDQU Y0, (DI)
	ADDQ    $32, DI
	ADDQ    $16, SI
	SUBQ    $8, CX
	JNZ     rowLoop
	VZEROUPPER
	RET

// func filterColAVX2(out *int32, mid *int32, w int, n int, k *int32, taps int, add int32, sh uint64)
TEXT ·filterColAVX2(SB), NOSPLIT, $0-64
	MOVQ out+0(FP), DI
	MOVQ mid+8(FP), SI
	MOVQ w+16(FP), DX
	SHLQ $2, DX
	MOVQ n+24(FP), CX
	MOVQ k+32(FP), R8
	MOVQ taps+40(FP), R9
	MOVL         add+48(FP), AX
	VMOVQ        AX, X10
	VPBROADCASTD X10, Y10
	VMOVQ sh+56(FP), X11

colLoop:
	VMOVDQA Y10, Y0
	MOVQ SI, R10
	MOVQ R8, R11
	MOVQ R9, R12

colTap:
	VMOVDQU      (R10), Y1
	VPBROADCASTD (R11), Y2
	VPMULLD      Y2, Y1, Y1
	VPADDD       Y1, Y0, Y0
	ADDQ         DX, R10
	ADDQ         $4, R11
	DECQ         R12
	JNZ          colTap

	VPSRAD  X11, Y0, Y0
	VMOVDQU Y0, (DI)
	ADDQ    $32, DI
	ADDQ    $32, SI
	SUBQ    $8, CX
	JNZ     colLoop
	VZEROUPPER
	RET
