//go:build amd64 && !purego

#include "textflag.h"

// func residAVX2(src *uint16, ss int, pred *uint16, out *int32, w, h int)
// out (w wide) = src (stride ss samples) - pred (w wide), w x h, w a
// multiple of 4.
TEXT ·residAVX2(SB), NOSPLIT, $0-48
	MOVQ src+0(FP), SI
	MOVQ ss+8(FP), R8
	MOVQ pred+16(FP), DI
	MOVQ out+24(FP), DX
	MOVQ w+32(FP), CX
	MOVQ h+40(FP), BX
	SHLQ $1, R8
rrow:
	MOVQ CX, AX
	MOVQ SI, R10
r8:
	CMPQ AX, $8
	JL r4
	VPMOVZXWD (R10), Y1
	VPMOVZXWD (DI), Y2
	VPSUBD Y2, Y1, Y1
	VMOVDQU Y1, (DX)
	ADDQ $16, R10
	ADDQ $16, DI
	ADDQ $32, DX
	SUBQ $8, AX
	JMP r8
r4:
	CMPQ AX, $4
	JL rdone
	VPMOVZXWD (R10), X1
	VPMOVZXWD (DI), X2
	VPSUBD X2, X1, X1
	VMOVDQU X1, (DX)
	ADDQ $8, DI
	ADDQ $16, DX
rdone:
	ADDQ R8, SI
	DECQ BX
	JNZ rrow
	VZEROUPPER
	RET

// func reconAddAVX2(rec *uint16, rs int, res *int32, w, h, maxV int)
// rec (stride rs samples) += res (w wide), clamped to 0..maxV, w x h, w
// a multiple of 4.
TEXT ·reconAddAVX2(SB), NOSPLIT, $0-48
	MOVQ rec+0(FP), DI
	MOVQ rs+8(FP), R8
	MOVQ res+16(FP), SI
	MOVQ w+24(FP), CX
	MOVQ h+32(FP), BX
	MOVQ maxV+40(FP), AX
	SHLQ $1, R8
	VMOVD AX, X5
	VPBROADCASTD X5, Y5
	VPXOR Y4, Y4, Y4
arow:
	MOVQ CX, AX
	MOVQ DI, R10
a8:
	CMPQ AX, $8
	JL a4
	VPMOVZXWD (R10), Y1
	VPADDD (SI), Y1, Y1
	VPMAXSD Y4, Y1, Y1
	VPMINSD Y5, Y1, Y1
	VEXTRACTI128 $1, Y1, X2
	VPACKUSDW X2, X1, X1
	VMOVDQU X1, (R10)
	ADDQ $16, R10
	ADDQ $32, SI
	SUBQ $8, AX
	JMP a8
a4:
	CMPQ AX, $4
	JL adone
	VPMOVZXWD (R10), X1
	VPADDD (SI), X1, X1
	VPMAXSD X4, X1, X1
	VPMINSD X5, X1, X1
	VPACKUSDW X1, X1, X1
	VMOVQ X1, (R10)
	ADDQ $16, SI
adone:
	ADDQ R8, DI
	DECQ BX
	JNZ arow
	VZEROUPPER
	RET
