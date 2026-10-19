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
	MOVD AX, X15
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
	MOVD AX, X15
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
	MOVD AX, X15
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
