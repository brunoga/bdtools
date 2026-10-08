//go:build amd64 && !purego

#include "textflag.h"

// Register use in cabacResidAsm:
//   R8  codIRange (scaled, see cabac.go)   R9  codIOffset (scaled)
//   SI  context states                     DI  slice data
//   R10 next byte position                 R12, R13 loop state
//   R14 cabacLPS4               R15 cabacTrans
//   AX, BX, CX, DX, R11 scratch; DX holds the context index for DECIDE.

// REFILL shifts 48 new bits into the engine when the range is below thr.
// Clobbers AX, CX.
#define REFILL(thr, skip) \
	CMPQ R8, $thr; \
	JCC skip; \
	MOVQ lim-48(SP), CX; \
	MOVQ R10, AX; \
	CMPQ AX, CX; \
	CMOVQGT CX, AX; \
	MOVQ (DI)(AX*1), AX; \
	BSWAPQ AX; \
	SHRQ $16, AX; \
	ADDQ $6, R10; \
	SHLQ $48, R9; \
	ORQ AX, R9; \
	SHLQ $48, R8; \
skip:

// DECIDE decodes one context-coded bin (DecodeDecision) with context
// index DX, leaving the bin in R11. The MPS/LPS outcome is a branch: it is
// usually well predicted and keeps the range update off the critical path.
// Clobbers AX, BX, CX, R11.
#define DECIDE(lps, done) \
	MOVBLZX (SI)(DX*1), AX; \
	LZCNTQ R8, CX; \
	SHLXQ CX, R8, BX; \
	SHRQ $58, BX; \
	ANDL $24, BX; \
	MOVL AX, R11; \
	SHRL $1, R11; \
	MOVL (R14)(R11*4), R11; \
	SHRXL BX, R11, R11; \
	MOVBLZX R11B, R11; \
	NEGQ CX; \
	ADDQ $55, CX; \
	SHLXQ CX, R11, R11; \
	SUBQ R11, R8; \
	CMPQ R9, R8; \
	JCC lps; \
	MOVBLZX (R15)(AX*1), BX; \
	MOVB BX, (SI)(DX*1); \
	MOVL AX, R11; \
	ANDL $1, R11; \
	JMP done; \
lps: \
	SUBQ R8, R9; \
	MOVQ R11, R8; \
	MOVBLZX 128(R15)(AX*1), BX; \
	MOVB BX, (SI)(DX*1); \
	MOVL AX, R11; \
	ANDL $1, R11; \
	XORL $1, R11; \
done:

// BYPASS decodes one bypass bin into R11. Clobbers AX, CX, R11.
#define BYPASS(skip) \
	REFILL(512, skip); \
	SHRQ $1, R8; \
	XORL R11, R11; \
	MOVQ R9, CX; \
	SUBQ R8, CX; \
	CMPQ R9, R8; \
	SETCC R11B; \
	CMOVQCC CX, R9

// RESID decodes one residual block with the engine in R8 (range), R9
// (offset), R10 (position), SI (contexts), DI (data), R14/R15 (tables)
// and the block parameters in the stack slots cbfctx, tab, end, absbase,
// gt1max, cbptr, dqdst, dqscale, dqpos, dqsh. AX = 1 if the block was
// coded (n-16(SP) coefficients, indices in cb.idx), else 0. Expanded once
// per function (labels are function-scoped).
#define RESID \
	MOVQ cbfctx-88(SP), DX; \
	TESTQ DX, DX; \
	JLT rsig0; \
	DECIDE(rd1l, rd1d); \
	REFILL(256, rr0); \
	TESTL R11, R11; \
	JNE rsig0; \
	XORL AX, AX; \
	JMP rend; \
rsig0: \
	MOVQ tab-112(SP), R12; \
	XORL R13, R13; \
	MOVQ $0, n-16(SP); \
	PCALIGN $32; \
rsig: \
	MOVWLZX (R12)(R13*4), DX; \
	DECIDE(rd2l, rd2d); \
	REFILL(256, rr1); \
	TESTL R11, R11; \
	JEQ rsigz; \
	MOVQ cbptr-120(SP), AX; \
	MOVQ n-16(SP), CX; \
	MOVB R13, 8(AX)(CX*1); \
	INCQ CX; \
	MOVQ CX, n-16(SP); \
	MOVWLZX 2(R12)(R13*4), DX; \
	DECIDE(rd3l, rd3d); \
	REFILL(256, rr2); \
	TESTL R11, R11; \
	JNE rlevels; \
