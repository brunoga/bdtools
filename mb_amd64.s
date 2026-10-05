//go:build amd64 && !purego

#include "textflag.h"

// func beginMBAsm(refs0, refs1 *int8, mvs0, mvs1 *mv, mvd0, mvd1 *[2]uint8, i4 *int8, stride int, mvd bool)
// Resets the 4x4-block grids of one macroblock: refs to -1, mvs and mvd
// to 0, intra modes to 2 (DC). stride is in blocks.
TEXT ·beginMBAsm(SB), NOSPLIT, $0-65
	MOVQ refs0+0(FP), AX
	MOVQ refs1+8(FP), BX
	MOVQ mvs0+16(FP), CX
	MOVQ mvs1+24(FP), DX
	MOVQ mvd0+32(FP), SI
	MOVQ mvd1+40(FP), DI
	MOVQ i4+48(FP), R8
	MOVQ stride+56(FP), R9
	MOVBLZX mvd+64(FP), R10
	VPXOR X0, X0, X0
	MOVL $0xffffffff, R11
	MOVL $0x02020202, R12
	MOVQ $4, R13
bm:
	MOVL R11, (AX)
	MOVL R11, (BX)
	VMOVDQU X0, (CX)
	VMOVDQU X0, (DX)
	MOVL R12, (R8)
	TESTL R10, R10
	JEQ bmn
	VMOVQ X0, (SI)
	VMOVQ X0, (DI)
	LEAQ (SI)(R9*2), SI
	LEAQ (DI)(R9*2), DI
bmn:
	ADDQ R9, AX
	ADDQ R9, BX
	LEAQ (CX)(R9*4), CX
	LEAQ (DX)(R9*4), DX
	ADDQ R9, R8
	DECQ R13
	JNE bm
	RET

// func fillMotionMBAsm(refs *int8, mvs *int32, stride, ref, mv int)
// Fills the 16 blocks of a macroblock with one reference index and
// motion vector (x | y<<16). stride is in blocks.
TEXT ·fillMotionMBAsm(SB), NOSPLIT, $0-40
	MOVQ refs+0(FP), AX
	MOVQ mvs+8(FP), CX
	MOVQ stride+16(FP), R9
	MOVQ ref+24(FP), BX
	MOVQ mv+32(FP), DX
	ANDL $0xff, BX
	IMULL $0x01010101, BX
	VMOVD DX, X0
	VPBROADCASTD X0, X0
	MOVL BX, (AX)
	VMOVDQU X0, (CX)
	MOVL BX, (AX)(R9*1)
	VMOVDQU X0, (CX)(R9*4)
	ADDQ R9, AX
	LEAQ (CX)(R9*4), CX
	MOVL BX, (AX)(R9*1)
	VMOVDQU X0, (CX)(R9*4)
	ADDQ R9, AX
	LEAQ (CX)(R9*4), CX
	MOVL BX, (AX)(R9*1)
	VMOVDQU X0, (CX)(R9*4)
	RET

// func fillIDsAsm(out0, out1 *int32, stride, id0, id1 int)
// Fills the reference ids of both lists for the 16 blocks of a macroblock.
TEXT ·fillIDsAsm(SB), NOSPLIT, $0-40
	MOVQ out0+0(FP), AX
	MOVQ out1+8(FP), CX
	MOVQ stride+16(FP), R9
	MOVQ id0+24(FP), BX
	MOVQ id1+32(FP), DX
	VMOVD BX, X0
	VPBROADCASTD X0, X0
	VMOVD DX, X1
	VPBROADCASTD X1, X1
	VMOVDQU X0, (AX)
	VMOVDQU X1, (CX)
	VMOVDQU X0, (AX)(R9*4)
	VMOVDQU X1, (CX)(R9*4)
	LEAQ (AX)(R9*4), AX
	LEAQ (CX)(R9*4), CX
	VMOVDQU X0, (AX)(R9*4)
	VMOVDQU X1, (CX)(R9*4)
	LEAQ (AX)(R9*4), AX
	LEAQ (CX)(R9*4), CX
	VMOVDQU X0, (AX)(R9*4)
	VMOVDQU X1, (CX)(R9*4)
	RET
