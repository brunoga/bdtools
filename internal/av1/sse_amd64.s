//go:build amd64 && !purego

#include "textflag.h"

// func sseAVX2(a *uint16, as int, b *uint16, bs int, w, h int) int64
// The sum of squared differences of two w by h blocks of samples of at
// most 12 bits (w a multiple of 8; strides in samples). VPMADDWD squares
// the 16-bit differences and sums pairs; each row's dword sums (at most
// 16 squares a lane) are widened into four qword sums.
TEXT ·sseAVX2(SB), NOSPLIT, $0-56
	MOVQ a+0(FP), SI
	MOVQ as+8(FP), R8
	SHLQ $1, R8
	MOVQ b+16(FP), DI
	MOVQ bs+24(FP), R9
	SHLQ $1, R9
	MOVQ w+32(FP), R10
	MOVQ h+40(FP), DX
	VPXOR Y4, Y4, Y4
row:
	MOVQ SI, AX
	MOVQ DI, BX
	MOVQ R10, CX
	VPXOR X0, X0, X0
col:
	VMOVDQU (AX), X1
	VPSUBW (BX), X1, X1
	VPMADDWD X1, X1, X1
	VPADDD X1, X0, X0
	ADDQ $16, AX
	ADDQ $16, BX
	SUBQ $8, CX
	JNZ col
	VPMOVZXDQ X0, Y0
	VPADDQ Y0, Y4, Y4
	ADDQ R8, SI
	ADDQ R9, DI
	DECQ DX
	JNZ row
	VEXTRACTI128 $1, Y4, X0
	VPADDQ X0, X4, X4
	VPSHUFD $0x4e, X4, X0
	VPADDQ X0, X4, X4
	VMOVQ X4, AX
	MOVQ AX, ret+48(FP)
	VZEROUPPER
	RET
