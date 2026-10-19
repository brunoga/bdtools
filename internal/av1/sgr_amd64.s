//go:build amd64 && !purego

#include "textflag.h"

// The self guided filter's output rows (sgrFilter), eight samples a step;
// n is a positive multiple of 8. a and b point at the row's first A and
// B (the samples left of them are read too).

// func sgrEvenAVX2(out, u, aUp, aDn, bUp, bDn *int32, n int)
// out[j] = (av*u[j] + bv + 256) >> 9, av = 6(a[up]+a[dn]) plus 5 times
// their four diagonal neighbours (pass 0, even rows); bv likewise.
TEXT ·sgrEvenAVX2(SB), NOSPLIT, $0-56
	MOVQ out+0(FP), DI
	MOVQ u+8(FP), SI
	MOVQ aUp+16(FP), R8
	MOVQ aDn+24(FP), R9
	MOVQ bUp+32(FP), R10
	MOVQ bDn+40(FP), R11
	MOVQ n+48(FP), CX
	MOVL $256, AX
	VMOVD AX, X15
	VPBROADCASTD X15, Y15

evenLoop:
	VMOVDQU (R8), Y0
	VPADDD  (R9), Y0, Y0
	VMOVDQU -4(R8), Y1
	VPADDD  4(R8), Y1, Y1
	VPADDD  -4(R9), Y1, Y1
	VPADDD  4(R9), Y1, Y1
	VPSLLD  $1, Y0, Y2
	VPSLLD  $2, Y0, Y0
	VPADDD  Y2, Y0, Y0
	VPSLLD  $2, Y1, Y2
	VPADDD  Y2, Y1, Y1
	VPADDD  Y1, Y0, Y0
	VMOVDQU (R10), Y3
	VPADDD  (R11), Y3, Y3
	VMOVDQU -4(R10), Y4
	VPADDD  4(R10), Y4, Y4
	VPADDD  -4(R11), Y4, Y4
	VPADDD  4(R11), Y4, Y4
	VPSLLD  $1, Y3, Y2
	VPSLLD  $2, Y3, Y3
	VPADDD  Y2, Y3, Y3
	VPSLLD  $2, Y4, Y2
	VPADDD  Y2, Y4, Y4
	VPADDD  Y4, Y3, Y3
	VPMULLD (SI), Y0, Y0
	VPADDD  Y3, Y0, Y0
	VPADDD  Y15, Y0, Y0
	VPSRAD  $9, Y0, Y0
	VMOVDQU Y0, (DI)
	ADDQ    $32, DI
	ADDQ    $32, SI
	ADDQ    $32, R8
	ADDQ    $32, R9
	ADDQ    $32, R10
	ADDQ    $32, R11
	SUBQ    $8, CX
	JG      evenLoop
	VZEROUPPER
	RET

// func sgrOddAVX2(out, u, a, b *int32, n int)
// out[j] = (av*u[j] + bv + 128) >> 8, av = 6a[c] + 5(a[c-1]+a[c+1])
// (pass 0, odd rows); bv likewise.
TEXT ·sgrOddAVX2(SB), NOSPLIT, $0-40
	MOVQ out+0(FP), DI
	MOVQ u+8(FP), SI
	MOVQ a+16(FP), R8
	MOVQ b+24(FP), R10
	MOVQ n+32(FP), CX
	MOVL $128, AX
	VMOVD AX, X15
	VPBROADCASTD X15, Y15

oddLoop:
	VMOVDQU (R8), Y0
	VMOVDQU -4(R8), Y1
	VPADDD  4(R8), Y1, Y1
	VPSLLD  $1, Y0, Y2
	VPSLLD  $2, Y0, Y0
	VPADDD  Y2, Y0, Y0
	VPSLLD  $2, Y1, Y2
	VPADDD  Y2, Y1, Y1
	VPADDD  Y1, Y0, Y0
	VMOVDQU (R10), Y3
	VMOVDQU -4(R10), Y4
	VPADDD  4(R10), Y4, Y4
	VPSLLD  $1, Y3, Y2
	VPSLLD  $2, Y3, Y3
	VPADDD  Y2, Y3, Y3
	VPSLLD  $2, Y4, Y2
	VPADDD  Y2, Y4, Y4
	VPADDD  Y4, Y3, Y3
	VPMULLD (SI), Y0, Y0
	VPADDD  Y3, Y0, Y0
	VPADDD  Y15, Y0, Y0
	VPSRAD  $8, Y0, Y0
	VMOVDQU Y0, (DI)
	ADDQ    $32, DI
	ADDQ    $32, SI
	ADDQ    $32, R8
	ADDQ    $32, R10
	SUBQ    $8, CX
	JG      oddLoop
	VZEROUPPER
	RET