rsigz: \
	INCQ R13; \
	CMPQ R13, end-8(SP); \
	JLT rsig; \
	MOVQ cbptr-120(SP), AX; \
	MOVQ n-16(SP), CX; \
	MOVB R13, 8(AX)(CX*1); \
	INCQ CX; \
	MOVQ CX, n-16(SP); \
rlevels: \
	MOVQ cbptr-120(SP), AX; \
	MOVQ n-16(SP), CX; \
	MOVQ CX, 0(AX); \
	DECQ CX; \
	MOVQ CX, k-24(SP); \
	MOVQ $0, eq-32(SP); \
	XORL R12, R12; \
	PCALIGN $32; \
rlvl: \
	MOVQ absbase-96(SP), BX; \
	MOVQ gt1max-104(SP), AX; \
	MOVQ R12, CX; \
	CMPQ CX, AX; \
	CMOVQGT AX, CX; \
	LEAQ 5(BX)(CX*1), CX; \
	MOVQ CX, cn-40(SP); \
	MOVQ BX, DX; \
	TESTQ R12, R12; \
	JNE rpre0; \
	MOVQ eq-32(SP), CX; \
	INCQ CX; \
	MOVQ $4, AX; \
	CMPQ CX, AX; \
	CMOVQGT AX, CX; \
	ADDQ CX, DX; \
rpre0: \
	XORL R13, R13; \
	PCALIGN $32; \
rpre: \
	DECIDE(rd4l, rd4d); \
	REFILL(256, rr3); \
	TESTL R11, R11; \
	JEQ rpredone; \
	INCQ R13; \
	CMPQ R13, $14; \
	JEQ rpredone; \
	MOVQ cn-40(SP), DX; \
	JMP rpre; \
rpredone: \
	TESTQ R13, R13; \
	JNE rgt1; \
	INCQ eq-32(SP); \
	MOVL $1, R13; \
	JMP rsign; \
rgt1: \
	CMPQ R13, $14; \
	JNE rgt1done; \
	XORL DX, DX; \
reg: \
	BYPASS(rr4); \
	TESTL R11, R11; \
	JEQ regbits; \
	MOVL DX, CX; \
	MOVL $1, AX; \
	SHLQ CX, AX; \
	ADDQ AX, R13; \
	INCL DX; \
	CMPL DX, $31; \
	JLT reg; \
regbits: \
	TESTL DX, DX; \
	JEQ rgt1done; \
	XORL BX, BX; \
regb: \
	BYPASS(rr5); \
	SHLQ $1, BX; \
	ORQ R11, BX; \
	DECL DX; \
	JNE regb; \
	ADDQ BX, R13; \
rgt1done: \
	INCQ R13; \
	INCQ R12; \
rsign: \
	BYPASS(rr6); \
	MOVQ R13, AX; \
	NEGQ AX; \
	TESTL R11, R11; \
	CMOVQNE AX, R13; \
	MOVQ cbptr-120(SP), AX; \
	MOVQ k-24(SP), CX; \
	MOVQ dqdst-56(SP), DX; \
	TESTQ DX, DX; \
	JEQ rstorelvl; \
	MOVBLZX 8(AX)(CX*1), BX; \
	MOVQ dqscale-64(SP), AX; \
	IMULL (AX)(BX*4), R13; \
	MOVQ dqsh-80(SP), AX; \
	SHLXL AX, R13, R13; \
	SHRL $8, AX; \
	MOVL AX, R11; \
	SHRL $8, R11; \
	ADDL R11, R13; \
	SARXL AX, R13, R13; \
	MOVQ dqpos-72(SP), AX; \
	MOVBLZX (AX)(BX*1), BX; \
	MOVW R13, (DX)(BX*2); \
	JMP rnextlvl; \
rstorelvl: \
	MOVL R13, 72(AX)(CX*4); \
rnextlvl: \
	DECQ CX; \
	MOVQ CX, k-24(SP); \
	JGE rlvl; \
	MOVL $1, AX; \
rend:

