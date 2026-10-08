//go:build amd64 && !purego

#include "textflag.h"

// Deblocking filters (AVX2), 16 edge positions per call in int16 lanes.
// Masks come from VPCMPGTW (all ones) and select with VPBLENDVB.

DATA dbC4<>+0(SB)/4, $0x00040004
GLOBL dbC4<>(SB), RODATA, $4
DATA dbC2<>+0(SB)/4, $0x00020002
GLOBL dbC2<>(SB), RODATA, $4
DATA dbC1<>+0(SB)/4, $0x00010001
GLOBL dbC1<>(SB), RODATA, $4

// TRANSPOSE8 as in idct_amd64.s (per 128-bit lane).
#define TRANSPOSE8(r0, r1, r2, r3, r4, r5, r6, r7, o0, o1, o2, o3, o4, o5, o6, o7) \
	VPUNPCKLWD r1, r0, o0; \
	VPUNPCKHWD r1, r0, o1; \
	VPUNPCKLWD r3, r2, o2; \
	VPUNPCKHWD r3, r2, o3; \
	VPUNPCKLWD r5, r4, o4; \
	VPUNPCKHWD r5, r4, o5; \
	VPUNPCKLWD r7, r6, o6; \
	VPUNPCKHWD r7, r6, o7; \
	VPUNPCKLDQ o2, o0, r0; \
	VPUNPCKHDQ o2, o0, r1; \
	VPUNPCKLDQ o3, o1, r2; \
	VPUNPCKHDQ o3, o1, r3; \
	VPUNPCKLDQ o6, o4, r4; \
	VPUNPCKHDQ o6, o4, r5; \
	VPUNPCKLDQ o7, o5, r6; \
	VPUNPCKHDQ o7, o5, r7; \
	VPUNPCKLQDQ r4, r0, o0; \
	VPUNPCKHQDQ r4, r0, o1; \
	VPUNPCKLQDQ r5, r1, o2; \
	VPUNPCKHQDQ r5, r1, o3; \
	VPUNPCKLQDQ r6, r2, o4; \
	VPUNPCKHQDQ r6, r2, o5; \
	VPUNPCKLQDQ r7, r3, o6; \
	VPUNPCKHQDQ r7, r3, o7

// LD8X2 loads 8 bytes from a and b into the two lanes of y (as words).
#define LD8X2(a, b, x, y) \
	VMOVQ a, x; \
	VPINSRQ $1, b, x, x; \
	VPMOVZXBW x, y

// ST8X2 stores the two lanes of y (words) as 8 bytes to a and b.
#define ST8X2(y, x, t, a, b) \
	VEXTRACTI128 $1, y, t; \
	VPACKUSWB t, x, x; \
	VMOVQ x, a; \
	VPEXTRQ $1, x, b

// ABSDIFF computes |a-b| into o.
#define ABSDIFF(a, b, o) \
	VPSUBW b, a, o; \
	VPABSW o, o

// func filterLumaAsm(p *byte, step, along int, bs, tc0 *int16, alpha, beta int)
// p addresses q0 of the first position; step is the offset from p0 to q0
// and along the offset between positions. bs and tc0 hold 16 lanes.
TEXT ·filterLumaAsm(SB), $256-56
	MOVQ p+0(FP), DI
	MOVQ step+8(FP), R8
	MOVQ along+16(FP), R9
	CMPQ R8, $1
	JNE lh
	// vertical edge: 16 rows of 8 bytes (p3..q3), transposed
	LEAQ -4(DI), SI
	LEAQ (SI)(R9*8), DX       // row 8
	LD8X2((SI), (DX), X0, Y0)
	LD8X2((SI)(R9*1), (DX)(R9*1), X1, Y1)
	LD8X2((SI)(R9*2), (DX)(R9*2), X2, Y2)
	LEAQ (R9)(R9*2), R10
	LD8X2((SI)(R10*1), (DX)(R10*1), X3, Y3)
	LEAQ (SI)(R9*4), R11
	LEAQ (DX)(R9*4), R12
	LD8X2((R11), (R12), X4, Y4)
	LD8X2((R11)(R9*1), (R12)(R9*1), X5, Y5)
	LD8X2((R11)(R9*2), (R12)(R9*2), X6, Y6)
	LD8X2((R11)(R10*1), (R12)(R10*1), X7, Y7)
	TRANSPOSE8(Y0, Y1, Y2, Y3, Y4, Y5, Y6, Y7, Y8, Y9, Y10, Y11, Y12, Y13, Y14, Y15)
	VMOVDQA Y8, Y0
	VMOVDQA Y9, Y1
	VMOVDQA Y10, Y2
	VMOVDQA Y11, Y3
	VMOVDQA Y12, Y4
	VMOVDQA Y13, Y5
	VMOVDQA Y14, Y6
	VMOVDQA Y15, Y7
	JMP lf
