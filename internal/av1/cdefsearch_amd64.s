//go:build amd64 && !purego

#include "textflag.h"

// cdefParams offsets (see cdefsearch_amd64.go)
#define P_OFFS 0
#define P_PRIT 128
#define P_PRISH 192
#define P_TAP0 256
#define P_TAP1 320
#define P_SECT 384
#define P_SECSH 400
#define P_D 416
#define P_P 1056
#define P_S 1568
#define P_S0 1696
#define P_ACC 1824

// CONSTRAIN(d, t, sh, out, tmp): out = constrain(d, t) with the shift
// in sh (an XMM register); Y14 is zero.
#define CONSTRAIN(d, t, sh, out, tmp) \
	VPABSD d, out; \
	VPSRLD sh, out, tmp; \
	VPSUBD tmp, t, tmp; \
	VPMAXSD Y14, tmp, tmp; \
	VPMINSD out, tmp, out; \
	VPSIGND d, out, out

// func cdefSearch8AVX2(win *int32, src *uint16, stride int, p *cdefParams)
// cdefSearchBlock's sums of squared errors of a whole 8x8 luma block, as
// 8 lanes (by column) of each strength pair's in p.acc: the window's rows
// are 12 wide, the block's first sample at win.
TEXT ·cdefSearch8AVX2(SB), NOSPLIT, $0-32
	MOVQ win+0(FP), SI
	MOVQ src+8(FP), DI
	MOVQ stride+16(FP), DX
	SHLQ $1, DX // the source's stride in bytes
	MOVQ p+24(FP), BX
	MOVL $30000, AX
	VMOVD AX, X15
	VPBROADCASTD X15, Y15 // cdefVeryLarge
	VPXOR Y14, Y14, Y14
	MOVQ $8, R15 // rows
row:
	VMOVDQU (SI), Y0 // x
	VPMOVZXWD (DI), Y1 // the source
	VMOVDQA Y0, Y2 // mn
	VMOVDQA Y0, Y3 // mx
	// the 20 taps' differences: the first 12 (primary and secondary
	// taps along the direction) bound the clip
	XORQ CX, CX
diffs:
	MOVLQSX (BX)(CX*4), AX
	VMOVDQU (SI)(AX*1), Y4
	VPCMPEQD Y15, Y4, Y5
	CMPQ CX, $12
	JAE nominmax
	VPMINSD Y4, Y2, Y2
	VPANDN Y4, Y5, Y6
	VPMAXSD Y6, Y3, Y3
nominmax:
	VPSUBD Y0, Y4, Y4
	VPANDN Y4, Y5, Y4
	MOVQ CX, AX
	SHLQ $5, AX
	VMOVDQU Y4, P_D(BX)(AX*1)
	INCQ CX
	CMPQ CX, $20
	JB diffs
	// the primary sums, by strength 1..15 (Y13 a temporary)
	VMOVDQU (P_D+0)(BX), Y8
	VMOVDQU (P_D+32)(BX), Y9
	VMOVDQU (P_D+64)(BX), Y10
	VMOVDQU (P_D+96)(BX), Y11
	MOVQ $1, CX
pri:
	VPBROADCASTD P_PRIT(BX)(CX*4), Y7
	VMOVD P_PRISH(BX)(CX*4), X12
	CONSTRAIN(Y8, Y7, X12, Y4, Y5)
	CONSTRAIN(Y9, Y7, X12, Y6, Y5)
	VPADDD Y6, Y4, Y4
	VPBROADCASTD P_TAP0(BX)(CX*4), Y5
	VPMULLD Y5, Y4, Y4
	CONSTRAIN(Y10, Y7, X12, Y6, Y5)
	CONSTRAIN(Y11, Y7, X12, Y13, Y5)
	VPADDD Y13, Y6, Y6
	VPBROADCASTD P_TAP1(BX)(CX*4), Y5
	VPMULLD Y5, Y6, Y6
	VPADDD Y6, Y4, Y4
	MOVQ CX, AX
	SHLQ $5, AX
	VMOVDQU Y4, P_P(BX)(AX*1)
	INCQ CX
	CMPQ CX, $16
	JB pri
	// the secondary sums, by strength 1..3: with the primary's direction
	// (S) and with direction 0's (S0, the primary off)
	MOVQ $1, CX
