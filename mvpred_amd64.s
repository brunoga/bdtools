//go:build amd64 && !purego

#include "textflag.h"

// blkOrder: decoding order of 4x4 blocks by raster position.
DATA blkOrd<>+0(SB)/8, $0x0706030205040100
DATA blkOrd<>+8(SB)/8, $0x0f0e0b0a0d0c0908
GLOBL blkOrd<>(SB), RODATA, $16

// NB fetches the neighbour block (AX, BX) = (x, y) of the current
// macroblock: CX = refIdx (-1 if unavailable), DX = packed mv, R15 = 1 if
// available. SI/DI address block (0,0) of the refs/mvs grids, R8 is the
// grid stride and R9 the availability bits (A, B, C, D).
#define NB(yp, x0, bb, xp, f, d) \
	MOVQ $1, R15; \
	TESTQ BX, BX; \
	JGE yp; \
	TESTQ AX, AX; \
	JGE x0; \
	BTQ $3, R9; \
	SETCS R15; \
	JMP f; \
x0: \
	CMPQ AX, $4; \
	JLT bb; \
	BTQ $2, R9; \
	SETCS R15; \
	JMP f; \
bb: \
	BTQ $1, R9; \
	SETCS R15; \
	JMP f; \
yp: \
	TESTQ AX, AX; \
	JGE xp; \
	BTQ $0, R9; \
	SETCS R15; \
	JMP f; \
xp: \
	CMPQ AX, $4; \
	JLT f; \
	XORL R15, R15; \
f: \
	MOVQ $-1, CX; \
	XORL DX, DX; \
	TESTQ R15, R15; \
	JEQ d; \
	MOVQ BX, CX; \
	IMULQ R8, CX; \
	ADDQ AX, CX; \
	MOVL (DI)(CX*4), DX; \
	MOVBQSX (SI)(CX*1), CX; \
d:

// MINPOS computes minPositive(AX, BX) into AX: the smaller when both are
// non-negative, else the larger. Uses CX.
#define MINPOS \
	MOVQ AX, CX; \
	CMPQ AX, BX; \
	CMOVQGT BX, CX; \
	CMOVQLT BX, AX; \
	TESTQ CX, CX; \
	CMOVQGE CX, AX

// MEDIAN3 computes the median of the signed 16-bit values in AX, BX, CX
// into AX (uses DX).
#define MEDIAN3 \
	MOVQ AX, DX; \
	CMPQ AX, BX; \
	CMOVQGT BX, DX; \
	CMOVQGT AX, BX; \
	MOVQ DX, AX; \
	CMPQ BX, CX; \
	CMOVQGT CX, BX; \
	CMPQ AX, BX; \
	CMOVQLT BX, AX

// func mvPredFillAsm(refs *int8, mvs *int32, stride, avail, ref, x, y, w, h, mvdx, mvdy, fill int) int
// Derives the motion vector predictor of the partition (x,y,w,h) in 4x4
// blocks (8.4.1.3), adds the mvd, fills the partition's refs and mvs when
// fill is set, and returns the packed vector.
TEXT ·mvPredFillAsm(SB), NOSPLIT, $96-104
	MOVQ refs+0(FP), SI
	MOVQ mvs+8(FP), DI
	MOVQ stride+16(FP), R8
	MOVQ avail+24(FP), R9
	MOVQ x+40(FP), R10
	MOVQ y+48(FP), R11
	// A = (x-1, y)
	LEAQ -1(R10), AX
	MOVQ R11, BX
	NB(nbypa, nbx0a, nbba, nbxpa, nbfa, nbda)
	MOVQ CX, ra-8(SP)
	MOVQ DX, ma-16(SP)
	MOVQ R15, aa-24(SP)
	// B = (x, y-1)
	MOVQ R10, AX
	LEAQ -1(R11), BX
	NB(nbypb, nbx0b, nbbb, nbxpb, nbfb, nbdb)
	MOVQ CX, rb-32(SP)
	MOVQ DX, mb-40(SP)
	MOVQ R15, ab-48(SP)
	// P_Skip (fill bit 1): mv 0 when A or B is unavailable or has ref 0
	// and mv 0 (8.4.1.1)
	TESTQ $2, fill+88(FP)
	JEQ nopskip
	CMPQ aa-24(SP), $0
	JEQ pzero
	CMPQ ab-48(SP), $0
	JEQ pzero
	CMPQ ra-8(SP), $0
	JNE 3(PC)
	CMPQ ma-16(SP), $0
	JEQ pzero
	CMPQ rb-32(SP), $0
	JNE 3(PC)
	CMPQ mb-40(SP), $0
	JEQ pzero
