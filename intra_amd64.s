//go:build amd64 && !purego

#include "textflag.h"

// Intra NxN prediction (AVX2, 128-bit). The edge samples are gathered
// into a byte array B: B[0..1] = left[n-1] (replication), B[9-y] =
// left[y], B[10] = top-left, B[11+x] = top[x] for x < 2n (top-right
// replicated when unavailable), B[11+2n] = top[2n-1]; unavailable edges
// read as 0. The "average of 2" A2[k] = (B[k]+B[k+1]+1)>>1 and "average
// of 3" A3[k] = (B[k]+2B[k+1]+B[k+2]+2)>>2 arrays give every angular mode
// as contiguous runs (8.3.1.2, 8.3.2.2). For 8x8 blocks the references
// are first filtered, which is the A3 array of the unfiltered samples
// shifted by one, with the end rules of 8.3.2.2.1.
//
// Frame layout: B at -32(SP), A2 at -64(SP), A3 at -96(SP), 32 bytes of
// row scratch at -128(SP).

DATA inC16<>+0(SB)/4, $0x00100010
GLOBL inC16<>(SB), RODATA, $4
// x-7 for x = 0..15 as words
DATA inX7<>+0(SB)/8, $0xfffcfffbfffafff9
DATA inX7<>+8(SB)/8, $0x0000fffffffefffd
DATA inX7<>+16(SB)/8, $0x0004000300020001
DATA inX7<>+24(SB)/8, $0x0008000700060005
GLOBL inX7<>(SB), RODATA, $32

// top row fix-ups indexed by (n == 8)*2 + topRight: replicate top[n-1]
// into the top-right and add one more replicated sample
DATA inTopFix<>+0(SB)/8, $0x0303030303020100
DATA inTopFix<>+8(SB)/8, $0x8080808080808003
DATA inTopFix<>+16(SB)/8, $0x0706050403020100
DATA inTopFix<>+24(SB)/8, $0x8080808080808007
DATA inTopFix<>+32(SB)/8, $0x0706050403020100
DATA inTopFix<>+40(SB)/8, $0x0707070707070707
DATA inTopFix<>+48(SB)/8, $0x0706050403020100
DATA inTopFix<>+56(SB)/8, $0x0f0e0d0c0b0a0908
GLOBL inTopFix<>(SB), RODATA, $64
// [-, -, -, -, -, rep, rep, left[7..0], tl] from [left[7..0], tl]
DATA inLeftFix<>+0(SB)/8, $0x0000008080808080
DATA inLeftFix<>+8(SB)/8, $0x0807060504030201
GLOBL inLeftFix<>(SB), RODATA, $16
// B[16..31] from the top row: top[5..15], top[15], 0
DATA inHi<>+0(SB)/8, $0x0c0b0a0908070605
DATA inHi<>+8(SB)/8, $0x808080800f0f0e0d
GLOBL inHi<>(SB), RODATA, $16
DATA inOnes<>+0(SB)/8, $0x0101010101010101
DATA inOnes<>+8(SB)/8, $0x0101010101010101
GLOBL inOnes<>(SB), RODATA, $16
// re-replication after filtering: B[0] = B[1] = B[2]; B[27] = B[26]
DATA inRep2<>+0(SB)/8, $0x0706050403020202
DATA inRep2<>+8(SB)/8, $0x0f0e0d0c0b0a0908
GLOBL inRep2<>(SB), RODATA, $16
DATA inRep27<>+0(SB)/8, $0x0706050403020100
DATA inRep27<>+8(SB)/8, $0x0f0e0d0c0a0a0908
GLOBL inRep27<>(SB), RODATA, $16
// L (left forward, replicated) from B[0..15], for n = 4 and 8
DATA inL4<>+0(SB)/8, $0x0606060606070809
DATA inL4<>+8(SB)/8, $0x0606060606060606
GLOBL inL4<>(SB), RODATA, $16
DATA inL8<>+0(SB)/8, $0x0203040506070809
DATA inL8<>+8(SB)/8, $0x0202020202020202
GLOBL inL8<>(SB), RODATA, $16

// AVG3 computes (a + 2b + c + 2) >> 2 per byte into dst, using t1, t2:
// pavgb(b, pavgb(a, c) - ((a ^ c) & 1)).
#define AVG3(a, b, c, dst, t1, t2) \
	VPAVGB a, c, t1; \
	VPXOR a, c, t2; \
	VPAND inOnes<>(SB), t2, t2; \
	VPSUBB t2, t1, t1; \
	VPAVGB b, t1, dst