lh:
	// horizontal edge: rows p3..q3 of 16 pixels
	MOVQ DI, SI
	SUBQ R8, SI
	SUBQ R8, SI
	SUBQ R8, SI
	SUBQ R8, SI               // p3
	LEAQ (R8)(R8*2), R10
	VPMOVZXBW (SI), Y0
	VPMOVZXBW (SI)(R8*1), Y1
	VPMOVZXBW (SI)(R8*2), Y2
	VPMOVZXBW (SI)(R10*1), Y3
	VPMOVZXBW (DI), Y4
	VPMOVZXBW (DI)(R8*1), Y5
	VPMOVZXBW (DI)(R8*2), Y6
	VPMOVZXBW (DI)(R10*1), Y7
lf:
	// Y0..Y7 = p3 p2 p1 p0 q0 q1 q2 q3
	ABSDIFF(Y3, Y4, Y8)                  // ad = |p0-q0|
	MOVQ alpha+40(FP), AX
	VMOVQ AX, X9
	VPBROADCASTW X9, Y9
	VPCMPGTW Y8, Y9, Y9                  // alpha > ad
	MOVQ beta+48(FP), AX
	VMOVQ AX, X10
	VPBROADCASTW X10, Y10
	ABSDIFF(Y2, Y3, Y11)
	VPCMPGTW Y11, Y10, Y11
	VPAND Y11, Y9, Y9
	ABSDIFF(Y5, Y4, Y11)
	VPCMPGTW Y11, Y10, Y11
	VPAND Y11, Y9, Y9
	MOVQ bs+24(FP), AX
	VMOVDQU (AX), Y11
	VPXOR Y12, Y12, Y12
	VPCMPGTW Y12, Y11, Y11               // bs > 0
	VPAND Y11, Y9, Y9                    // Y9 = m
	VPTEST Y9, Y9
	JZ ldone
	VMOVDQU Y9, m-64(SP)
	VMOVDQU Y1, p2n-128(SP)              // p2/q2 results default to unchanged
	VMOVDQU Y6, q2n-96(SP)
	ABSDIFF(Y1, Y3, Y11)
	VPCMPGTW Y11, Y10, Y11               // Y11 = mp
	ABSDIFF(Y6, Y4, Y12)
	VPCMPGTW Y12, Y10, Y12               // Y12 = mq
	MOVQ tc0+32(FP), BX
	VMOVDQU (BX), Y13
	VPSUBW Y11, Y13, Y13
	VPSUBW Y12, Y13, Y13                 // tc
	VPSUBW Y3, Y4, Y14
	VPSLLW $2, Y14, Y14
	VPSUBW Y5, Y2, Y15
	VPADDW Y15, Y14, Y14
	VPBROADCASTD dbC4<>(SB), Y15
	VPADDW Y15, Y14, Y14
	VPSRAW $3, Y14, Y14                  // delta
	VPXOR Y15, Y15, Y15
	VPSUBW Y13, Y15, Y15                 // -tc
	VPMAXSW Y15, Y14, Y14
	VPMINSW Y13, Y14, Y14
	VPADDW Y14, Y3, Y15
	VMOVDQU Y15, np0-256(SP)
	VPSUBW Y14, Y4, Y15
	VMOVDQU Y15, nq0-224(SP)
	VPADDW Y4, Y3, Y14
	VPBROADCASTD dbC1<>(SB), Y15
	VPADDW Y15, Y14, Y14
	VPSRAW $1, Y14, Y14                  // avg
	VPXOR Y13, Y13, Y13
	VPSUBW (BX), Y13, Y13                // -tc0
	VPADDW Y14, Y1, Y15
	VPSLLW $1, Y2, Y10
	VPSUBW Y10, Y15, Y15
	VPSRAW $1, Y15, Y15
	VPMAXSW Y13, Y15, Y15
	VPMINSW (BX), Y15, Y15
	VPADDW Y2, Y15, Y15
	VPBLENDVB Y11, Y15, Y2, Y15          // mp ? np1 : p1
	VMOVDQU Y15, np1-192(SP)
	VPADDW Y14, Y6, Y15
	VPSLLW $1, Y5, Y10
	VPSUBW Y10, Y15, Y15
	VPSRAW $1, Y15, Y15
	VPMAXSW Y13, Y15, Y15
	VPMINSW (BX), Y15, Y15
	VPADDW Y5, Y15, Y15
	VPBLENDVB Y12, Y15, Y5, Y15          // mq ? nq1 : q1
	VMOVDQU Y15, nq1-160(SP)
	// bS == 4
	VMOVDQU (AX), Y10
	VPBROADCASTD dbC4<>(SB), Y15
	VPCMPEQW Y15, Y10, Y10               // is4
	VPTEST Y10, Y10
	JZ lfinal
	MOVQ alpha+40(FP), AX
	SARQ $2, AX
	ADDQ $2, AX
	VMOVQ AX, X15
	VPBROADCASTW X15, Y15
	VPCMPGTW Y8, Y15, Y15                // strong
	VPAND Y15, Y11, Y13                  // sp
	VPAND Y15, Y12, Y14                  // sq
	VPADDW Y3, Y2, Y15
	VPADDW Y4, Y15, Y15                  // sum = p1+p0+q0
	VPBROADCASTD dbC4<>(SB), Y11
	VPBROADCASTD dbC2<>(SB), Y12
	// p0
	VPSLLW $1, Y15, Y8
	VPADDW Y1, Y8, Y8
	VPADDW Y5, Y8, Y8
	VPADDW Y11, Y8, Y8
	VPSRAW $3, Y8, Y8                    // sp0
	VPSLLW $1, Y2, Y9
	VPADDW Y3, Y9, Y9
	VPADDW Y5, Y9, Y9
	VPADDW Y12, Y9, Y9
	VPSRAW $2, Y9, Y9                    // wp0
	VPBLENDVB Y13, Y8, Y9, Y8
	VMOVDQU np0-256(SP), Y9
	VPBLENDVB Y10, Y8, Y9, Y8
	VMOVDQU Y8, np0-256(SP)
	// p1
	VPADDW Y15, Y1, Y8
	VPADDW Y12, Y8, Y8
	VPSRAW $2, Y8, Y8                    // sp1
	VPBLENDVB Y13, Y8, Y2, Y8
	VMOVDQU np1-192(SP), Y9
	VPBLENDVB Y10, Y8, Y9, Y8
	VMOVDQU Y8, np1-192(SP)
	// p2
	VPSLLW $1, Y0, Y8
	VPADDW Y1, Y8, Y8
	VPSLLW $1, Y1, Y9
	VPADDW Y9, Y8, Y8
	VPADDW Y15, Y8, Y8
	VPADDW Y11, Y8, Y8
	VPSRAW $3, Y8, Y8                    // sp2
	VPBLENDVB Y13, Y8, Y1, Y8
	VPBLENDVB Y10, Y8, Y1, Y8
	VMOVDQU Y8, p2n-128(SP)
	// q side: sumq = q1+q0+p0
	VPADDW Y4, Y5, Y15
	VPADDW Y3, Y15, Y15
	VPSLLW $1, Y15, Y8
	VPADDW Y6, Y8, Y8
	VPADDW Y2, Y8, Y8
	VPADDW Y11, Y8, Y8
	VPSRAW $3, Y8, Y8                    // sq0
	VPSLLW $1, Y5, Y9
	VPADDW Y4, Y9, Y9
	VPADDW Y2, Y9, Y9
	VPADDW Y12, Y9, Y9
	VPSRAW $2, Y9, Y9                    // wq0
	VPBLENDVB Y14, Y8, Y9, Y8
	VMOVDQU nq0-224(SP), Y9
	VPBLENDVB Y10, Y8, Y9, Y8
	VMOVDQU Y8, nq0-224(SP)
	VPADDW Y15, Y6, Y8
	VPADDW Y12, Y8, Y8
	VPSRAW $2, Y8, Y8                    // sq1
	VPBLENDVB Y14, Y8, Y5, Y8
	VMOVDQU nq1-160(SP), Y9
	VPBLENDVB Y10, Y8, Y9, Y8
	VMOVDQU Y8, nq1-160(SP)
	VPSLLW $1, Y7, Y8
	VPADDW Y6, Y8, Y8
	VPSLLW $1, Y6, Y9
	VPADDW Y9, Y8, Y8
	VPADDW Y15, Y8, Y8
	VPADDW Y11, Y8, Y8
	VPSRAW $3, Y8, Y8                    // sq2
	VPBLENDVB Y14, Y8, Y6, Y8
	VPBLENDVB Y10, Y8, Y6, Y8
	VMOVDQU Y8, q2n-96(SP)
