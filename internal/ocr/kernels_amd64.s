//go:build amd64 && !purego

#include "textflag.h"

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

// func xgetbvAsm() (eax, edx uint32)
TEXT ·xgetbvAsm(SB), NOSPLIT, $0-8
	XORL CX, CX
	XGETBV
	MOVL AX, eax+0(FP)
	MOVL DX, edx+4(FP)
	RET

// func axpyAVX2(y, x *float32, n int, a float32)
// y[i] += a*x[i] for n (a multiple of 16) floats.
TEXT ·axpyAVX2(SB), NOSPLIT, $0-28
	MOVQ y+0(FP), DI
	MOVQ x+8(FP), SI
	MOVQ n+16(FP), CX
	VBROADCASTSS a+24(FP), Y0
axloop:
	VMOVUPS (SI), Y1
	VMOVUPS 32(SI), Y2
	VFMADD213PS (DI), Y0, Y1
	VFMADD213PS 32(DI), Y0, Y2
	VMOVUPS Y1, (DI)
	VMOVUPS Y2, 32(DI)
	ADDQ $64, SI
	ADDQ $64, DI
	SUBQ $16, CX
	JNZ axloop
	VZEROUPPER
	RET

// func dotAVX2(a, b *float32, n int) float32
// The sum of a[i]*b[i] over n (a multiple of 16) floats.
TEXT ·dotAVX2(SB), NOSPLIT, $0-28
	MOVQ a+0(FP), SI
	MOVQ b+8(FP), DI
	MOVQ n+16(FP), CX
	VXORPS Y0, Y0, Y0
	VXORPS Y1, Y1, Y1
dloop:
	VMOVUPS (SI), Y2
	VMOVUPS 32(SI), Y3
	VFMADD231PS (DI), Y2, Y0
	VFMADD231PS 32(DI), Y3, Y1
	ADDQ $64, SI
	ADDQ $64, DI
	SUBQ $16, CX
	JNZ dloop
	VADDPS Y1, Y0, Y0
	VEXTRACTF128 $1, Y0, X1
	VADDPS X1, X0, X0
	VHADDPS X0, X0, X0
	VHADDPS X0, X0, X0
	MOVSS X0, ret+24(FP)
	VZEROUPPER
	RET
