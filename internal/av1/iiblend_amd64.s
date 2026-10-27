//go:build amd64 && !purego

#include "textflag.h"

// func blendAVX2(dst *uint16, dstStride int, intra, mask *uint16, w, h int)
// dst = (mask*intra + (64-mask)*dst + 32) >> 6 over a w by h block (w a
// multiple of 8; intra and mask w wide), in 32-bit lanes.
TEXT ·blendAVX2(SB), NOSPLIT, $0-48
	MOVQ dst+0(FP), DI
	MOVQ dstStride+8(FP), R8
	SHLQ $1, R8
	MOVQ intra+16(FP), SI
	MOVQ mask+24(FP), DX
	MOVQ w+32(FP), R9
	MOVQ h+40(FP), R10
	MOVL $64, AX
	VMOVD AX, X5
	VPBROADCASTD X5, Y5
	MOVL $32, AX
	VMOVD AX, X6
	VPBROADCASTD X6, Y6
row:
	MOVQ DI, BX
	MOVQ R9, CX
col:
	VPMOVZXWD (DX), Y0
	VPMOVZXWD (SI), Y1
	VPMOVZXWD (BX), Y2
	VPSUBD Y0, Y5, Y3
	VPMULLD Y0, Y1, Y1
	VPMULLD Y3, Y2, Y2
	VPADDD Y2, Y1, Y1
	VPADDD Y6, Y1, Y1
	VPSRLD $6, Y1, Y1
	VEXTRACTI128 $1, Y1, X2
	VPACKUSDW X2, X1, X1
	VMOVDQU X1, (BX)
	ADDQ $16, DX
	ADDQ $16, SI
	ADDQ $16, BX
	SUBQ $8, CX
	JNZ col
	ADDQ R8, DI
	DECQ R10
	JNZ row
	VZEROUPPER
	RET