nopskip:
	// C = (x+w, y-1), or D = (x-1, y-1) when C is later in decoding order
	// or unavailable
	MOVQ w+56(FP), AX
	ADDQ R10, AX
	LEAQ -1(R11), BX
	TESTQ BX, BX
	JLT cfetch
	CMPQ AX, $4
	JGE cused
	MOVQ BX, CX
	SHLQ $2, CX
	ADDQ AX, CX
	LEAQ blkOrd<>(SB), DX
	MOVBLZX (DX)(CX*1), CX
	MOVQ R11, R12
	SHLQ $2, R12
	ADDQ R10, R12
	MOVBLZX (DX)(R12*1), R12
	CMPQ CX, R12
	JGT cused
cfetch:
	NB(nbypc, nbx0c, nbbc, nbxpc, nbfc, nbdc)
	TESTQ R15, R15
	JNE cdone
cused:
	LEAQ -1(R10), AX
	LEAQ -1(R11), BX
	NB(nbypd, nbx0d, nbbd, nbxpd, nbfd, nbdd)
cdone:
	MOVQ CX, rc-56(SP)
	MOVQ DX, mc-64(SP)
	MOVQ R15, ac-72(SP)
	MOVQ ref+32(FP), R12
	CMPQ R12, $-2
	JNE notspatial
	// spatial direct (ref -2): ref = minPositive(A, B, C) (8.4.1.2.2),
	// returned in the low byte below the predictor
	MOVQ ra-8(SP), AX
	MOVQ rb-32(SP), BX
	MINPOS
	MOVQ rc-56(SP), BX
	MINPOS
	MOVQ AX, R12
	TESTQ R12, R12
	JGE notspatial
pzero:
	XORL AX, AX
	JMP fill
notspatial:
	// directional predictions for 16x8 and 8x16 partitions
	MOVQ w+56(FP), AX
	MOVQ h+64(FP), BX
	CMPQ AX, $4
	JNE not16x8
	CMPQ BX, $2
	JNE median
	TESTQ R11, R11
	JNE p16x8b
	CMPQ rb-32(SP), R12
	JNE median
	MOVQ mb-40(SP), AX
	JMP fill
p16x8b:
	CMPQ ra-8(SP), R12
	JNE median
	MOVQ ma-16(SP), AX
	JMP fill
not16x8:
	CMPQ AX, $2
	JNE median
	CMPQ BX, $4
	JNE median
	TESTQ R10, R10
	JNE p8x16b
	CMPQ ra-8(SP), R12
	JNE median
	MOVQ ma-16(SP), AX
	JMP fill
p8x16b:
	CMPQ rc-56(SP), R12
	JNE median
	MOVQ mc-64(SP), AX
	JMP fill
median:
	// only A available: use it
	MOVQ ab-48(SP), AX
	ORQ ac-72(SP), AX
	JNE medcount
	CMPQ aa-24(SP), $0
	JEQ medcount
	MOVQ ma-16(SP), AX
	JMP fill
medcount:
	XORL CX, CX               // number of neighbours with the same ref
	XORL AX, AX               // their mv
	CMPQ ra-8(SP), R12
	JNE mc1
	INCQ CX
	MOVQ ma-16(SP), AX
mc1:
	CMPQ rb-32(SP), R12
	JNE mc2
	INCQ CX
	MOVQ mb-40(SP), AX
mc2:
	CMPQ rc-56(SP), R12
	JNE mc3
	INCQ CX
	MOVQ mc-64(SP), AX
mc3:
	CMPQ CX, $1
	JEQ fill
	// component-wise median
	MOVWQSX ma-16(SP), AX
	MOVWQSX mb-40(SP), BX
	MOVWQSX mc-64(SP), CX
	MEDIAN3
	MOVQ AX, R13
	MOVWQSX ma-14(SP), AX
	MOVWQSX mb-38(SP), BX
	MOVWQSX mc-62(SP), CX
	MEDIAN3
	SHLQ $16, AX
	MOVWLZX R13, R13
	ORQ R13, AX
