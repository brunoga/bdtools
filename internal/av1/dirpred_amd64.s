//go:build amd64 && !purego

#include "textflag.h"

// func dirRowAVX2(out *uint16, a *int32, n int, sh int32)
// out[j] = (a[j]*(32-sh) + a[j+1]*sh + 16) >> 5 for j < n, n a multiple
// of 8 (a has n+1 samples).
TEXT ·dirRowAVX2(SB), NOSPLIT, $0-28
	MOVQ out+0(FP), DI
	MOVQ a+8(FP), SI
	MOVQ n+16(FP), CX
	MOVL sh+24(FP), AX
	VMOVD AX, X15
	VPBROADCASTD X15, Y15 // sh
	MOVL $32, DX
	SUBL AX, DX
	VMOVD DX, X14
	VPBROADCASTD X14, Y14 // 32-sh
	MOVL $16, DX
	VMOVD DX, X13
	VPBROADCASTD X13, Y13 // 16
row:
	VMOVDQU (SI), Y0
	VMOVDQU 4(SI), Y1
	VPMULLD Y14, Y0, Y0
	VPMULLD Y15, Y1, Y1
	VPADDD Y1, Y0, Y0
	VPADDD Y13, Y0, Y0
	VPSRAD $5, Y0, Y0
	VPACKUSDW Y0, Y0, Y0
	VPERMQ $0x08, Y0, Y0
	VMOVDQU X0, (DI)
	ADDQ $32, SI
	ADDQ $16, DI
	SUBQ $8, CX
	JNZ row
	VZEROUPPER
	RET

// func dirGatherAVX2(out *uint16, edge *int32, base, sh *int32, il int32, n int)
// out[j] = (edge[il+base[j]]*(32-sh[j]) + edge[il+base[j]+1]*sh[j] + 16)
// >> 5 for j < n, n a multiple of 8.
TEXT ·dirGatherAVX2(SB), NOSPLIT, $0-48
	MOVQ out+0(FP), DI
	MOVQ edge+8(FP), SI
	MOVQ base+16(FP), BX
	MOVQ sh+24(FP), DX
	MOVL il+32(FP), AX
	MOVQ n+40(FP), CX
	VMOVD AX, X12
	VPBROADCASTD X12, Y12 // il
	MOVL $32, AX
	VMOVD AX, X11
	VPBROADCASTD X11, Y11 // 32
	MOVL $16, AX
	VMOVD AX, X13
	VPBROADCASTD X13, Y13 // 16
	VPCMPEQD Y10, Y10, Y10 // all ones
gather:
	VMOVDQU (BX), Y2
	VPADDD Y12, Y2, Y2 // the positions
	VMOVDQA Y10, Y4
	VPXOR Y0, Y0, Y0
	VPGATHERDD Y4, (SI)(Y2*4), Y0
	VMOVDQA Y10, Y4
	VPXOR Y1, Y1, Y1
	VPGATHERDD Y4, 4(SI)(Y2*4), Y1
	VMOVDQU (DX), Y3 // sh
	VPSUBD Y3, Y11, Y5 // 32-sh
	VPMULLD Y5, Y0, Y0
	VPMULLD Y3, Y1, Y1
	VPADDD Y1, Y0, Y0
	VPADDD Y13, Y0, Y0
	VPSRAD $5, Y0, Y0
	VPACKUSDW Y0, Y0, Y0
	VPERMQ $0x08, Y0, Y0
	VMOVDQU X0, (DI)
	ADDQ $32, BX
	ADDQ $32, DX
	ADDQ $16, DI
	SUBQ $8, CX
	JNZ gather
	VZEROUPPER
	RET