// STOREAX stores the n low bytes of AX at (DI).
#define STOREAX \
	CMPQ R10, $8; \
	JEQ 3(PC); \
	MOVL AX, (DI); \
	JMP 2(PC); \
	MOVQ AX, (DI)

// func predNxNAsm(p *byte, stride, n, mode, avail int)
// avail: bit 0 left, bit 1 top, bit 2 top-right, bit 3 top-left.
TEXT ·predNxNAsm(SB), NOSPLIT, $128-40
	MOVQ p+0(FP), DI
	MOVQ stride+8(FP), R8
	MOVQ n+16(FP), R10
	MOVQ mode+24(FP), R11
	MOVQ avail+32(FP), R9
	// top: 16 samples, then replication/zeroing by table (X0)
	VPXOR X0, X0, X0
	BTQ $1, R9
	JCC notop
	MOVQ DI, SI
	SUBQ R8, SI
	VMOVDQU (SI), X0
notop:
	MOVQ R9, CX
	SHRQ $2, CX
	ANDQ $1, CX
	MOVQ R10, AX
	ANDQ $8, AX
	SHRQ $2, AX
	ORQ CX, AX
	SHLQ $4, AX
	LEAQ inTopFix<>(SB), BX
	VPSHUFB (BX)(AX*1), X0, X0
	// left column into R12: byte 7-y = left[y] (n = 4: bytes 0..3 =
	// left[3])
	XORL R12, R12
	BTQ $0, R9
	JCC noleft
	LEAQ -1(DI), SI
	LEAQ (R8)(R8*2), R13
	MOVBLZX (SI), R12
	MOVBLZX (SI)(R8*1), BX
	SHLQ $8, R12
	ORQ BX, R12
	MOVBLZX (SI)(R8*2), BX
	SHLQ $8, R12
	ORQ BX, R12
	MOVBLZX (SI)(R13*1), BX
	SHLQ $8, R12
	ORQ BX, R12
	CMPQ R10, $8
	JEQ left8
	IMULQ $0x01010101, BX
	SHLQ $32, R12
	ORQ BX, R12
	JMP noleft
left8:
	LEAQ (SI)(R8*4), SI
	MOVBLZX (SI), BX
	SHLQ $8, R12
	ORQ BX, R12
	MOVBLZX (SI)(R8*1), BX
	SHLQ $8, R12
	ORQ BX, R12
	MOVBLZX (SI)(R8*2), BX
	SHLQ $8, R12
	ORQ BX, R12
	MOVBLZX (SI)(R13*1), BX
	SHLQ $8, R12
	ORQ BX, R12
noleft:
	// top-left
	XORL BX, BX
	BTQ $3, R9
	JCC notl
	MOVQ DI, SI
	SUBQ R8, SI
	MOVBLZX -1(SI), BX
notl:
	VMOVQ R12, X1
	VPINSRB $8, BX, X1, X1
	VPSHUFB inLeftFix<>(SB), X1, X1
	VPALIGNR $5, X1, X0, X2            // B[0..15]
	VPSHUFB inHi<>(SB), X0, X3         // B[16..31]
	CMPQ R10, $8
	JNE angles
	// 8x8: filter the references: F[k] = (B[k-1] + 2B[k] + B[k+1] + 2)>>2
	VPALIGNR $1, X2, X3, X4            // B[1..16]
	VPSRLDQ $1, X3, X5                 // B[17..]
	VPSLLDQ $1, X2, X6                 // B[-1..14]
	VPALIGNR $15, X2, X3, X7           // B[15..30]
	AVG3(X6, X2, X4, X8, X10, X11)
	AVG3(X7, X3, X5, X9, X10, X11)
	BTQ $3, R9
	JCS ftl
	// no top-left: top[0] = (3t0 + t1 + 2)>>2, left[0] = (3l0 + l1 + 2)>>2
	VMOVDQU X2, b-32(SP)
	MOVBLZX b-32+11(SP), AX
	LEAQ 2(AX)(AX*2), AX
	MOVBLZX b-32+12(SP), CX
	ADDQ CX, AX
	SHRQ $2, AX
	VPINSRB $11, AX, X8, X8
	MOVBLZX b-32+9(SP), AX
	LEAQ 2(AX)(AX*2), AX
	MOVBLZX b-32+8(SP), CX
	ADDQ CX, AX
	SHRQ $2, AX
	VPINSRB $9, AX, X8, X8
	JMP fdone
