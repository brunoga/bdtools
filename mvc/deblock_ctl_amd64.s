//go:build amd64 && !purego

#include "textflag.h"
#include "funcdata.h"

// Deblocking control for one macroblock (8.7): boundary strengths, filter
// parameters and the calls into the edge filters. Field offsets of mbInfo
// (flags 0, qp 3, nzMask 12, mvEdges 14), sliceParams (alphaOffset 8,
// betaOffset 16, chromaQPOffset 24) and deblockArgs are hard-coded and
// checked by TestDeblockLayout.

// Shuffle masks producing 16 words from a broadcast dword of 4 bS bytes:
// word i = byte i>>2 (luma segments), or byte (i&7)>>1 (chroma).
DATA dbIdx4<>+0(SB)/8, $0x8000800080008000
DATA dbIdx4<>+8(SB)/8, $0x8001800180018001
DATA dbIdx4<>+16(SB)/8, $0x8002800280028002
DATA dbIdx4<>+24(SB)/8, $0x8003800380038003
GLOBL dbIdx4<>(SB), RODATA, $32
DATA dbIdxC<>+0(SB)/8, $0x8001800180008000
DATA dbIdxC<>+8(SB)/8, $0x8003800380028002
DATA dbIdxC<>+16(SB)/8, $0x8001800180008000
DATA dbIdxC<>+24(SB)/8, $0x8003800380028002
GLOBL dbIdxC<>(SB), RODATA, $32

// deblockArgs offsets
#define A_CUR 0
#define A_LEFT 8
#define A_TOP 16
#define A_SP 24
#define A_Y 32
#define A_CB 40
#define A_CR 48
#define A_STY 56
#define A_STC 64
#define A_REFS0 72
#define A_REFS1 80
#define A_MVS0 88
#define A_MVS1 96
#define A_ST4 104
#define A_CHROMA 112

// Frame: bs[2][4][4] at -32, lane arrays vbs -64, vtc -96, va -128, vb -160
// (16 words each), params: alpha -168, beta -176, ia -184, ca -200 (two
// qwords, growing upward), cb -216, cia -232; qp -240, inner -248 (set
// once the internal-edge parameters are saved at ipsave -320, 72 bytes);
// the outgoing call arguments at 0(SP) (64 bytes) lie below the locals.
// No register survives the filter calls (a stack growth in the callee's
// prologue clobbers them all): args is reloaded from -328 and the bs
// offset from -336 after each call.

// PARAMS computes the filter parameters for qpp in AX (qp in R9, args
// pointer in R8): alpha/beta/ia and the chroma ones. Clobbers AX-DX, SI,
// DI, R10, R11.
#define PARAMS(lbl1, lbl2, lbl3) \
	MOVQ A_SP(R8), SI; \
	LEAQ 1(AX)(R9*1), BX; \
	SARQ $1, BX; \
	MOVQ BX, CX; \
	ADDQ 8(SI), CX; \
	CLIP51; \
	MOVQ CX, ia-184(SP); \
	LEAQ ·alphaTab(SB), DX; \
	MOVBQZX (DX)(CX*1), DX; \
	MOVQ DX, alpha-168(SP); \
	MOVQ BX, CX; \
	ADDQ 16(SI), CX; \
	CLIP51; \
	LEAQ ·betaTab(SB), DX; \
	MOVBQZX (DX)(CX*1), DX; \
	MOVQ DX, beta-176(SP); \
	CMPQ A_CHROMA(R8), $0; \
	JEQ lbl3; \
	XORL DI, DI; \