sec:
	VPBROADCASTD P_SECT(BX)(CX*4), Y7
	VMOVD P_SECSH(BX)(CX*4), X12
	LEAQ P_D(BX), R8
	VPXOR Y8, Y8, Y8 // a
	VPXOR Y9, Y9, Y9 // b
	VPXOR Y10, Y10, Y10 // a0
	VPXOR Y11, Y11, Y11 // b0
	MOVQ $4, R9
secq:
	VMOVDQU 128(R8), Y6
	CONSTRAIN(Y6, Y7, X12, Y4, Y5)
	VPADDD Y4, Y8, Y8
	VMOVDQU 256(R8), Y6
	CONSTRAIN(Y6, Y7, X12, Y4, Y5)
	VPADDD Y4, Y9, Y9
	VMOVDQU 384(R8), Y6
	CONSTRAIN(Y6, Y7, X12, Y4, Y5)
	VPADDD Y4, Y10, Y10
	VMOVDQU 512(R8), Y6
	CONSTRAIN(Y6, Y7, X12, Y4, Y5)
	VPADDD Y4, Y11, Y11
	ADDQ $32, R8
	DECQ R9
	JNZ secq
	VPADDD Y8, Y8, Y8
	VPADDD Y9, Y8, Y8
	VPADDD Y10, Y10, Y10
	VPADDD Y11, Y10, Y10
	MOVQ CX, AX
	SHLQ $5, AX
	VMOVDQU Y8, P_S(BX)(AX*1)
	VMOVDQU Y10, P_S0(BX)(AX*1)
	INCQ CX
	CMPQ CX, $4
	JB sec
	// the errors: the primary off (unclipped), then each primary
	// strength with each secondary
	MOVL $8, AX
	VMOVD AX, X13
	VPBROADCASTD X13, Y13 // 8
	LEAQ P_ACC(BX), R9
	XORQ CX, CX
off:
	MOVQ CX, AX
	SHLQ $5, AX
	VMOVDQU P_S0(BX)(AX*1), Y4
	VPSRAD $31, Y4, Y5
	VPADDD Y5, Y4, Y4
	VPADDD Y13, Y4, Y4
	VPSRAD $4, Y4, Y4
	VPADDD Y0, Y4, Y4
	VPSUBD Y1, Y4, Y4
	VPMULLD Y4, Y4, Y4
	VPADDD (R9), Y4, Y4
	VMOVDQU Y4, (R9)
	ADDQ $32, R9
	INCQ CX
	CMPQ CX, $4
	JB off
	VMOVDQU (P_S+0)(BX), Y8
	VMOVDQU (P_S+32)(BX), Y9
	VMOVDQU (P_S+64)(BX), Y10
	VMOVDQU (P_S+96)(BX), Y11
	LEAQ (P_P+32)(BX), R8
	MOVQ $15, CX
prim:
	VMOVDQU (R8), Y7
	// ERR(s): acc += (clip(x + round(ps + s), mn, mx) - src)^2
#define ERR(s, o) \
	VPADDD s, Y7, Y4; \
	VPSRAD $31, Y4, Y5; \
	VPADDD Y5, Y4, Y4; \
	VPADDD Y13, Y4, Y4; \
	VPSRAD $4, Y4, Y4; \
	VPADDD Y0, Y4, Y4; \
	VPMAXSD Y2, Y4, Y4; \
	VPMINSD Y3, Y4, Y4; \
	VPSUBD Y1, Y4, Y4; \
	VPMULLD Y4, Y4, Y4; \
	VPADDD o(R9), Y4, Y4; \
	VMOVDQU Y4, o(R9)
	ERR(Y8, 0)
	ERR(Y9, 32)
	ERR(Y10, 64)
	ERR(Y11, 96)
	ADDQ $32, R8
	ADDQ $128, R9
	DECQ CX
	JNZ prim
	ADDQ $48, SI
	ADDQ DX, DI
	DECQ R15
	JNZ row
	VZEROUPPER
	RET