// func sgrCrossAVX2(out, u, a, b *int32, bw, n int)
// out[j] = (av*u[j] + bv + 256) >> 9, av = 4 times a[c] and its four
// neighbours plus 3 times its four diagonal ones (pass 1), the rows bw
// apart; bv likewise.
TEXT ·sgrCrossAVX2(SB), NOSPLIT, $0-48
	MOVQ out+0(FP), DI
	MOVQ u+8(FP), SI
	MOVQ a+16(FP), R8
	MOVQ b+24(FP), R10
	MOVQ bw+32(FP), R12
	MOVQ n+40(FP), CX
	SHLQ $2, R12
	MOVL $256, AX
	VMOVD AX, X15
	VPBROADCASTD X15, Y15

crossLoop:
	MOVQ    R8, R9
	SUBQ    R12, R9 // the row above
	MOVQ    R8, R11
	ADDQ    R12, R11 // the row below
	VMOVDQU (R8), Y0
	VPADDD  -4(R8), Y0, Y0
	VPADDD  4(R8), Y0, Y0
	VPADDD  (R9), Y0, Y0
	VPADDD  (R11), Y0, Y0
	VMOVDQU -4(R9), Y1
	VPADDD  4(R9), Y1, Y1
	VPADDD  -4(R11), Y1, Y1
	VPADDD  4(R11), Y1, Y1
	VPSLLD  $2, Y0, Y0
	VPSLLD  $1, Y1, Y2
	VPADDD  Y2, Y1, Y1
	VPADDD  Y1, Y0, Y0
	MOVQ    R10, R9
	SUBQ    R12, R9
	MOVQ    R10, R11
	ADDQ    R12, R11
	VMOVDQU (R10), Y3
	VPADDD  -4(R10), Y3, Y3
	VPADDD  4(R10), Y3, Y3
	VPADDD  (R9), Y3, Y3
	VPADDD  (R11), Y3, Y3
	VMOVDQU -4(R9), Y4
	VPADDD  4(R9), Y4, Y4
	VPADDD  -4(R11), Y4, Y4
	VPADDD  4(R11), Y4, Y4
	VPSLLD  $2, Y3, Y3
	VPSLLD  $1, Y4, Y2
	VPADDD  Y2, Y4, Y4
	VPADDD  Y4, Y3, Y3
	VPMULLD (SI), Y0, Y0
	VPADDD  Y3, Y0, Y0
	VPADDD  Y15, Y0, Y0
	VPSRAD  $9, Y0, Y0
	VMOVDQU Y0, (DI)
	ADDQ    $32, DI
	ADDQ    $32, SI
	ADDQ    $32, R8
	ADDQ    $32, R10
	SUBQ    $8, CX
	JG      crossLoop
	VZEROUPPER
	RET