lbl1: \
	MOVQ 24(SI)(DI*8), CX; \
	MOVQ R9, BX; \
	ADDQ CX, BX; \
	MOVQ BX, R11; \
	MOVQ R11, CX; \
	CLIP51; \
	LEAQ ·chromaQPTable(SB), DX; \
	MOVBQSX (DX)(CX*1), R11; \
	MOVQ AX, BX; \
	ADDQ 24(SI)(DI*8), BX; \
	MOVQ BX, CX; \
	CLIP51; \
	MOVBQSX (DX)(CX*1), R10; \
	LEAQ 1(R11)(R10*1), BX; \
	SARQ $1, BX; \
	MOVQ BX, CX; \
	ADDQ 8(SI), CX; \
	CLIP51; \
	MOVQ CX, cia-232(SP)(DI*8); \
	LEAQ ·alphaTab(SB), DX; \
	MOVBQZX (DX)(CX*1), DX; \
	MOVQ DX, ca-200(SP)(DI*8); \
	MOVQ BX, CX; \
	ADDQ 16(SI), CX; \
	CLIP51; \
	LEAQ ·betaTab(SB), DX; \
	MOVBQZX (DX)(CX*1), DX; \
	MOVQ DX, cb-216(SP)(DI*8); \
	INCQ DI; \
	CMPQ DI, $2; \
	JLT lbl1; \
lbl3:

// CLIP51 clamps CX to 0..51.
#define CLIP51 \
	TESTQ CX, CX; \
	JGE 2(PC); \
	XORL CX, CX; \
	CMPQ CX, $51; \
	JLE 2(PC); \
	MOVQ $51, CX

// bS validity masks indexed by (edge 0 filtered)*2 + transform 8x8:
// edge 0 bytes and, for 8x8 transforms, edges 1 and 3 are cleared.
DATA dbValid<>+0(SB)/8, $0xffffffff00000000
DATA dbValid<>+8(SB)/8, $0xffffffffffffffff
DATA dbValid<>+16(SB)/8, $0x0000000000000000
DATA dbValid<>+24(SB)/8, $0x00000000ffffffff
DATA dbValid<>+32(SB)/8, $0xffffffffffffffff
DATA dbValid<>+40(SB)/8, $0xffffffffffffffff
DATA dbValid<>+48(SB)/8, $0x00000000ffffffff
DATA dbValid<>+56(SB)/8, $0x00000000ffffffff
GLOBL dbValid<>(SB), RODATA, $64
// 4x4 byte transpose (raster y*4+x to x*4+y)
DATA dbTrans<>+0(SB)/8, $0x0d0905010c080400
DATA dbTrans<>+8(SB)/8, $0x0f0b07030e0a0602
GLOBL dbTrans<>(SB), RODATA, $16
DATA dbThree<>+0(SB)/2, $3
GLOBL dbThree<>(SB), RODATA, $2

// VALID loads the validity mask of a direction into dst: nb is the args
// offset of the neighbour, R13 the flags. Clobbers AX, BX.
#define VALID(nb, dst) \
	MOVQ R13, AX; \
	ANDQ $8, AX; \
	SHRQ $3, AX; \
	CMPQ nb(R14), $0; \
	JEQ 2(PC); \
	ORQ $2, AX; \
	SHLQ $4, AX; \
	LEAQ dbValid<>(SB), BX; \
	VMOVDQU (BX)(AX*1), dst

// LOADROWS loads grid rows r and r+1 (16 bytes each, DX bytes apart) at
// ptr+off into y.
#define LOADROWS(off, ptr, x, y) \
	VMOVDQU off(ptr), x; \
	VINSERTI128 $1, off(ptr)(DX*1), y, y