ftl:
	// top-left with both neighbours is the plain filter; with one it is
	// (3tl + t0 + 2)>>2 or (3tl + l0 + 2)>>2, with none tl
	MOVQ R9, CX
	ANDQ $3, CX
	CMPQ CX, $3
	JEQ fdone
	VMOVDQU X2, b-32(SP)
	MOVBLZX b-32+10(SP), AX
	TESTQ CX, CX
	JEQ ftl3
	LEAQ 2(AX)(AX*2), AX
	CMPQ CX, $2
	JNE ftl2
	MOVBLZX b-32+11(SP), BX            // top only
	ADDQ BX, AX
	SHRQ $2, AX
	JMP ftl3
ftl2:
	MOVBLZX b-32+9(SP), BX             // left only
	ADDQ BX, AX
	SHRQ $2, AX
ftl3:
	VPINSRB $10, AX, X8, X8
fdone:
	VPSHUFB inRep2<>(SB), X8, X2
	VPSHUFB inRep27<>(SB), X9, X3
	// the filtered top row and left column for V, H and DC
	VPALIGNR $11, X2, X3, X0
	VPSRLDQ $2, X2, X1
	VMOVQ X1, R12
angles:
	// dispatch
	CMPQ R11, $2
	JEQ mdc
	JLT mvh
	CMPQ R11, $8
	JEQ mhu
	// A2 and A3 of B
	VPALIGNR $1, X2, X3, X4            // B[1..16]
	VPSRLDQ $1, X3, X5
	VPALIGNR $2, X2, X3, X6            // B[2..17]
	VPSRLDQ $2, X3, X7
	VPAVGB X4, X2, X8
	VPAVGB X5, X3, X9
	VMOVDQU X8, a2-64(SP)
	VMOVDQU X9, a2-48(SP)
	AVG3(X2, X4, X6, X8, X10, X11)
	AVG3(X3, X5, X7, X9, X10, X11)
	VMOVDQU X8, a3-96(SP)
	VMOVDQU X9, a3-80(SP)
	CMPQ R11, $3
	JEQ mddl
	CMPQ R11, $4
	JEQ mddr
	CMPQ R11, $5
	JEQ mvr
	CMPQ R11, $6
	JEQ mhd
	// vertical left: even rows A2[11+y/2 ..], odd rows A3[11+y/2 ..]
	LEAQ a2-64+11(SP), SI
	LEAQ a3-96+11(SP), BX
	MOVQ R10, CX
	SHRQ $1, CX
vl1:
	MOVQ (SI), AX
	STOREAX
	ADDQ R8, DI
	MOVQ (BX), AX
	STOREAX
	ADDQ R8, DI
	INCQ SI
	INCQ BX
	DECQ CX
	JNE vl1
	RET

mvh:
	TESTQ R11, R11
	JNE mh
	// vertical: top row repeated
	VMOVQ X0, AX
	MOVQ R10, CX
mv1:
	STOREAX
	ADDQ R8, DI
	DECQ CX
	JNE mv1
	RET
mh:
	// horizontal: row y = B[9-y] (the filtered left for 8x8) replicated
	VMOVDQU X2, b-32(SP)
	MOVQ $0x0101010101010101, BX
	LEAQ b-32+9(SP), SI
	MOVQ R10, CX
mh1:
	MOVBLZX (SI), AX
	IMULQ BX, AX
	STOREAX
	DECQ SI
	ADDQ R8, DI
	DECQ CX
	JNE mh1
	RET

mdc:
	// DC: sums of the available edges
	VPXOR X5, X5, X5
	XORL AX, AX
	XORL BX, BX                        // count of available edges
	BTQ $1, R9
	JCC dcnt
	INCQ BX
	VMOVQ X0, DX
	CMPQ R10, $8
	JEQ dct8
	MOVL DX, DX                        // top[0..3]
dct8:
	VMOVQ DX, X4
	VPSADBW X5, X4, X4
	VMOVQ X4, AX
dcnt:
	BTQ $0, R9
	JCC dcnl
	INCQ BX
	MOVQ R12, DX
	CMPQ R10, $8
	JEQ dcl8
	SHRQ $32, DX                       // left[0..3]
dcl8:
	VMOVQ DX, X4
	VPSADBW X5, X4, X4
	VMOVQ X4, DX
	ADDQ DX, AX