// LOADENGINE loads the engine state of the cabac at e+0(FP).
#define LOADENGINE \
	MOVQ e+0(FP), AX; \
	MOVQ 0(AX), R9; \
	MOVQ 8(AX), R8; \
	MOVQ 16(AX), DI; \
	MOVQ 24(AX), CX; \
	SUBQ $8, CX; \
	MOVQ CX, lim-48(SP); \
	MOVQ 40(AX), R10; \
	MOVQ ctx+8(FP), SI; \
	LEAQ ·cabacLPS4(SB), R14; \
	LEAQ ·cabacTrans(SB), R15

#define STOREENGINE \
	MOVQ e+0(FP), AX; \
	MOVQ R9, 0(AX); \
	MOVQ R8, 8(AX); \
	MOVQ R10, 40(AX)

// func cabacResidAsm(e *cabac, ctx *uint8, tab *[2]uint16, cb *coeffBuf,
//	cbf, end, absBase, gt1Max int, dst *int16, scale *int32, pos *uint8,
//	shifts int) bool
TEXT ·cabacResidAsm(SB), NOSPLIT, $128-97
	LOADENGINE
	MOVQ tab+16(FP), AX
	MOVQ AX, tab-112(SP)
	MOVQ cb+24(FP), AX
	MOVQ AX, cbptr-120(SP)
	MOVQ cbf+32(FP), AX
	MOVQ AX, cbfctx-88(SP)
	MOVQ end+40(FP), AX
	MOVQ AX, end-8(SP)
	MOVQ absBase+48(FP), AX
	MOVQ AX, absbase-96(SP)
	MOVQ gt1Max+56(FP), AX
	MOVQ AX, gt1max-104(SP)
	MOVQ dst+64(FP), AX
	MOVQ AX, dqdst-56(SP)
	MOVQ scale+72(FP), AX
	MOVQ AX, dqscale-64(SP)
	MOVQ pos+80(FP), AX
	MOVQ AX, dqpos-72(SP)
	MOVQ shifts+88(FP), AX
	MOVQ AX, dqsh-80(SP)
	RESID
	MOVB AX, ret+96(FP)
	STOREENGINE
	RET

// func cabacBlocksAsm(e *cabac, ctx *uint8, tab *[2]uint16, cb *coeffBuf,
//	absBase, gt1Max, end, catBase int, desc *uint64, n, cbp int,
//	cbfA, cbfB, cbfCur int, dst *int16, scale0, scale1 *int32, pos *uint8,
//	sh0, sh1 int) (cbfOut, nz, ac int)
// Decodes the blocks described by desc (see blockDesc in cabac_amd64.go).
TEXT ·cabacBlocksAsm(SB), NOSPLIT, $256-184
	LOADENGINE
	MOVQ tab+16(FP), AX
	MOVQ AX, tab-112(SP)
	MOVQ cb+24(FP), AX
	MOVQ AX, cbptr-120(SP)
	MOVQ absBase+32(FP), AX
	MOVQ AX, absbase-96(SP)
	MOVQ gt1Max+40(FP), AX
	MOVQ AX, gt1max-104(SP)
	MOVQ end+48(FP), AX
	MOVQ AX, end-8(SP)
	MOVQ pos+136(FP), AX
	MOVQ AX, dqpos-72(SP)
	MOVQ cbfCur+104(FP), AX
	MOVQ AX, cbfcur-152(SP)
	MOVQ $0, nz-176(SP)
	MOVQ $0, ac-184(SP)
	MOVQ $0, blk-136(SP)
blkloop:
	MOVQ blk-136(SP), CX
	CMPQ CX, n+72(FP)
	JGE blkdone
	MOVQ desc+64(FP), AX
	MOVQ (AX)(CX*8), BX
	MOVQ BX, desc0-200(SP)
	MOVQ BX, AX
	ANDQ $31, AX
	MOVQ cbp+80(FP), DX
	BTQ AX, DX
	JCC blknext
	BTQ $22, BX
	JCS nocbf
	MOVQ BX, AX
	SHRQ $10, AX
	ANDQ $31, AX
	MOVQ cbfcur-152(SP), DX
	BTQ $20, BX
	JCS asrc
	MOVQ cbfA+88(FP), DX
asrc:
	SHRXQ AX, DX, DX
	ANDQ $1, DX
	MOVQ DX, R11
	MOVQ BX, AX
	SHRQ $15, AX
	ANDQ $31, AX
	MOVQ cbfcur-152(SP), DX
	BTQ $21, BX
	JCS bsrc
	MOVQ cbfB+96(FP), DX