fill:
	// mv = pred + mvd, components added separately
	MOVWQSX AX, BX
	ADDQ mvdx+72(FP), BX
	SARQ $16, AX
	ADDQ mvdy+80(FP), AX
	SHLQ $16, AX
	MOVWLZX BX, BX
	ORQ BX, AX                // packed mv
	CMPQ ref+32(FP), $-2
	JNE 5(PC)
	SHLQ $8, AX
	MOVQ R12, BX
	ANDQ $0xff, BX
	ORQ BX, AX
	MOVQ AX, ret+96(FP)
	CMPQ fill+88(FP), $0
	JEQ nofill
	MOVQ R11, CX
	IMULQ R8, CX
	ADDQ R10, CX              // first block index
	ADDQ CX, SI
	LEAQ (DI)(CX*4), DI
	MOVQ h+64(FP), R13
	MOVQ ref+32(FP), R12
	ANDL $0xff, R12
	IMULL $0x01010101, R12    // ref replicated in 4 bytes
	VMOVD AX, X0
	VPBROADCASTD X0, X0       // mv replicated
	MOVQ w+56(FP), R14
	CMPQ R14, $4
	JEQ frow4
	CMPQ R14, $2
	JEQ frow2
frow1:
	MOVB R12, (SI)
	MOVL AX, (DI)
	ADDQ R8, SI
	LEAQ (DI)(R8*4), DI
	DECQ R13
	JNE frow1
	RET
frow2:
	MOVW R12, (SI)
	VMOVQ X0, (DI)
	ADDQ R8, SI
	LEAQ (DI)(R8*4), DI
	DECQ R13
	JNE frow2
	RET
frow4:
	MOVL R12, (SI)
	VMOVDQU X0, (DI)
	ADDQ R8, SI
	LEAQ (DI)(R8*4), DI
	DECQ R13
	JNE frow4
nofill:
	RET

// func directFillAsm(refs0, refs1 *int8, mvs0, mvs1 *int32, stride int,
//	colRefs0, colRefs1 *int8, colMvs0, colMvs1 *int32, mask, infer, ref0, ref1,
//	pmv0, pmv1, checkCol int) int
// Spatial direct prediction (8.4.1.2.2) of the 8x8 blocks in mask: each
// 4x4 block gets ref0/ref1 and the predictors, or zero vectors where the
// reference is 0 and the co-located block (with direct_8x8_inference
// when infer is set) is a short-term list-0 zero-motion block. Returns
// bit 0 set when all 16 blocks carry identical motion and bits 1-4 when
// each 8x8 block does (computed over the whole macroblock).
TEXT ·directFillAsm(SB), NOSPLIT, $32-136
	MOVQ refs0+0(FP), SI
	MOVQ refs1+8(FP), DI
	MOVQ mvs0+16(FP), R8
	MOVQ mvs1+24(FP), R9
	MOVQ stride+32(FP), R10
	XORL R14, R14             // block counter 0..15 (raster)
dfl:
	MOVQ R14, AX
	ANDQ $3, AX               // x
	MOVQ R14, BX
	SHRQ $2, BX               // y
	MOVQ BX, CX
	ANDQ $2, CX
	MOVQ AX, DX
	SHRQ $1, DX
	ADDQ DX, CX               // 8x8 index
	MOVQ mask+72(FP), DX
	BTQ CX, DX
	JCC dfnext
	// co-located zero test
	XORL R11, R11             // colZero
	CMPQ checkCol+120(FP), $0
	JEQ dfm
	MOVQ AX, DX
	MOVQ BX, R12
	CMPQ infer+80(FP), $0
	JEQ dfc
	SHRQ $1, DX
	LEAQ (DX)(DX*2), DX       // (x>>1)*3
	SHRQ $1, R12
	LEAQ (R12)(R12*2), R12
dfc:
	IMULQ R10, R12
	ADDQ DX, R12              // co-located block index
	MOVQ colRefs0+40(FP), DX
	MOVBQSX (DX)(R12*1), R13
	MOVQ colMvs0+56(FP), DX
	MOVL (DX)(R12*4), R11
	TESTQ R13, R13
	JGE dfz
	MOVQ colRefs1+48(FP), DX
	MOVBQSX (DX)(R12*1), R13
	MOVQ colMvs1+64(FP), DX
	MOVL (DX)(R12*4), R11