// MOTION computes, for the 8 blocks of Y0-Y7 (q mvs0/mvs1, p mvs0/mvs1,
// q refs0/refs1, p refs0/refs1), a bit per block (dword) in AX set where
// p and q have the same motion. Y14 = words of 3, Y15 = all ones.
// Clobbers Y8-Y13.
#define MOTION \
	VPCMPEQD Y4, Y6, Y8; \
	VPCMPEQD Y5, Y7, Y9; \
	VPAND Y9, Y8, Y8; \
	VPCMPEQD Y5, Y6, Y9; \
	VPCMPEQD Y4, Y7, Y10; \
	VPAND Y10, Y9, Y9; \
	VPSUBW Y0, Y2, Y10; \
	VPABSW Y10, Y10; \
	VPCMPGTW Y14, Y10, Y10; \
	VPSUBW Y1, Y3, Y11; \
	VPABSW Y11, Y11; \
	VPCMPGTW Y14, Y11, Y11; \
	VPOR Y11, Y10, Y10; \
	VPANDN Y8, Y10, Y10; \
	VPCMPEQD Y15, Y10, Y10; \
	VPSUBW Y1, Y2, Y11; \
	VPABSW Y11, Y11; \
	VPCMPGTW Y14, Y11, Y11; \
	VPSUBW Y0, Y3, Y12; \
	VPABSW Y12, Y12; \
	VPCMPGTW Y14, Y12, Y12; \
	VPOR Y12, Y11, Y11; \
	VPANDN Y9, Y11, Y11; \
	VPCMPEQD Y15, Y11, Y11; \
	VPOR Y11, Y10, Y10; \
	VMOVMSKPS Y10, AX

// func deblockMBAsm(a *deblockArgs)
TEXT ·deblockMBAsm(SB), $416-8
	NO_LOCAL_POINTERS
	MOVQ a+0(FP), R14
	MOVQ R14, args-328(SP)
	MOVQ A_CUR(R14), R9
	MOVBQSX 3(R9), AX
	MOVQ AX, qp-240(SP)
	MOVQ $0, inner-248(SP)
	MOVWLZX 0(R9), R13                 // flags
	VALID(A_LEFT, X12)
	VALID(A_TOP, X13)
	TESTL $1, R13
	JEQ inter
	// intra: 3 everywhere, 4 on the macroblock edges
	MOVL $0x03030303, AX
	VMOVD AX, X0
	VPBROADCASTD X0, X0
	MOVL $0x04040404, AX
	VMOVD AX, X1
	VPMAXUB X1, X0, X1
	VPAND X12, X1, X2
	VPAND X13, X1, X3
	VMOVDQU X2, bs-32(SP)
	VMOVDQU X3, bs-16(SP)
	JMP filter
inter:
	// Nothing to filter when this macroblock and its filtered neighbours
	// are inter with uniform motion, no residual, and the same reference
	// and motion vector (the common static case): all bS are 0.
	MOVBLZX 14(R9), AX                 // mvEdges
	MOVWLZX 12(R9), BX                 // nzMask
	ORL BX, AX
	JNE full
	MOVQ A_ST4(R14), DX
	SHLQ $2, DX                        // grid row stride in bytes
	MOVQ A_LEFT(R14), CX
	TESTQ CX, CX
	JEQ nbtop
	TESTW $1, 0(CX)                    // intra
	JNE full
	MOVBLZX 14(CX), AX
	MOVWLZX 12(CX), BX
	ORL BX, AX                         // mvEdges or nzMask
	JNE full
	MOVQ A_REFS0(R14), SI
	MOVL (SI), AX
	CMPL AX, -4(SI)
	JNE full
	MOVQ A_REFS1(R14), SI
	MOVL (SI), AX
	CMPL AX, -4(SI)
	JNE full
	MOVQ A_MVS0(R14), SI
	MOVL (SI), AX
	CMPL AX, -4(SI)
	JNE full
	MOVQ A_MVS1(R14), SI
	MOVL (SI), AX
	CMPL AX, -4(SI)
	JNE full
nbtop:
	MOVQ A_TOP(R14), CX
	TESTQ CX, CX
	JEQ nofilter
	TESTW $1, 0(CX)                    // intra
	JNE full
	MOVBLZX 14(CX), AX
	MOVWLZX 12(CX), BX
	ORL BX, AX                         // mvEdges or nzMask
	JNE full
	MOVQ A_REFS0(R14), SI
	SUBQ DX, SI
	MOVL (SI), AX
	CMPL AX, (SI)(DX*1)
	JNE full
	MOVQ A_REFS1(R14), SI
	SUBQ DX, SI
	MOVL (SI), AX
	CMPL AX, (SI)(DX*1)
	JNE full
	MOVQ A_MVS0(R14), SI
	SUBQ DX, SI
	MOVL (SI), AX
	CMPL AX, (SI)(DX*1)
	JNE full
	MOVQ A_MVS1(R14), SI
	SUBQ DX, SI
	MOVL (SI), AX
	CMPL AX, (SI)(DX*1)
	JNE full