bsrc:
	SHRXQ AX, DX, DX
	ANDQ $1, DX
	LEAQ (R11)(DX*2), DX
	ADDQ catBase+56(FP), DX
	MOVQ DX, cbfctx-88(SP)
	JMP setdq
nocbf:
	MOVQ $-1, cbfctx-88(SP)
setdq:
	MOVQ BX, AX
	SHRQ $24, AX
	ANDQ $0xffff, AX
	MOVQ dst+112(FP), DX
	LEAQ (DX)(AX*2), DX
	MOVQ DX, dqdst-56(SP)
	BTQ $23, BX
	JCS comp1
	MOVQ scale0+120(FP), AX
	MOVQ AX, dqscale-64(SP)
	MOVQ sh0+144(FP), AX
	MOVQ AX, dqsh-80(SP)
	JMP dqset
comp1:
	MOVQ scale1+128(FP), AX
	MOVQ AX, dqscale-64(SP)
	MOVQ sh1+152(FP), AX
	MOVQ AX, dqsh-80(SP)
dqset:
	RESID
	TESTL AX, AX
	JEQ blknext
	MOVQ desc0-200(SP), BX
	BTQ $22, BX
	JCS skipcbf
	MOVQ BX, AX
	SHRQ $5, AX
	ANDQ $31, AX
	BTSQ AX, cbfcur-152(SP)
skipcbf:
	MOVQ BX, AX
	SHRQ $40, AX
	ANDQ $31, AX
	BTSQ AX, nz-176(SP)
	MOVQ n-16(SP), CX
	CMPQ CX, $1
	JNE setac
	MOVQ cbptr-120(SP), DX
	CMPB 8(DX), $0
	JEQ blknext
setac:
	BTSQ AX, ac-184(SP)
blknext:
	INCQ blk-136(SP)
	JMP blkloop
blkdone:
	STOREENGINE
	MOVQ cbfcur-152(SP), AX
	MOVQ AX, cbfOut+160(FP)
	MOVQ nz-176(SP), AX
	MOVQ AX, nz+168(FP)
	MOVQ ac-184(SP), AX
	MOVQ AX, ac+176(FP)
	RET

// func cabacMvdAsm(e *cabac, ctx *uint8, grid *[2]uint8, stride, avail, w, h int) (mx, my int)
// Decodes an mvd pair (9.3.3.1.1.7, UEG3 binarization). g addresses the
// partition's first cell of the absolute-mvd grid (stride cells per row);
// avail bit 0 means the left cell is available, bit 1 the top cell. The
// clipped absolute values are stored to the w x h cells of the partition.
TEXT ·cabacMvdAsm(SB), NOSPLIT, $64-72
	LOADENGINE
	MOVQ grid+16(FP), BX
	MOVQ stride+24(FP), CX
	MOVQ avail+32(FP), DX
	// sums of the neighbouring absolute mvds, x in R12, y in R13
	XORL R12, R12
	XORL R13, R13
	BTQ $0, DX
	JCC mvnoleft
	MOVBLZX -2(BX), R12
	MOVBLZX -1(BX), R13
mvnoleft:
	BTQ $1, DX
	JCC mvnotop
	NEGQ CX
	MOVBLZX (BX)(CX*2), AX
	ADDQ AX, R12
	MOVBLZX 1(BX)(CX*2), AX
	ADDQ AX, R13
mvnotop:
	// increments: sum > 32 -> 2, sum >= 3 -> 1
	XORL AX, AX
	CMPQ R12, $3
	SETCC AX
	CMPQ R12, $32
	JLE mvincx
	MOVQ $2, AX
mvincx:
	MOVQ AX, incx-8(SP)
	XORL AX, AX
	CMPQ R13, $3
	SETCC AX
	CMPQ R13, $32
	JLE mvincy
	MOVQ $2, AX
mvincy:
	MOVQ AX, incy-16(SP)
	MOVQ $40, R12             // context base (x), then 47 (y)
	MOVQ incx-8(SP), R13
mvcomp:
	LEAQ (R12)(R13*1), DX
	XORL R13, R13             // prefix value
mvpre:
	DECIDE(md1l, md1d)
	REFILL(256, mr0)
	TESTL R11, R11
	JEQ mvpredone
	INCQ R13
	CMPQ R13, $9
	JEQ mvsuffix
	LEAQ 2(R13), DX           // ctxIdxInc = min(v+2, 6)
	MOVQ $6, AX
	CMPQ DX, AX
	CMOVQGT AX, DX
	ADDQ R12, DX
	JMP mvpre
