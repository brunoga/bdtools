//go:build amd64 && !purego

#include "textflag.h"

// func rankEstAVX2(coef *int32, inv *uint16, n, dsh, qs int) (d int64, nb, eob int)
// rankTxTypes' estimate of n coefficients (n a multiple of 8) quantized
// with step qs: the sum of the squared errors, the levels' bits (3 +
// 2*floor(log2 l) a non-zero level l) and the end of block (the largest
// inv[i]+1 of a non-zero level). The quotient is taken in double
// precision, exact as quantDZAVX2's.
TEXT ·rankEstAVX2(SB), NOSPLIT, $0-64
	MOVQ coef+0(FP), SI
	MOVQ inv+8(FP), BX
	MOVQ n+16(FP), CX
	MOVQ dsh+24(FP), AX
	VMOVQ AX, X10 // dsh
	MOVQ qs+32(FP), DX
	VMOVQ DX, X11
	VPBROADCASTD X11, Y11 // qs
	VCVTDQ2PD X11, Y12
	VPERMQ $0, Y12, Y12 // qs, double
	SHRQ $1, DX
	VMOVQ DX, X13
	VPBROADCASTD X13, Y13 // qs/2
	MOVL $0xFFFFFF, DX
	VMOVQ DX, X14
	VPBROADCASTD X14, Y14 // 0xFFFFFF
	MOVL $1, DX
	VMOVQ DX, X15
	VPBROADCASTD X15, Y15 // 1
	VPXOR Y9, Y9, Y9 // the end of block
	VPXOR Y8, Y8, Y8 // the squared errors, as 4 quadwords
	VPXOR Y7, Y7, Y7 // the bits
loop:
	VMOVDQU (SI), Y0
	VPABSD Y0, Y0 // a
	VPSLLD X10, Y0, Y1
	VPADDD Y13, Y1, Y1 // r = a<<dsh + qs/2
	VPCMPGTD Y1, Y11, Y2 // qs > r: a level of 0
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
	VINSERTI128 $1, X4, Y3, Y3 // l
	VPMULLD Y11, Y3, Y4
	VPAND Y14, Y4, Y4
	VPSRLD X10, Y4, Y4
	VPSUBD Y4, Y0, Y0 // e = a - ((l*qs)&0xFFFFFF)>>dsh
	// floor(log2 l): the exponent of l as a float, less one where it
	// rounded up past l
	VCVTDQ2PS Y3, Y5
	VPSRLD $23, Y5, Y5
	MOVL $127, DX
	VMOVQ DX, X6
	VPBROADCASTD X6, Y6
	VPSUBD Y6, Y5, Y5
	VPSLLVD Y5, Y15, Y6
	VPCMPGTD Y3, Y6, Y6
	VPADDD Y6, Y5, Y5
	VPADDD Y5, Y5, Y5
	MOVL $3, DX
	VMOVQ DX, X6
	VPBROADCASTD X6, Y6
	VPADDD Y6, Y5, Y5
	VPANDN Y5, Y2, Y5
	VPADDD Y5, Y7, Y7
	VPMOVZXWD (BX), Y5
	VPADDD Y15, Y5, Y5
	VPANDN Y5, Y2, Y5 // the non-zero's inv+1
	VPMAXSD Y5, Y9, Y9
zero:
	// a level of 0's error is a (l = 0)
	VPMULDQ Y0, Y0, Y4
	VPADDQ Y4, Y8, Y8
	VPSRLQ $32, Y0, Y0
	VPMULDQ Y0, Y0, Y4
	VPADDQ Y4, Y8, Y8
	ADDQ $32, SI
	ADDQ $16, BX
	SUBQ $8, CX
	JNZ loop
	VEXTRACTI128 $1, Y8, X0
	VPADDQ X0, X8, X8
	VPSHUFD $0x4e, X8, X0
	VPADDQ X0, X8, X8
	VMOVQ X8, AX
	MOVQ AX, d+40(FP)
	VEXTRACTI128 $1, Y7, X0
	VPADDD X0, X7, X7
	VPSHUFD $0x4e, X7, X0
	VPADDD X0, X7, X7
	VPSHUFD $0xb1, X7, X0
	VPADDD X0, X7, X7
	VMOVD X7, AX
	MOVQ AX, nb+48(FP)
	VEXTRACTI128 $1, Y9, X0
	VPMAXSD X0, X9, X9
	VPSHUFD $0x4e, X9, X0
	VPMAXSD X0, X9, X9
	VPSHUFD $0xb1, X9, X0
	VPMAXSD X0, X9, X9
	VMOVD X9, AX
	MOVQ AX, eob+56(FP)
	VZEROUPPER
	RET