dcnl:
	MOVQ $128, CX
	TESTQ BX, BX
	JEQ dcfill
	// (sum + n*count/2) / (n*count): n*count is 4, 8 or 16
	MOVQ R10, DX
	IMULQ BX, DX                       // n*count
	MOVQ DX, CX
	SHRQ $1, CX
	ADDQ AX, CX
	TZCNTQ DX, DX
	SHRXQ DX, CX, CX
dcfill:
	MOVQ $0x0101010101010101, BX
	IMULQ BX, CX
	MOVQ CX, AX
	MOVQ R10, DX
dcf:
	STOREAX
	ADDQ R8, DI
	DECQ DX
	JNE dcf
	RET

mddl:
	// diagonal down left: row y = A3[11+y ..]
	LEAQ a3-96+11(SP), SI
	MOVQ R10, CX
ddl1:
	MOVQ (SI), AX
	STOREAX
	INCQ SI
	ADDQ R8, DI
	DECQ CX
	JNE ddl1
	RET

mddr:
	// diagonal down right: row y = A3[9-y ..]
	LEAQ a3-96+9(SP), SI
	MOVQ R10, CX
ddr1:
	MOVQ (SI), AX
	STOREAX
	DECQ SI
	ADDQ R8, DI
	DECQ CX
	JNE ddr1
	RET

mvr:
	// vertical right: row0 = A2[10..], row1 = A3[9..], row y = A3[10-y]
	// followed by row y-2 shifted right
	VMOVDQU a2-64+10(SP), X4           // row 0
	VMOVDQU a3-96+9(SP), X5            // row 1
	VMOVQ X4, AX
	STOREAX
	ADDQ R8, DI
	VMOVQ X5, AX
	STOREAX
	ADDQ R8, DI
	LEAQ a3-96+8(SP), SI               // A3[10-y] for y = 2
	MOVQ R10, CX
	SHRQ $1, CX
	DECQ CX
vr1:
	VPSLLDQ $1, X4, X4
	VPINSRB $0, (SI), X4, X4
	VMOVQ X4, AX
	STOREAX
	ADDQ R8, DI
	VPSLLDQ $1, X5, X5
	VPINSRB $0, -1(SI), X5, X5
	VMOVQ X5, AX
	STOREAX
	ADDQ R8, DI
	SUBQ $2, SI
	DECQ CX
	JNE vr1
	RET

mhd:
	// horizontal down: row0 = [A2[9], A3[9], A3[10], ...], row y =
	// [A2[9-y], A3[9-y]] followed by row y-1 shifted right by two
	VMOVDQU a3-96+9(SP), X4
	VPSLLDQ $1, X4, X4
	VPINSRB $0, a2-64+9(SP), X4, X4
	VMOVQ X4, AX
	STOREAX
	ADDQ R8, DI
	LEAQ a3-96+8(SP), SI
	LEAQ a2-64+8(SP), BX
	MOVQ R10, CX
	DECQ CX
hd1:
	VPSLLDQ $2, X4, X4
	VPINSRB $1, (SI), X4, X4
	VPINSRB $0, (BX), X4, X4
	VMOVQ X4, AX
	STOREAX
	ADDQ R8, DI
	DECQ SI
	DECQ BX
	DECQ CX
	JNE hd1
	RET

mhu:
	// horizontal up: row y = interleave(A2L[y..], A3L[y..]) with L the
	// left column forward, replicated
	CMPQ R10, $8
	JEQ hu8
	VPSHUFB inL4<>(SB), X2, X4
	JMP hu1
hu8:
	VPSHUFB inL8<>(SB), X2, X4
hu1:
	VPSRLDQ $1, X4, X5
	VPSRLDQ $2, X4, X6
	VPAVGB X5, X4, X7                  // A2L
	AVG3(X4, X5, X6, X8, X10, X11)     // A3L
	VPUNPCKLBW X8, X7, X9
	VPUNPCKHBW X8, X7, X10
	VMOVDQU X9, rows-128(SP)
	VMOVDQU X10, rows-112(SP)
	LEAQ rows-128(SP), SI
	MOVQ R10, CX
hu2:
	MOVQ (SI), AX
	STOREAX
	ADDQ $2, SI
	ADDQ R8, DI
	DECQ CX
	JNE hu2
	RET