lfinal:
	VMOVDQU m-64(SP), Y9
	VPBLENDVB Y9, p2n-128(SP), Y1, Y1
	VPBLENDVB Y9, np1-192(SP), Y2, Y2
	VPBLENDVB Y9, np0-256(SP), Y3, Y3
	VPBLENDVB Y9, nq0-224(SP), Y4, Y4
	VPBLENDVB Y9, nq1-160(SP), Y5, Y5
	VPBLENDVB Y9, q2n-96(SP), Y6, Y6
	CMPQ R8, $1
	JNE lsth
	TRANSPOSE8(Y0, Y1, Y2, Y3, Y4, Y5, Y6, Y7, Y8, Y9, Y10, Y11, Y12, Y13, Y14, Y15)
	LEAQ -4(DI), SI
	LEAQ (SI)(R9*8), DX
	LEAQ (R9)(R9*2), R10
	ST8X2(Y8, X8, X0, (SI), (DX))
	ST8X2(Y9, X9, X0, (SI)(R9*1), (DX)(R9*1))
	ST8X2(Y10, X10, X0, (SI)(R9*2), (DX)(R9*2))
	ST8X2(Y11, X11, X0, (SI)(R10*1), (DX)(R10*1))
	LEAQ (SI)(R9*4), R11
	LEAQ (DX)(R9*4), R12
	ST8X2(Y12, X12, X0, (R11), (R12))
	ST8X2(Y13, X13, X0, (R11)(R9*1), (R12)(R9*1))
	ST8X2(Y14, X14, X0, (R11)(R9*2), (R12)(R9*2))
	ST8X2(Y15, X15, X0, (R11)(R10*1), (R12)(R10*1))
	VZEROUPPER
	RET