dfz:
	// colZero = colRef == 0 && both components in -1..1
	MOVQ R11, DX
	XORL R11, R11
	TESTQ R13, R13
	JNE dfm
	MOVWQSX DX, R13
	INCQ R13
	CMPQ R13, $2
	JA dfm
	SARQ $16, DX
	MOVWQSX DX, DX            // the vector was loaded zero-extended
	INCQ DX
	CMPQ DX, $2
	JA dfm
	MOVQ $1, R11
dfm:
	MOVQ BX, R12
	IMULQ R10, R12
	ADDQ AX, R12              // block index
	// list 0
	MOVQ ref0+88(FP), DX
	MOVB DX, (SI)(R12*1)
	MOVQ pmv0+104(FP), R13
	TESTQ DX, DX
	JLT df0z
	JNE df0s
	TESTQ R11, R11
	JEQ df0s
df0z:
	XORL R13, R13
df0s:
	MOVL R13, (R8)(R12*4)
	// list 1
	MOVQ ref1+96(FP), DX
	MOVB DX, (DI)(R12*1)
	MOVQ pmv1+112(FP), R13
	TESTQ DX, DX
	JLT df1z
	JNE df1s
	TESTQ R11, R11
	JEQ df1s
df1z:
	XORL R13, R13
df1s:
	MOVL R13, (R9)(R12*4)
dfnext:
	INCQ R14
	CMPQ R14, $16
	JLT dfl
	// uniformity: refs and mvs of both lists packed per block, compared
	// with the first block of each 8x8 (bits 1-4) and of the MB (bit 0)
	MOVQ $31, R15
	XORL R14, R14             // 8x8 index
ul:
	MOVQ R14, AX
	SHRQ $1, AX
	ADDQ AX, AX
	IMULQ R10, AX
	MOVQ R14, BX
	ANDQ $1, BX
	ADDQ BX, BX
	ADDQ BX, AX               // first block of the 8x8
	MOVBQZX (SI)(AX*1), R11
	MOVBQZX (DI)(AX*1), R12
	SHLQ $8, R12
	ORQ R12, R11
	MOVL (R8)(AX*4), R12
	SHLQ $16, R12
	ORQ R12, R11
	MOVL (R9)(AX*4), R12
	SHLQ $48, R12
	ORQ R12, R11              // 64-bit signature (mv1 truncated to 16 bits)
	MOVL (R9)(AX*4), R12
	SHRQ $16, R12
	MOVQ R12, mv1hi-8(SP)
	TESTQ R14, R14
	JNE ucmp
	MOVQ R11, ref-16(SP)
	MOVQ R12, refhi-24(SP)
	JMP uother
ucmp:
	CMPQ R11, ref-16(SP)
	JNE uclr0
	CMPQ R12, refhi-24(SP)
	JEQ uother
uclr0:
	ANDQ $-2, R15
uother:
	// the other three blocks of the 8x8: +1, +stride, +stride+1
	MOVQ $3, R13
uo:
	MOVQ AX, BX
	CMPQ R13, $3
	JNE 2(PC)
	INCQ BX
	CMPQ R13, $2
	JNE 2(PC)
	ADDQ R10, BX
	CMPQ R13, $1
	JNE 3(PC)
	ADDQ R10, BX
	INCQ BX
	MOVBQZX (SI)(BX*1), DX
	MOVBQZX (DI)(BX*1), CX
	SHLQ $8, CX
	ORQ CX, DX
	MOVL (R8)(BX*4), CX
	SHLQ $16, CX
	ORQ CX, DX
	MOVL (R9)(BX*4), CX
	SHLQ $48, CX
	ORQ CX, DX
	CMPQ DX, R11
	JNE uclr
	MOVL (R9)(BX*4), CX
	SHRQ $16, CX
	CMPQ CX, mv1hi-8(SP)
	JEQ unext
uclr:
	LEAQ 1(R14), CX
	BTRQ CX, R15
	ANDQ $-2, R15
unext:
	DECQ R13
	JNE uo
	INCQ R14
	CMPQ R14, $4
	JLT ul
	MOVQ R15, ret+128(FP)
	RET