// func predPlaneAsm(p *byte, stride, w, h int)
// Plane prediction for 16x16 luma or 8x8 chroma (8.3.3.4, 8.3.4.4).
TEXT ·predPlaneAsm(SB), NOSPLIT, $0-32
	MOVQ p+0(FP), DI
	MOVQ stride+8(FP), R8
	MOVQ w+16(FP), R10
	MOVQ h+24(FP), R11
	MOVQ DI, SI
	SUBQ R8, SI                        // top row
	// H = sum (x+1) * (top[xc+1+x] - top[xc-1-x]), xc = w/2-1
	MOVQ R10, R12
	SHRQ $1, R12
	DECQ R12                           // xc
	XORL AX, AX                        // H
	XORL CX, CX
hl:
	LEAQ 1(R12)(CX*1), DX
	MOVBLZX (SI)(DX*1), BX
	MOVQ R12, DX
	SUBQ CX, DX
	DECQ DX
	MOVBLZX (SI)(DX*1), R9             // top[xc-1-x]; x = xc gives top-left
	SUBQ R9, BX
	LEAQ 1(CX), DX
	IMULQ DX, BX
	ADDQ BX, AX
	INCQ CX
	CMPQ CX, R12
	JLE hl
	// V = sum (y+1) * (left[yc+1+y] - left[yc-1-y]), yc = h/2-1
	MOVQ R11, R13
	SHRQ $1, R13
	DECQ R13
	XORL BX, BX                        // V
	XORL CX, CX
vl:
	LEAQ 1(R13)(CX*1), DX
	IMULQ R8, DX
	MOVBLZX -1(DI)(DX*1), R9
	MOVQ R13, DX
	SUBQ CX, DX
	DECQ DX
	IMULQ R8, DX
	MOVBLZX -1(DI)(DX*1), R14
	SUBQ R14, R9
	LEAQ 1(CX), DX
	IMULQ DX, R9
	ADDQ R9, BX
	INCQ CX
	CMPQ CX, R13
	JLE vl
	// a = 16*(left[h-1] + top[w-1]); b, c with 5 (16) or 34 (8) factors
	MOVQ R11, DX
	DECQ DX
	IMULQ R8, DX
	MOVBLZX -1(DI)(DX*1), CX
	MOVBLZX -1(SI)(R10*1), DX
	ADDQ DX, CX
	SHLQ $4, CX                        // a
	MOVQ $5, DX
	CMPQ R10, $16
	JEQ bf
	MOVQ $34, DX
bf:
	IMULQ DX, AX
	ADDQ $32, AX
	SARQ $6, AX                        // b
	MOVQ $5, DX
	CMPQ R11, $16
	JEQ cf
	MOVQ $34, DX
cf:
	IMULQ DX, BX
	ADDQ $32, BX
	SARQ $6, BX                        // c
	// row y: clip((a + b*(x-xc) + c*(y-yc) + 16) >> 5)
	VMOVQ AX, X1
	VPBROADCASTW X1, Y1                // b
	MOVQ $7, DX
	SUBQ R12, DX                       // 7 - xc: shift of the x-7 table
	VMOVDQU inX7<>(SB), Y2
	VMOVQ DX, X3
	VPBROADCASTW X3, Y3
	VPADDW Y3, Y2, Y2                  // x - xc
	VPMULLW Y1, Y2, Y2                 // b*(x-xc)
	MOVQ R13, DX
	NEGQ DX
	IMULQ BX, DX
	ADDQ CX, DX
	ADDQ $16, DX                       // a + c*(0-yc) + 16
	MOVQ R11, R9
pl1:
	VMOVQ DX, X4
	VPBROADCASTW X4, Y4
	VPADDW Y2, Y4, Y4
	VPSRAW $5, Y4, Y4
	VEXTRACTI128 $1, Y4, X5
	VPACKUSWB X5, X4, X4
	CMPQ R10, $16
	JNE pl8
	VMOVDQU X4, (DI)
	JMP pln
pl8:
	VMOVQ X4, (DI)
pln:
	ADDQ BX, DX
	ADDQ R8, DI
	DECQ R9
	JNE pl1
	VZEROUPPER
	RET