// func sgrABAVX2(sq, sum, a, b *int32, n, nn, s, oneOverN, sh, sh2, r1, r2 int)
// A and B (sgrAB) of n positions, n a positive multiple of 8: p = max(0,
// aa*nn - d*d) from the sums rounded by sh (r1, r2 their rounding),
// z = (p*s + 2^19) >> 20, a = sgrA2's (computed, 256 from z = 255), b = ((256-a)*sum*oneOverN +
// 2^11) >> 12; the products of 64 bits are of unsigned 32 bit values. The
// shifts read their counts (sh, sh2 = 2 sh) from the arguments.
TEXT ·sgrABAVX2(SB), NOSPLIT, $0-96
	MOVQ sq+0(FP), SI
	MOVQ sum+8(FP), DX
	MOVQ a+16(FP), DI
	MOVQ b+24(FP), R8
	MOVQ n+32(FP), CX
	MOVQ nn+40(FP), AX
	VMOVD AX, X8
	VPBROADCASTD X8, Y8
	MOVQ s+48(FP), AX
	VMOVD AX, X9
	VPBROADCASTD X9, Y9
	MOVQ oneOverN+56(FP), AX
	VMOVD AX, X10
	VPBROADCASTD X10, Y10
	MOVQ r1+80(FP), AX
	VMOVD AX, X0
	VPBROADCASTD X0, Y12
	MOVQ r2+88(FP), AX
	VMOVD AX, X0
	VPBROADCASTD X0, Y11
	MOVQ $0x80000, AX
	VMOVQ AX, X0
	VPBROADCASTQ X0, Y13
	MOVQ $0x800, AX
	VMOVQ AX, X0
	VPBROADCASTQ X0, Y14
	MOVL $256, AX
	VMOVD AX, X0
	VPBROADCASTD X0, Y15
	MOVL $255, AX
	VMOVD AX, X0
	VPBROADCASTD X0, Y7

abLoop:
	VMOVDQU  (SI), Y0
	VMOVDQU  (DX), Y1
	VPADDD   Y11, Y0, Y0
	VPSRLD   sh2+72(FP), Y0, Y0 // aa
	VPADDD   Y12, Y1, Y2
	VPSRLD   sh+64(FP), Y2, Y2 // d
	VPMULLD  Y8, Y0, Y0
	VPMULLD  Y2, Y2, Y2
	VPSUBD   Y2, Y0, Y0
	VPXOR    Y3, Y3, Y3
	VPMAXSD  Y3, Y0, Y0 // p
	VPMULUDQ Y9, Y0, Y4
	VPSRLQ   $32, Y0, Y5
	VPMULUDQ Y9, Y5, Y5
	VPADDQ   Y13, Y4, Y4
	VPSRLQ   $20, Y4, Y4
	VPADDQ   Y13, Y5, Y5
	VPSRLQ   $20, Y5, Y5
	VPSLLQ   $32, Y5, Y5
	VPBLENDD $0xaa, Y5, Y4, Y4 // z
	VPMINUD    Y7, Y4, Y4 // z, at most 255
	VPCMPEQD   Y2, Y2, Y2
	VPSRLD     $31, Y2, Y2 // ones
	VPADDD     Y2, Y4, Y6
	VPSLLD     $8, Y4, Y5
	VPSRLD     $1, Y4, Y3
	VPADDD     Y3, Y5, Y5
	VCVTDQ2PS  Y5, Y5
	VCVTDQ2PS  Y6, Y6
	VDIVPS     Y6, Y5, Y5
	VCVTTPS2DQ Y5, Y3 // (256z + z/2) / (z+1), exact in single precision
	VPXOR      Y6, Y6, Y6
	VPCMPEQD   Y6, Y4, Y6
	VPAND      Y2, Y6, Y6
	VPADDD     Y6, Y3, Y3 // 1 for z = 0
	VPCMPEQD   Y7, Y4, Y6
	VPAND      Y2, Y6, Y6
	VPADDD     Y6, Y3, Y3 // 256 for z = 255 (and beyond)
	VMOVDQU  Y3, (DI)
	VPSUBD   Y3, Y15, Y3
	VPMULLD  Y1, Y3, Y3
	VPMULUDQ Y10, Y3, Y4
	VPSRLQ   $32, Y3, Y5
	VPMULUDQ Y10, Y5, Y5
	VPADDQ   Y14, Y4, Y4
	VPSRLQ   $12, Y4, Y4
	VPADDQ   Y14, Y5, Y5
	VPSRLQ   $12, Y5, Y5
	VPSLLQ   $32, Y5, Y5
	VPBLENDD $0xaa, Y5, Y4, Y4 // b
	VMOVDQU  Y4, (R8)
	ADDQ     $32, SI
	ADDQ     $32, DX
	ADDQ     $32, DI
	ADDQ     $32, R8
	SUBQ     $8, CX
	JG       abLoop
	VZEROUPPER
	RET
