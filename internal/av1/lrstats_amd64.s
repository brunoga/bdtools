//go:build amd64 && !purego

#include "textflag.h"

// func gramAVX2(y *int16, n, k int, gm *int64)
// gm[u*17+v] += sum over j < n of y[u*n+j]*y[v*n+j], for u <= v < k; n is
// a multiple of 16 and the sums of a lane fit 32 bits.
TEXT ·gramAVX2(SB), NOSPLIT, $0-32
	MOVQ y+0(FP), SI
	MOVQ n+8(FP), DX
	MOVQ k+16(FP), CX
	MOVQ gm+24(FP), DI
	MOVQ DX, R8
	SHLQ $1, R8 // the bytes of a row
	XORQ R9, R9 // u

uLoop:
	MOVQ R9, R10 // v
	MOVQ R9, AX
	IMULQ R8, AX
	LEAQ (SI)(AX*1), R11 // row u
	MOVQ R11, R12        // row v

vLoop:
	VPXOR Y0, Y0, Y0
	XORQ  R13, R13

jLoop:
	VMOVDQU  (R11)(R13*1), Y1
	VPMADDWD (R12)(R13*1), Y1, Y1
	VPADDD   Y1, Y0, Y0
	ADDQ     $32, R13
	CMPQ     R13, R8
	JLT      jLoop

	VEXTRACTI128 $1, Y0, X1
	VPADDD       X1, X0, X0
	VPSHUFD      $0x4e, X0, X1
	VPADDD       X1, X0, X0
	VPSHUFD      $0xb1, X0, X1
	VPADDD       X1, X0, X0
	VMOVD        X0, AX
	MOVLQSX      AX, AX
	MOVQ         R9, BX
	IMULQ        $17, BX
	ADDQ         R10, BX
	ADDQ         AX, (DI)(BX*8)

	ADDQ R8, R12
	INCQ R10
	CMPQ R10, CX
	JLT  vLoop

	INCQ R9
	CMPQ R9, CX
	JLT  uLoop
	VZEROUPPER
	RET

// func dot32AVX2(a, b *int32, n int) int64
// The sum of a[i]*b[i] (as 64 bits) for i < n, n a positive multiple of 8.
TEXT ·dot32AVX2(SB), NOSPLIT, $0-32
	MOVQ a+0(FP), SI
	MOVQ b+8(FP), DI
	MOVQ n+16(FP), CX
	VPXOR Y0, Y0, Y0
	VPXOR Y1, Y1, Y1

dotLoop:
	VMOVDQU (SI), Y2
	VMOVDQU (DI), Y3
	VPMULDQ Y3, Y2, Y4 // the even lanes
	VPADDQ  Y4, Y0, Y0
	VPSRLQ  $32, Y2, Y2
	VPSRLQ  $32, Y3, Y3
	VPMULDQ Y3, Y2, Y4 // the odd ones
	VPADDQ  Y4, Y1, Y1
	ADDQ    $32, SI
	ADDQ    $32, DI
	SUBQ    $8, CX
	JG      dotLoop
	VPADDQ       Y1, Y0, Y0
	VEXTRACTI128 $1, Y0, X1
	VPADDQ       X1, X0, X0
	VPSHUFD      $0x4e, X0, X1
	VPADDQ       X1, X0, X0
	VMOVQ        X0, AX
	MOVQ         AX, ret+24(FP)
	VZEROUPPER
	RET