nofilter:
	RET
full:
	MOVQ A_MVS0(R14), R8
	MOVQ A_MVS1(R14), R9
	MOVQ A_REFS0(R14), R10
	MOVQ A_REFS1(R14), R11
	MOVQ A_ST4(R14), DX
	SHLQ $2, DX                        // grid row stride in bytes
	VPBROADCASTW dbThree<>(SB), Y14
	VPCMPEQD Y15, Y15, Y15
	MOVQ $0x0101010101010101, R12
	// vertical edges: p is the block to the left
	LOADROWS(0, R8, X0, Y0)
	LOADROWS(0, R9, X1, Y1)
	LOADROWS(0, R10, X4, Y4)
	LOADROWS(0, R11, X5, Y5)
	CMPQ A_LEFT(R14), $0
	JEQ vnoleft0
	LOADROWS(-16, R8, X2, Y2)
	LOADROWS(-16, R9, X3, Y3)
	LOADROWS(-16, R10, X6, Y6)
	LOADROWS(-16, R11, X7, Y7)
	VPALIGNR $12, Y2, Y0, Y2
	VPALIGNR $12, Y3, Y1, Y3
	VPALIGNR $12, Y6, Y4, Y6
	VPALIGNR $12, Y7, Y5, Y7
	JMP vhalf0
vnoleft0:
	VPSLLDQ $4, Y0, Y2
	VPSLLDQ $4, Y1, Y3
	VPSLLDQ $4, Y4, Y6
	VPSLLDQ $4, Y5, Y7
vhalf0:
	MOTION
	MOVQ AX, SI                        // same-motion bits, rows 0-1
	LEAQ (R8)(DX*2), BX
	LOADROWS(0, BX, X0, Y0)
	LEAQ (R9)(DX*2), BX
	LOADROWS(0, BX, X1, Y1)
	LEAQ (R10)(DX*2), BX
	LOADROWS(0, BX, X4, Y4)
	LEAQ (R11)(DX*2), BX
	LOADROWS(0, BX, X5, Y5)
	CMPQ A_LEFT(R14), $0
	JEQ vnoleft1
	LEAQ (R8)(DX*2), BX
	LOADROWS(-16, BX, X2, Y2)
	LEAQ (R9)(DX*2), BX
	LOADROWS(-16, BX, X3, Y3)
	LEAQ (R10)(DX*2), BX
	LOADROWS(-16, BX, X6, Y6)
	LEAQ (R11)(DX*2), BX
	LOADROWS(-16, BX, X7, Y7)
	VPALIGNR $12, Y2, Y0, Y2
	VPALIGNR $12, Y3, Y1, Y3
	VPALIGNR $12, Y6, Y4, Y6
	VPALIGNR $12, Y7, Y5, Y7
	JMP vhalf1
vnoleft1:
	VPSLLDQ $4, Y0, Y2
	VPSLLDQ $4, Y1, Y3
	VPSLLDQ $4, Y4, Y6
	VPSLLDQ $4, Y5, Y7