lsth:
#define ST16(y, x, t, m) \
	VEXTRACTI128 $1, y, t; \
	VPACKUSWB t, x, x; \
	VMOVDQU x, m
	ST16(Y1, X1, X8, (SI)(R8*1))
	ST16(Y2, X2, X8, (SI)(R8*2))
	ST16(Y3, X3, X8, (SI)(R10*1))
	ST16(Y4, X4, X8, (DI))
	ST16(Y5, X5, X8, (DI)(R8*1))
	ST16(Y6, X6, X8, (DI)(R8*2))
ldone:
	VZEROUPPER
	RET

// func filterChroma2Asm(cb, cr *byte, step, along int, bs, tc0, alpha, beta *int16)
// Both chroma planes: lanes 0-7 are Cb positions 0-7, lanes 8-15 Cr.
// bs, tc0, alpha and beta hold 16 lanes.
TEXT ·filterChroma2Asm(SB), NOSPLIT, $0-64
	MOVQ cb+0(FP), DI
	MOVQ cr+8(FP), DX
	MOVQ step+16(FP), R8
	MOVQ along+24(FP), R9
	CMPQ R8, $1
	JNE ch
	// vertical edge: 8 rows of 8 bytes from each plane, transposed
	LEAQ -4(DI), SI
	LEAQ -4(DX), R11
	LEAQ (R9)(R9*2), R10
	LD8X2((SI), (R11), X0, Y0)
	LD8X2((SI)(R9*1), (R11)(R9*1), X1, Y1)
	LD8X2((SI)(R9*2), (R11)(R9*2), X2, Y2)
	LD8X2((SI)(R10*1), (R11)(R10*1), X3, Y3)
	LEAQ (SI)(R9*4), R12
	LEAQ (R11)(R9*4), R13
	LD8X2((R12), (R13), X4, Y4)
	LD8X2((R12)(R9*1), (R13)(R9*1), X5, Y5)
	LD8X2((R12)(R9*2), (R13)(R9*2), X6, Y6)
	LD8X2((R12)(R10*1), (R13)(R10*1), X7, Y7)
	TRANSPOSE8(Y0, Y1, Y2, Y3, Y4, Y5, Y6, Y7, Y8, Y9, Y10, Y11, Y12, Y13, Y14, Y15)
	VMOVDQA Y8, Y0
	VMOVDQA Y9, Y1
	VMOVDQA Y10, Y2
	VMOVDQA Y11, Y3
	VMOVDQA Y12, Y4
	VMOVDQA Y13, Y5
	VMOVDQA Y14, Y6
	VMOVDQA Y15, Y7
	JMP cf
