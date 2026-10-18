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

// func filterColClipAVX2(out *uint16, mid *int32, w int, n int, k *int32, taps int, add int32, sh uint64, maxV int32)
// filterColAVX2 with its outputs clipped to 0..maxV, as samples.
TEXT ·filterColClipAVX2(SB), NOSPLIT, $0-68
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
	MOVL         maxV+64(FP), AX
	VMOVQ        AX, X12
	VPBROADCASTW X12, X12

clipLoop:
	VMOVDQA Y10, Y0
	MOVQ SI, R10
	MOVQ R8, R11
	MOVQ R9, R12

clipTap:
	VMOVDQU      (R10), Y1
	VPBROADCASTD (R11), Y2
	VPMULLD      Y2, Y1, Y1
	VPADDD       Y1, Y0, Y0
	ADDQ         DX, R10
	ADDQ         $4, R11
	DECQ         R12
	JNZ          clipTap

	VPSRAD       X11, Y0, Y0
	VEXTRACTI128 $1, Y0, X1
	VPACKUSDW    X1, X0, X0
	VPMINUW      X12, X0, X0
	VMOVDQU      X0, (DI)
	ADDQ         $16, DI
	ADDQ         $32, SI
	SUBQ         $8, CX
	JNZ          clipLoop
	VZEROUPPER
	RET

// func widenAVX2(out *int32, src *uint16, n int, sh uint64)
// out[i] = src[i] << sh, 8 at a time.
TEXT ·widenAVX2(SB), NOSPLIT, $0-32
	MOVQ  out+0(FP), DI
	MOVQ  src+8(FP), SI
	MOVQ  n+16(FP), CX
	VMOVQ sh+24(FP), X11

widenLoop:
	VPMOVZXWD (SI), Y0
	VPSLLD    X11, Y0, Y0
	VMOVDQU   Y0, (DI)
	ADDQ      $32, DI
	ADDQ      $16, SI
	SUBQ      $8, CX
	JNZ       widenLoop
	VZEROUPPER
	RET

// func compAvgAVX2(out *uint16, p0, p1 *int32, n int, w0, w1, add int32, sh uint64, maxV int32)
// out[i] = clip((w0*p0[i] + w1*p1[i] + add) >> sh, 0, maxV), 8 at a time.
TEXT ·compAvgAVX2(SB), NOSPLIT, $0-60
	MOVQ         out+0(FP), DI
	MOVQ         p0+8(FP), SI
	MOVQ         p1+16(FP), R8
	MOVQ         n+24(FP), CX
	MOVL         w0+32(FP), AX
	VMOVQ        AX, X8
	VPBROADCASTD X8, Y8
	MOVL         w1+36(FP), AX
	VMOVQ        AX, X9
	VPBROADCASTD X9, Y9
	MOVL         add+40(FP), AX
	VMOVQ        AX, X10
	VPBROADCASTD X10, Y10
	VMOVQ        sh+48(FP), X11
	MOVL         maxV+56(FP), AX
	VMOVQ        AX, X12
	VPBROADCASTW X12, X12

avgLoop:
	VMOVDQU      (SI), Y0
	VMOVDQU      (R8), Y1
	VPMULLD      Y8, Y0, Y0
	VPMULLD      Y9, Y1, Y1
	VPADDD       Y1, Y0, Y0
	VPADDD       Y10, Y0, Y0
	VPSRAD       X11, Y0, Y0
	VEXTRACTI128 $1, Y0, X1
	VPACKUSDW    X1, X0, X0
	VPMINUW      X12, X0, X0
	VMOVDQU      X0, (DI)
	ADDQ         $16, DI
	ADDQ         $32, SI
	ADDQ         $32, R8
	SUBQ         $8, CX
	JNZ          avgLoop
	VZEROUPPER
	RET