vhalf1:
	MOTION
	SHLQ $8, AX
	ORQ SI, AX
	NOTQ AX
	ANDQ $0xffff, AX                   // motion bits (raster)
	// residual: nz of q or p, with the left macroblock's column 3
	MOVQ A_CUR(R14), DI
	MOVWLZX 12(DI), BX
	MOVQ BX, CX
	SHLQ $1, CX
	ANDQ $0xeeee, CX
	ORQ CX, BX
	MOVQ A_LEFT(R14), DI
	TESTQ DI, DI
	JEQ 5(PC)
	MOVWLZX 12(DI), CX
	SHRQ $3, CX
	ANDQ $0x1111, CX
	ORQ CX, BX
	ANDNQ AX, BX, AX                   // motion only where no residual
	// bytes: 2 for residual, 1 for motion
	PDEPQ R12, BX, CX
	SHLQ $1, CX
	PDEPQ R12, AX, SI
	ORQ SI, CX
	SHRQ $8, BX
	SHRQ $8, AX
	PDEPQ R12, BX, DI
	SHLQ $1, DI
	PDEPQ R12, AX, SI
	ORQ SI, DI
	VMOVQ CX, X0
	VPINSRQ $1, DI, X0, X0
	VPSHUFB dbTrans<>(SB), X0, X0      // [edge][segment]
	VALID(A_LEFT, X1)
	VPAND X1, X0, X0
	MOVQ A_LEFT(R14), CX
	TESTQ CX, CX
	JEQ vdone
	TESTW $1, 0(CX)
	JEQ vdone
	MOVL $0x04040404, AX               // intra neighbour
	VMOVD AX, X1
	VPMAXUB X1, X0, X0
vdone:
	VMOVDQU X0, bs-32(SP)

	// horizontal edges: p is the block above
	LOADROWS(0, R8, X0, Y0)
	LOADROWS(0, R9, X1, Y1)
	LOADROWS(0, R10, X4, Y4)
	LOADROWS(0, R11, X5, Y5)
	CMPQ A_TOP(R14), $0
	JEQ hnotop
	MOVQ R8, BX
	SUBQ DX, BX
	LOADROWS(0, BX, X2, Y2)
	MOVQ R9, BX
	SUBQ DX, BX
	LOADROWS(0, BX, X3, Y3)
	MOVQ R10, BX
	SUBQ DX, BX
	LOADROWS(0, BX, X6, Y6)
	MOVQ R11, BX
	SUBQ DX, BX
	LOADROWS(0, BX, X7, Y7)
	JMP hhalf0
hnotop:
	VPXOR X2, X2, X2
	VINSERTI128 $1, (R8), Y2, Y2
	VPXOR X3, X3, X3
	VINSERTI128 $1, (R9), Y3, Y3
	VPXOR X6, X6, X6
	VINSERTI128 $1, (R10), Y6, Y6
	VPXOR X7, X7, X7
	VINSERTI128 $1, (R11), Y7, Y7
hhalf0:
	MOTION
	MOVQ AX, SI
	LEAQ (R8)(DX*2), BX
	LOADROWS(0, BX, X0, Y0)
	LEAQ (R9)(DX*2), BX
	LOADROWS(0, BX, X1, Y1)
	LEAQ (R10)(DX*2), BX
	LOADROWS(0, BX, X4, Y4)
	LEAQ (R11)(DX*2), BX
	LOADROWS(0, BX, X5, Y5)
	LEAQ (R8)(DX*1), BX
	LOADROWS(0, BX, X2, Y2)
	LEAQ (R9)(DX*1), BX
	LOADROWS(0, BX, X3, Y3)
	LEAQ (R10)(DX*1), BX
	LOADROWS(0, BX, X6, Y6)
	LEAQ (R11)(DX*1), BX
	LOADROWS(0, BX, X7, Y7)
	MOTION
	SHLQ $8, AX
	ORQ SI, AX
	NOTQ AX
	ANDQ $0xffff, AX
	MOVQ A_CUR(R14), DI
	MOVWLZX 12(DI), BX
	MOVQ BX, CX
	SHLQ $4, CX
	ORQ CX, BX
	MOVQ A_TOP(R14), DI
	TESTQ DI, DI
	JEQ 4(PC)
	MOVWLZX 12(DI), CX
	SHRQ $12, CX
	ORQ CX, BX
	ANDQ $0xffff, BX
	ANDNQ AX, BX, AX
	PDEPQ R12, BX, CX
	SHLQ $1, CX
	PDEPQ R12, AX, SI
	ORQ SI, CX
	SHRQ $8, BX
	SHRQ $8, AX
	PDEPQ R12, BX, DI
	SHLQ $1, DI
	PDEPQ R12, AX, SI
	ORQ SI, DI
	VMOVQ CX, X0
	VPINSRQ $1, DI, X0, X0             // raster order is [edge][segment]
	VALID(A_TOP, X1)
	VPAND X1, X0, X0
	MOVQ A_TOP(R14), CX
	TESTQ CX, CX
	JEQ hdone
	TESTW $1, 0(CX)
	JEQ hdone
	MOVL $0x04040404, AX
	VMOVD AX, X1
	VPMAXUB X1, X0, X0