ch:
	MOVQ DI, SI
	SUBQ R8, SI
	SUBQ R8, SI               // p1 of Cb
	MOVQ DX, R11
	SUBQ R8, R11
	SUBQ R8, R11
	LD8X2((SI), (R11), X2, Y2)
	LD8X2((SI)(R8*1), (R11)(R8*1), X3, Y3)
	LD8X2((DI), (DX), X4, Y4)
	LD8X2((DI)(R8*1), (DX)(R8*1), X5, Y5)
cf:
	// Y2..Y5 = p1 p0 q0 q1
	ABSDIFF(Y3, Y4, Y8)
	MOVQ alpha+48(FP), AX
	VMOVDQU (AX), Y9
	VPCMPGTW Y8, Y9, Y9
	MOVQ beta+56(FP), AX
	VMOVDQU (AX), Y10
	ABSDIFF(Y2, Y3, Y11)
	VPCMPGTW Y11, Y10, Y11
	VPAND Y11, Y9, Y9
	ABSDIFF(Y5, Y4, Y11)
	VPCMPGTW Y11, Y10, Y11
	VPAND Y11, Y9, Y9
	MOVQ bs+32(FP), AX
	VMOVDQU (AX), Y11
	VPXOR Y12, Y12, Y12
	VPCMPGTW Y12, Y11, Y12
	VPAND Y12, Y9, Y9                    // m
	VPTEST Y9, Y9
	JZ cdone
	VPBROADCASTD dbC4<>(SB), Y15
	VPCMPEQW Y15, Y11, Y11               // is4
	MOVQ tc0+40(FP), AX
	VMOVDQU (AX), Y13
	VPBROADCASTD dbC1<>(SB), Y12
	VPADDW Y12, Y13, Y13                 // tc
	VPSUBW Y3, Y4, Y14
	VPSLLW $2, Y14, Y14
	VPSUBW Y5, Y2, Y12
	VPADDW Y12, Y14, Y14
	VPADDW Y15, Y14, Y14
	VPSRAW $3, Y14, Y14
	VPXOR Y12, Y12, Y12
	VPSUBW Y13, Y12, Y12
	VPMAXSW Y12, Y14, Y14
	VPMINSW Y13, Y14, Y14                // delta
	VPADDW Y14, Y3, Y12                  // np0
	VPSUBW Y14, Y4, Y13                  // nq0
	VPBROADCASTD dbC2<>(SB), Y15
	VPSLLW $1, Y2, Y14
	VPADDW Y3, Y14, Y14
	VPADDW Y5, Y14, Y14
	VPADDW Y15, Y14, Y14
	VPSRAW $2, Y14, Y14                  // sp0
	VPBLENDVB Y11, Y14, Y12, Y12
	VPSLLW $1, Y5, Y14
	VPADDW Y4, Y14, Y14
	VPADDW Y2, Y14, Y14
	VPADDW Y15, Y14, Y14
	VPSRAW $2, Y14, Y14                  // sq0
	VPBLENDVB Y11, Y14, Y13, Y13
	VPBLENDVB Y9, Y12, Y3, Y3
	VPBLENDVB Y9, Y13, Y4, Y4
	CMPQ R8, $1
	JNE csth
	TRANSPOSE8(Y0, Y1, Y2, Y3, Y4, Y5, Y6, Y7, Y8, Y9, Y10, Y11, Y12, Y13, Y14, Y15)
	ST8X2(Y8, X8, X0, (SI), (R11))
	ST8X2(Y9, X9, X0, (SI)(R9*1), (R11)(R9*1))
	ST8X2(Y10, X10, X0, (SI)(R9*2), (R11)(R9*2))
	ST8X2(Y11, X11, X0, (SI)(R10*1), (R11)(R10*1))
	ST8X2(Y12, X12, X0, (R12), (R13))
	ST8X2(Y13, X13, X0, (R12)(R9*1), (R13)(R9*1))
	ST8X2(Y14, X14, X0, (R12)(R9*2), (R13)(R9*2))
	ST8X2(Y15, X15, X0, (R12)(R10*1), (R13)(R10*1))
	VZEROUPPER
	RET
csth:
	ST8X2(Y3, X3, X0, (SI)(R8*1), (R11)(R8*1))
	ST8X2(Y4, X4, X0, (DI), (DX))
cdone:
	VZEROUPPER
	RET
