//go:build amd64 && !purego

#include "textflag.h"

// func quantDZAVX2(coef *int32, q *int32, inv *uint16, n, den, qs int) int
// The levels of n coefficients (n a multiple of 8) quantized with step
// qs, as quantize's first pass does it: q[i] = sign(c) * min((|c|*den +
// qs/2) / qs, 1<<18), the division in double precision (exact: the
// quotient of integers below 2^31 is more than 2^-31 from the next
// integer). It returns the end of block: the largest inv[i]+1 of a
// non-zero level (0 when all are).
TEXT ·quantDZAVX2(SB), NOSPLIT, $0-56
	MOVQ coef+0(FP), SI
	MOVQ q+8(FP), DI
	MOVQ inv+16(FP), BX
	MOVQ n+24(FP), CX
	MOVQ den+32(FP), AX
	BSFQ AX, AX // den is 1, 2 or 4: its log2
	VMOVQ AX, X10
	MOVQ qs+40(FP), DX
	VMOVQ DX, X11
	VPBROADCASTD X11, Y11 // qs
	VCVTDQ2PD X11, Y12
	VPERMQ $0, Y12, Y12 // qs, double
	SHRQ $1, DX
	VMOVQ DX, X13
	VPBROADCASTD X13, Y13 // qs/2
	MOVL $0x40000, DX
	VMOVQ DX, X14
	VPBROADCASTD X14, Y14 // 1<<18
	MOVL $1, DX
	VMOVQ DX, X15
	VPBROADCASTD X15, Y15 // 1
	VPXOR Y9, Y9, Y9 // the end of block
	VPXOR Y8, Y8, Y8 // zero
loop:
	VMOVDQU (SI), Y0
	VPABSD Y0, Y1
	VPSLLD X10, Y1, Y1
	VPADDD Y13, Y1, Y1 // v = |c|*den + qs/2
	VPCMPGTD Y1, Y11, Y2 // qs > v: zero
	VPMOVMSKB Y2, AX
	CMPL AX, $-1
	JEQ zero
	VCVTDQ2PD X1, Y3
	VEXTRACTI128 $1, Y1, X4
	VCVTDQ2PD X4, Y4
	VDIVPD Y12, Y3, Y3
	VDIVPD Y12, Y4, Y4
	VCVTTPD2DQY Y3, X3
	VCVTTPD2DQY Y4, X4
	VINSERTI128 $1, X4, Y3, Y3
	VPMINSD Y14, Y3, Y3
	VPSIGND Y0, Y3, Y3
	VMOVDQU Y3, (DI)
	VPMOVZXWD (BX), Y5
	VPADDD Y15, Y5, Y5
	VPANDN Y5, Y2, Y5 // the non-zero's inv+1
	VPMAXSD Y5, Y9, Y9
	JMP next
zero:
	VMOVDQU Y8, (DI)
next:
	ADDQ $32, SI
	ADDQ $32, DI
	ADDQ $16, BX
	SUBQ $8, CX
	JNZ loop
	VEXTRACTI128 $1, Y9, X0
	VPMAXSD X0, X9, X9
	VPSHUFD $0x4e, X9, X0
	VPMAXSD X0, X9, X9
	VPSHUFD $0xb1, X9, X0
	VPMAXSD X0, X9, X9
	VMOVD X9, AX
	MOVQ AX, ret+48(FP)
	VZEROUPPER
	RET