hdone:
	VMOVDQU X0, bs-16(SP)
	VPOR bs-32(SP), X0, X0
	VPTEST X0, X0
	JNE filter
	VZEROUPPER
	RET

filter:
	VZEROUPPER
	XORL R15, R15                      // bs offset = dir*16 + e*4
floop:
	MOVL bs-32(SP)(R15*1), AX
	TESTL AX, AX
	JEQ fnext
	MOVQ R15, bsoff-336(SP)
	MOVQ R14, R8
	MOVQ qp-240(SP), R9
	TESTQ $12, R15
	JEQ fedge
	CMPQ inner-248(SP), $0
	JEQ finner
	// internal edge: restore the saved parameters
	VMOVDQU ipsave-320(SP), Y0
	VMOVDQU Y0, cia-232(SP)
	VMOVDQU ipsave-288(SP), Y0
	VMOVDQU Y0, cia-200(SP)
	MOVQ ipsave-256(SP), AX
	MOVQ AX, alpha-168(SP)
	JMP fparamsdone
finner:
	// first internal edge needing filtering: compute and save
	MOVQ R9, AX
	PARAMS(ip1, ip2, ip3)
	VMOVDQU cia-232(SP), Y0
	VMOVDQU Y0, ipsave-320(SP)
	VMOVDQU cia-200(SP), Y0
	VMOVDQU Y0, ipsave-288(SP)
	MOVQ alpha-168(SP), AX
	MOVQ AX, ipsave-256(SP)
	MOVQ $1, inner-248(SP)
	JMP fparamsdone
fedge:
	MOVQ A_LEFT(R8), DX
	TESTQ $16, R15
	JEQ 2(PC)
	MOVQ A_TOP(R8), DX
	MOVBQSX 3(DX), AX
	PARAMS(fp1, fp2, fp3)
fparamsdone:
	MOVQ R15, R10
	SHRQ $4, R10                       // dir
	MOVQ R15, R11
	SHRQ $2, R11
	ANDQ $3, R11                       // e
	// luma: skipped when alpha or beta is 0
	MOVQ alpha-168(SP), AX
	TESTQ AX, AX
	JEQ fchroma
	MOVQ beta-176(SP), AX
	TESTQ AX, AX
	JEQ fchroma
	// lane arrays by shuffles: vbs[i] = bs[i>>2]; vtc[i] = tc0[vbs[i]]
	// with tc0 = {0, T[ia][0], T[ia][1], T[ia][2], 0..}
	VPBROADCASTD bs-32(SP)(R15*1), Y0
	VPSHUFB dbIdx4<>(SB), Y0, Y1       // words with the segment's bS
	VMOVDQU Y1, vbs-64(SP)
	MOVQ ia-184(SP), DX
	LEAQ (DX)(DX*2), DX
	LEAQ ·tc0Tab(SB), BX
	MOVL (BX)(DX*1), AX
	SHLL $8, AX
	VMOVD AX, X2
	VPBROADCASTQ X2, Y2
	VPSHUFB Y1, Y2, Y1
	VMOVDQU Y1, vtc-96(SP)
	// call filterLumaAsm(p, step, along, &vbs, &vtc, alpha, beta)
	MOVQ A_Y(R8), AX
	MOVQ A_STY(R8), DX
	TESTQ R10, R10
	JNE flv
	LEAQ (AX)(R11*4), AX
	MOVQ $1, 8(SP)
	MOVQ DX, 16(SP)
	JMP flc
flv:
	MOVQ R11, CX
	SHLQ $2, CX
	IMULQ DX, CX
	ADDQ CX, AX
	MOVQ DX, 8(SP)
	MOVQ $1, 16(SP)