mvsuffix:
	// Exp-Golomb k=3 suffix (DX counts k: BYPASS clobbers AX, CX, R11)
	MOVQ $3, DX
mveg:
	BYPASS(mr1)
	TESTL R11, R11
	JEQ mvegbits
	MOVL $1, AX
	SHLXQ DX, AX, AX
	ADDQ AX, R13
	INCQ DX
	CMPQ DX, $25
	JLT mveg
mvegbits:
	XORL BX, BX
mvegb:
	BYPASS(mr2)
	SHLQ $1, BX
	ORQ R11, BX
	DECQ DX
	JNE mvegb
	ADDQ BX, R13
mvpredone:
	TESTQ R13, R13
	JEQ mvstore
	BYPASS(mr3)
	MOVQ R13, AX
	NEGQ AX
	TESTL R11, R11
	CMOVQNE AX, R13
mvstore:
	CMPQ R12, $47
	JEQ mvy
	MOVQ R13, mx+56(FP)
	MOVQ $47, R12
	MOVQ incy-16(SP), R13
	JMP mvcomp
mvy:
	MOVQ R13, my+64(FP)
	STOREENGINE
	// store min(|mx|,64) | min(|my|,64)<<8 to the w x h cells
	MOVQ mx+56(FP), AX
	MOVQ AX, BX
	NEGQ BX
	TESTQ AX, AX
	CMOVQLT BX, AX
	MOVQ $64, CX
	CMPQ AX, CX
	CMOVQGT CX, AX
	MOVQ my+64(FP), BX
	MOVQ BX, DX
	NEGQ DX
	TESTQ BX, BX
	CMOVQLT DX, BX
	CMPQ BX, CX
	CMOVQGT CX, BX
	SHLQ $8, BX
	ORQ BX, AX                // packed cell value
	MOVQ grid+16(FP), BX
	MOVQ stride+24(FP), CX
	ADDQ CX, CX               // bytes per row
	MOVQ h+48(FP), R8
mvrow:
	MOVQ w+40(FP), R9
	MOVQ BX, DX
mvcell:
	MOVW AX, (DX)
	ADDQ $2, DX
	DECQ R9
	JNE mvcell
	ADDQ CX, BX
	DECQ R8
	JNE mvrow
	RET

// func cabacCBPAsm(e *cabac, ctx *uint8, lumaA, lumaB, chromaA, chromaB, chroma int) int
// Decodes coded_block_pattern; lumaA/lumaB are the neighbours' luma bits
// (0xf when unavailable), chromaA/chromaB their chroma values (0 when
// unavailable); chroma selects the chroma part.
TEXT ·cabacCBPAsm(SB), NOSPLIT, $64-64
	LOADENGINE
	XORL R12, R12             // cbp
	XORL R13, R13             // b8
cbpl:
	// la: x == 0 -> lumaA bit b8+1, else cbp bit b8-1
	MOVQ R13, CX
	ANDQ $1, CX
	JNE cbpla1
	MOVQ lumaA+16(FP), AX
	LEAQ 1(R13), CX
	SHRXQ CX, AX, AX
	JMP cbpla
cbpla1:
	LEAQ -1(R13), CX
	SHRXQ CX, R12, AX
cbpla:
	ANDQ $1, AX
	XORQ $1, AX
	MOVQ AX, BX               // condTermA
	CMPQ R13, $2
	JGE cbplb1
	MOVQ lumaB+24(FP), AX
	LEAQ 2(R13), CX
	SHRXQ CX, AX, AX
	JMP cbplb
cbplb1:
	LEAQ -2(R13), CX
	SHRXQ CX, R12, AX
cbplb:
	ANDQ $1, AX
	XORQ $1, AX
	LEAQ 73(BX)(AX*2), DX
	DECIDE(cd1l, cd1d)
	REFILL(256, cr0)
	MOVQ R13, CX
	SHLXQ CX, R11, R11
	ORQ R11, R12
	INCQ R13
	CMPQ R13, $4
	JLT cbpl
	MOVQ chroma+48(FP), AX
	TESTQ AX, AX
	JEQ cbpdone
	XORL DX, DX
	MOVQ chromaA+32(FP), AX
	TESTQ AX, AX
	JEQ cbpc1
	INCQ DX