// func predChromaAsm(p *byte, stride, mode, avail int)
// Chroma 8x8 prediction for modes 0 (DC per 4x4 block, 8.3.4.1-3), 1
// (horizontal) and 2 (vertical); avail bit 0 = left, bit 1 = top.
TEXT ·predChromaAsm(SB), NOSPLIT, $0-32
	MOVQ p+0(FP), DI
	MOVQ stride+8(FP), R8
	MOVQ mode+16(FP), R11
	MOVQ avail+24(FP), R9
	CMPQ R11, $1
	JEQ ch
	CMPQ R11, $2
	JEQ cv
	// DC: sums of the top halves (R12, R13) and left halves (R14, R15)
	MOVQ DI, SI
	SUBQ R8, SI
	XORL R12, R12
	XORL R13, R13
	XORL R14, R14
	XORL R15, R15
	BTQ $1, R9
	JCC cdcl
	MOVBLZX (SI), R12
	MOVBLZX 1(SI), AX
	ADDQ AX, R12
	MOVBLZX 2(SI), AX
	ADDQ AX, R12
	MOVBLZX 3(SI), AX
	ADDQ AX, R12
	MOVBLZX 4(SI), R13
	MOVBLZX 5(SI), AX
	ADDQ AX, R13
	MOVBLZX 6(SI), AX
	ADDQ AX, R13
	MOVBLZX 7(SI), AX
	ADDQ AX, R13
cdcl:
	BTQ $0, R9
	JCC cdcb
	LEAQ -1(DI), SI
	MOVBLZX (SI), R14
	MOVBLZX (SI)(R8*1), AX
	ADDQ AX, R14
	LEAQ (SI)(R8*2), SI
	MOVBLZX (SI), AX
	ADDQ AX, R14
	MOVBLZX (SI)(R8*1), AX
	ADDQ AX, R14
	LEAQ (SI)(R8*2), SI
	MOVBLZX (SI), R15
	MOVBLZX (SI)(R8*1), AX
	ADDQ AX, R15
	LEAQ (SI)(R8*2), SI
	MOVBLZX (SI), AX
	ADDQ AX, R15
	MOVBLZX (SI)(R8*1), AX
	ADDQ AX, R15
cdcb:
	// block (0,0): both, left, top; (1,0): top first; (0,1): left first;
	// (1,1): both, left, top. Values in AX (0,0), BX (1,0), CX (0,1),
	// DX (1,1).
	MOVQ R9, R10
	ANDQ $3, R10
	MOVQ $128, AX
	MOVQ $128, BX
	MOVQ $128, CX
	MOVQ $128, DX
	TESTQ R10, R10
	JEQ cdcfill
	CMPQ R10, $3
	JNE cdc1
	LEAQ 4(R12)(R14*1), AX
	SHRQ $3, AX
	LEAQ 4(R13)(R15*1), DX
	SHRQ $3, DX
	LEAQ 2(R13), BX
	SHRQ $2, BX
	LEAQ 2(R15), CX
	SHRQ $2, CX
	JMP cdcfill
cdc1:
	CMPQ R10, $1
	JNE cdc2
	// left only
	LEAQ 2(R14), AX
	SHRQ $2, AX
	MOVQ AX, BX
	MOVQ AX, CX
	LEAQ 2(R15), DX
	SHRQ $2, DX
	MOVQ DX, CX
	JMP cdcfill
cdc2:
	// top only
	LEAQ 2(R12), AX
	SHRQ $2, AX
	MOVQ AX, CX
	LEAQ 2(R13), BX
	SHRQ $2, BX
	MOVQ BX, DX
cdcfill:
	// rows 0-3: AX | BX, rows 4-7: CX | DX (4 bytes each)
	IMULQ $0x01010101, AX
	IMULQ $0x01010101, BX
	SHLQ $32, BX
	ORQ BX, AX
	IMULQ $0x01010101, CX
	IMULQ $0x01010101, DX
	SHLQ $32, DX
	ORQ DX, CX
	MOVQ $4, R10
cdcr:
	MOVQ AX, (DI)
	ADDQ R8, DI
	DECQ R10
	JNE cdcr
	MOVQ $4, R10
cdcr2:
	MOVQ CX, (DI)
	ADDQ R8, DI
	DECQ R10
	JNE cdcr2
	RET
ch:
	MOVQ $8, R10
	MOVQ $0x0101010101010101, BX
chr:
	MOVBQZX -1(DI), AX
	IMULQ BX, AX
	MOVQ AX, (DI)
	ADDQ R8, DI
	DECQ R10
	JNE chr
	RET
cv:
	MOVQ DI, SI
	SUBQ R8, SI
	MOVQ (SI), AX
	MOVQ $8, R10
cvr:
	MOVQ AX, (DI)
	ADDQ R8, DI
	DECQ R10
	JNE cvr
	RET