flc:
	MOVQ AX, 0(SP)
	LEAQ vbs-64(SP), AX
	MOVQ AX, 24(SP)
	LEAQ vtc-96(SP), AX
	MOVQ AX, 32(SP)
	MOVQ alpha-168(SP), AX
	MOVQ AX, 40(SP)
	MOVQ beta-176(SP), AX
	MOVQ AX, 48(SP)
	CALL ·filterLumaAsm(SB)
	MOVQ args-328(SP), R14
	MOVQ bsoff-336(SP), R15
	MOVQ R14, R8
	MOVQ R15, R10
	SHRQ $4, R10
	MOVQ R15, R11
	SHRQ $2, R11
	ANDQ $3, R11
fchroma:
	CMPQ A_CHROMA(R8), $0
	JEQ fnext
	TESTQ $1, R11
	JNE fnext
	// lanes i = 0..15: segment (i&7)>>1, component i>>3
	VPBROADCASTD bs-32(SP)(R15*1), Y0
	VPSHUFB dbIdxC<>(SB), Y0, Y1
	VMOVDQU Y1, vbs-64(SP)
	// tc0 tables of both components, one per 128-bit lane
	MOVQ cia-232(SP), DX
	LEAQ (DX)(DX*2), DX
	LEAQ ·tc0Tab(SB), BX
	MOVL (BX)(DX*1), AX
	SHLL $8, AX
	VMOVD AX, X2
	VPBROADCASTQ X2, X2
	MOVQ cia-224(SP), DX
	LEAQ (DX)(DX*2), DX
	MOVL (BX)(DX*1), AX
	SHLL $8, AX
	VMOVD AX, X3
	VPBROADCASTQ X3, X3
	VINSERTI128 $1, X3, Y2, Y2
	VPSHUFB Y1, Y2, Y1
	VMOVDQU Y1, vtc-96(SP)
	// alpha and beta per component
	MOVQ ca-200(SP), AX
	VMOVQ AX, X4
	VPBROADCASTW X4, X4
	MOVQ ca-192(SP), AX
	VMOVQ AX, X5
	VPBROADCASTW X5, X5
	VINSERTI128 $1, X5, Y4, Y4
	VMOVDQU Y4, va-128(SP)
	MOVQ cb-216(SP), AX
	VMOVQ AX, X4
	VPBROADCASTW X4, X4
	MOVQ cb-208(SP), AX
	VMOVQ AX, X5
	VPBROADCASTW X5, X5
	VINSERTI128 $1, X5, Y4, Y4
	VMOVDQU Y4, vb-160(SP)
	// call filterChroma2Asm(cb, cr, step, along, &vbs, &vtc, &va, &vb)
	MOVQ A_CB(R8), AX
	MOVQ A_CR(R8), BX
	MOVQ A_STC(R8), DX
	TESTQ R10, R10
	JNE fcv
	LEAQ (AX)(R11*2), AX
	LEAQ (BX)(R11*2), BX
	MOVQ $1, 16(SP)
	MOVQ DX, 24(SP)
	JMP fcc
fcv:
	MOVQ R11, CX
	ADDQ CX, CX
	IMULQ DX, CX
	ADDQ CX, AX
	ADDQ CX, BX
	MOVQ DX, 16(SP)
	MOVQ $1, 24(SP)
fcc:
	MOVQ AX, 0(SP)
	MOVQ BX, 8(SP)
	LEAQ vbs-64(SP), AX
	MOVQ AX, 32(SP)
	LEAQ vtc-96(SP), AX
	MOVQ AX, 40(SP)
	LEAQ va-128(SP), AX
	MOVQ AX, 48(SP)
	LEAQ vb-160(SP), AX
	MOVQ AX, 56(SP)
	CALL ·filterChroma2Asm(SB)
	MOVQ args-328(SP), R14
	MOVQ bsoff-336(SP), R15
fnext:
	ADDQ $4, R15
	CMPQ R15, $32
	JLT floop
	RET