cbpc1:
	MOVQ chromaB+40(FP), AX
	TESTQ AX, AX
	JEQ cbpc2
	ADDQ $2, DX
cbpc2:
	ADDQ $77, DX
	DECIDE(cd2l, cd2d)
	REFILL(256, cr1)
	TESTL R11, R11
	JEQ cbpdone
	MOVQ $4, DX
	CMPQ chromaA+32(FP), $2
	JNE cbpc3
	INCQ DX
cbpc3:
	CMPQ chromaB+40(FP), $2
	JNE cbpc4
	ADDQ $2, DX
cbpc4:
	ADDQ $77, DX
	DECIDE(cd3l, cd3d)
	REFILL(256, cr2)
	INCQ R11
	SHLQ $4, R11
	ORQ R11, R12
cbpdone:
	MOVQ R12, ret+56(FP)
	STOREENGINE
	RET

// func cpuidAsm(leaf, sub uint32) (eax, ebx, ecx, edx uint32)
TEXT ·cpuidAsm(SB), NOSPLIT, $0-24
	MOVL leaf+0(FP), AX
	MOVL sub+4(FP), CX
	CPUID
	MOVL AX, eax+8(FP)
	MOVL BX, ebx+12(FP)
	MOVL CX, ecx+16(FP)
	MOVL DX, edx+20(FP)
	RET

// func cabacChromaDCAsm(e *cabac, ctx *uint8, cb *coeffBuf, cbfA, cbfB, cbfCur int,
//	coef *int16, scale0, scale1, sh0, sh1 int) (cbfOut, nz int)
// Decodes the chroma DC blocks of both components (coded_block_flag,
// residual, 2x2 transform and scaling, 8.5.11) into coef (Cb at 0, Cr at
// 64 words, block b at (b>>1)*32 + (b&1)*4). scale is LevelScale(qp%6,0,0)
// and sh = qp/6 per component. nz holds the nonzero blocks (bits 0-3 Cb,
// 4-7 Cr).
TEXT ·cabacChromaDCAsm(SB), NOSPLIT, $224-104
	LOADENGINE
	MOVQ cb+16(FP), AX
	MOVQ AX, cbptr-120(SP)
	LEAQ ·sigLastCtx+768(SB), AX      // sigLastCtx[catChromaDC]: 3*64*2*2 bytes in
	MOVQ AX, tab-112(SP)
	MOVQ $3, end-8(SP)
	MOVQ $257, absbase-96(SP)         // 227 + 30
	MOVQ $3, gt1max-104(SP)
	MOVQ $0, dqdst-56(SP)
	MOVQ cbfCur+40(FP), AX
	MOVQ AX, cbfcur-152(SP)
	MOVQ $0, nz-176(SP)
	MOVQ $0, blk-136(SP)              // component
cdc:
	// cbf context: 97 + a + 2b with the DC bits (25 + c) of the masks
	MOVQ blk-136(SP), CX
	ADDQ $25, CX
	MOVQ cbfA+24(FP), AX
	SHRXQ CX, AX, AX
	ANDQ $1, AX
	MOVQ cbfB+32(FP), DX
	SHRXQ CX, DX, DX
	ANDQ $1, DX
	LEAQ 97(AX)(DX*2), DX
	MOVQ DX, cbfctx-88(SP)
	RESID
	TESTL AX, AX
	JEQ cdcnext
	MOVQ blk-136(SP), CX
	ADDQ $25, CX
	BTSQ CX, cbfcur-152(SP)
	// gather the four coefficients
	XORL R12, R12
	MOVQ R12, dc-192(SP)
	MOVQ R12, dc-184(SP)
	MOVQ cbptr-120(SP), AX
	MOVQ n-16(SP), CX
	LEAQ dc-192(SP), DX               // 4 int32
cdcg:
	DECQ CX
	JLT cdch
	MOVBLZX 8(AX)(CX*1), BX
	MOVL 72(AX)(CX*4), R11
	MOVL R11, (DX)(BX*4)
	JMP cdcg
cdch:
	// Hadamard: f0 = a+b+c+d, f1 = a-b+c-d, f2 = a+b-c-d, f3 = a-b-c+d
	// (R8-R10, SI, DI, R14 and R15 hold the engine state and must survive)
	MOVLQSX dc-192(SP), AX
	MOVLQSX dc-188(SP), BX
	MOVLQSX dc-184(SP), CX
	MOVLQSX dc-180(SP), DX
	MOVQ AX, R11
	ADDQ CX, R11                      // a+c
	SUBQ CX, AX                       // a-c
	MOVQ BX, R12
	ADDQ DX, R12                      // b+d
	SUBQ DX, BX                       // b-d
	MOVQ R11, CX
	ADDQ R12, CX                      // f0
	SUBQ R12, R11                     // f1
	MOVQ AX, DX
	ADDQ BX, DX                       // f2
	SUBQ BX, AX                       // f3
	MOVQ CX, dc-192(SP)
	MOVQ R11, dc-184(SP)
	MOVQ DX, f2-200(SP)
	MOVQ AX, f3-208(SP)
	// scale: (f*scale << sh) >> 5, by component
	MOVQ blk-136(SP), R11
	MOVQ scale0+56(FP), R12
	MOVQ sh0+72(FP), R13
	TESTQ R11, R11
	JEQ cdcs
	MOVQ scale1+64(FP), R12
	MOVQ sh1+80(FP), R13
cdcs:
	MOVQ R11, BX
	SHLQ $2, BX                       // nz bit base
	MOVQ coef+48(FP), CX
	SHLQ $7, R11
	ADDQ R11, CX                      // component's coefficients
	MOVQ dc-192(SP), AX
	IMULQ R12, AX
	SHLXQ R13, AX, AX
	SARQ $5, AX
	TESTQ AX, AX
	JEQ cdc1
	MOVW AX, (CX)
	BTSQ BX, nz-176(SP)
cdc1:
	MOVQ dc-184(SP), AX
	IMULQ R12, AX
	SHLXQ R13, AX, AX
	SARQ $5, AX
	TESTQ AX, AX
	JEQ cdc2
	MOVW AX, 8(CX)
	LEAQ 1(BX), DX
	BTSQ DX, nz-176(SP)
cdc2:
	MOVQ f2-200(SP), AX
	IMULQ R12, AX
	SHLXQ R13, AX, AX
	SARQ $5, AX
	TESTQ AX, AX
	JEQ cdc3
	MOVW AX, 64(CX)
	LEAQ 2(BX), DX
	BTSQ DX, nz-176(SP)
cdc3:
	MOVQ f3-208(SP), AX
	IMULQ R12, AX
	SHLXQ R13, AX, AX
	SARQ $5, AX
	TESTQ AX, AX
	JEQ cdcnext
	MOVW AX, 72(CX)
	LEAQ 3(BX), DX
	BTSQ DX, nz-176(SP)
cdcnext:
	INCQ blk-136(SP)
	CMPQ blk-136(SP), $2
	JLT cdc
	STOREENGINE
	MOVQ cbfcur-152(SP), AX
	MOVQ AX, cbfOut+88(FP)
	MOVQ nz-176(SP), AX
	MOVQ AX, nz+96(FP)
	RET

// func cabacIntraModesAsm(e *cabac, ctx *uint8, n int, prev *bool, rem *int8)
// Decodes n prev_intra_pred_mode_flag / rem_intra_pred_mode pairs.
TEXT ·cabacIntraModesAsm(SB), NOSPLIT, $64-40
	LOADENGINE
	MOVQ n+16(FP), R12
	MOVQ prev+24(FP), R13
	MOVQ rem+32(FP), BX
	MOVQ BX, rem-8(SP)
iml:
	MOVQ $68, DX
	DECIDE(id1l, id1d)
	REFILL(256, ir0)
	MOVB R11, (R13)
	TESTL R11, R11
	JNE imnext
	MOVQ $69, DX
	DECIDE(id2l, id2d)
	REFILL(256, ir1)
	MOVQ R11, acc-16(SP)      // DECIDE clobbers AX, BX, CX, R11
	MOVQ $69, DX
	DECIDE(id3l, id3d)
	REFILL(256, ir2)
	SHLQ $1, R11
	ORQ R11, acc-16(SP)
	MOVQ $69, DX
	DECIDE(id4l, id4d)
	REFILL(256, ir3)
	SHLQ $2, R11
	ORQ R11, acc-16(SP)
	MOVQ rem-8(SP), AX
	MOVQ acc-16(SP), BX
	MOVB BX, (AX)
imnext:
	INCQ R13
	INCQ rem-8(SP)
	DECQ R12
	JNE iml
	STOREENGINE
	RET

