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

// The filters work on 16, then 8, then 4 columns at once, w4 (a multiple
// of 4) in all. A tap pair (c[2k], c[2k+1]) multiplies the samples
// (s[x+2k], s[x+2k+1]) with VPMADDWD: in 32 bits, so 12-bit samples fit.

// Horizontally, the even outputs of 16 come from the dwords of the loads
// at s+2k, the odd ones from those at s+2k+1; the two are then woven back
// together, word by word.

// HPAIR accumulates tap pair k (coefficients in C) of the even (E) and
// odd (O) outputs, at CX + 4k bytes.
#define HPAIR(off, C, E, O, T) \
	VPMADDWD off(CX), C, T; \
	VPADDD   T, E, E;       \
	VPMADDWD off+2(CX), C, T; \
	VPADDD   T, O, O

#define HPAIR0(C, E, O) \
	VPMADDWD (CX), C, E; \
	VPMADDWD 2(CX), C, O

// HWEAVE shifts the sums and puts the odd outputs between the even ones.
#define HWEAVE(E, O) \
	VPSRAD  X15, E, E;       \
	VPSRAD  X15, O, O;       \
	VPSLLD  $16, O, O;       \
	VPBLENDW $0xAA, O, E, E

// func hFilterAVX2(dst *int16, dstride int, src *uint16, sstride int, w4, h int, c *tapPairs, pairs int, shift int)
TEXT ·hFilterAVX2(SB), NOSPLIT, $0-72
	MOVQ dst+0(FP), DI
	MOVQ dstride+8(FP), R8
	SHLQ $1, R8
	MOVQ src+16(FP), SI
	MOVQ sstride+24(FP), R9
	SHLQ $1, R9
	MOVQ w4+32(FP), R10
	SHLQ $1, R10
	MOVQ h+40(FP), R11
	MOVQ c+48(FP), BX
	MOVQ pairs+56(FP), R12
	MOVQ shift+64(FP), AX
	VMOVQ AX, X15
	VPBROADCASTD (BX), Y8
	VPBROADCASTD 4(BX), Y9
	VPBROADCASTD 8(BX), Y10
	VPBROADCASTD 12(BX), Y11
	TESTQ R11, R11
	JZ    hdone
	CMPQ  R12, $2
	JEQ   h4row

h8row:
	XORQ AX, AX
h8loop16:
	LEAQ 32(AX), DX
	CMPQ DX, R10
	JGT  h8tail8
	LEAQ (SI)(AX*1), CX
	HPAIR0(Y8, Y0, Y1)
	HPAIR(4, Y9, Y0, Y1, Y2)
	HPAIR(8, Y10, Y0, Y1, Y2)
	HPAIR(12, Y11, Y0, Y1, Y2)
	HWEAVE(Y0, Y1)
	VMOVDQU Y0, (DI)(AX*1)
	ADDQ $32, AX
	JMP  h8loop16
h8tail8:
	LEAQ 16(AX), DX
	CMPQ DX, R10
	JGT  h8tail4
	LEAQ (SI)(AX*1), CX
	HPAIR0(X8, X0, X1)
	HPAIR(4, X9, X0, X1, X2)
	HPAIR(8, X10, X0, X1, X2)
	HPAIR(12, X11, X0, X1, X2)
	HWEAVE(X0, X1)
	VMOVDQU X0, (DI)(AX*1)
	ADDQ $16, AX
h8tail4:
	LEAQ 8(AX), DX
	CMPQ DX, R10
	JGT  h8next
	LEAQ (SI)(AX*1), CX
	// Four outputs need s[x .. x+10]: 64-bit loads, read no further.
	VMOVQ (CX), X3
	VPMADDWD X3, X8, X0
	VMOVQ 2(CX), X3
	VPMADDWD X3, X8, X1
	VMOVQ 4(CX), X3
	VPMADDWD X3, X9, X2
	VPADDD X2, X0, X0
	VMOVQ 6(CX), X3
	VPMADDWD X3, X9, X2
	VPADDD X2, X1, X1
	VMOVQ 8(CX), X3
	VPMADDWD X3, X10, X2
	VPADDD X2, X0, X0
	VMOVQ 10(CX), X3
	VPMADDWD X3, X10, X2
	VPADDD X2, X1, X1
	VMOVQ 12(CX), X3
	VPMADDWD X3, X11, X2
	VPADDD X2, X0, X0
	VMOVQ 14(CX), X3
	VPMADDWD X3, X11, X2
	VPADDD X2, X1, X1
	HWEAVE(X0, X1)
	VMOVQ X0, (DI)(AX*1)
h8next:
	ADDQ R8, DI
	ADDQ R9, SI
	DECQ R11
	JNZ  h8row
	JMP  hdone

h4row:
	XORQ AX, AX
h4loop16:
	LEAQ 32(AX), DX
	CMPQ DX, R10
	JGT  h4tail8
	LEAQ (SI)(AX*1), CX
	HPAIR0(Y8, Y0, Y1)
	HPAIR(4, Y9, Y0, Y1, Y2)
	HWEAVE(Y0, Y1)
	VMOVDQU Y0, (DI)(AX*1)
	ADDQ $32, AX
	JMP  h4loop16
h4tail8:
	LEAQ 16(AX), DX
	CMPQ DX, R10
	JGT  h4tail4
	LEAQ (SI)(AX*1), CX
	HPAIR0(X8, X0, X1)
	HPAIR(4, X9, X0, X1, X2)
	HWEAVE(X0, X1)
	VMOVDQU X0, (DI)(AX*1)
	ADDQ $16, AX
h4tail4:
	LEAQ 8(AX), DX
	CMPQ DX, R10
	JGT  h4next
	LEAQ (SI)(AX*1), CX
	// s[x .. x+6].
	VMOVQ (CX), X3
	VPMADDWD X3, X8, X0
	VMOVQ 2(CX), X3
	VPMADDWD X3, X8, X1
	VMOVQ 4(CX), X3
	VPMADDWD X3, X9, X2
	VPADDD X2, X0, X0
	VMOVQ 6(CX), X3
	VPMADDWD X3, X9, X2
	VPADDD X2, X1, X1
	HWEAVE(X0, X1)
	VMOVQ X0, (DI)(AX*1)
h4next:
	ADDQ R8, DI
	ADDQ R9, SI
	DECQ R11
	JNZ  h4row

hdone:
	VZEROUPPER
	RET

// Vertically, two rows are interleaved word by word (within each 128-bit
// lane, which packing back undoes) and multiplied by a tap pair.

// func vFilterAVX2(dst *int16, dstride int, src unsafe.Pointer, sstride int, w4, h int, c *tapPairs, pairs int, shift int)
TEXT ·vFilterAVX2(SB), NOSPLIT, $0-72
	MOVQ dst+0(FP), DI
	MOVQ dstride+8(FP), R8
	SHLQ $1, R8
	MOVQ src+16(FP), SI
	MOVQ sstride+24(FP), R9
	SHLQ $1, R9
	MOVQ w4+32(FP), R10
	SHLQ $1, R10
	MOVQ h+40(FP), R11
	MOVQ c+48(FP), BX
	MOVQ pairs+56(FP), R12
	MOVQ shift+64(FP), AX
	VMOVQ AX, X15
	VPBROADCASTD (BX), Y8
	VPBROADCASTD 4(BX), Y9
	VPBROADCASTD 8(BX), Y10
	VPBROADCASTD 12(BX), Y11
	TESTQ R11, R11
	JZ    vdone

vrow:
	XORQ AX, AX
vloop16:
	LEAQ 32(AX), DX
	CMPQ DX, R10
	JGT  vtail8
	LEAQ (SI)(AX*1), DX
	VPXOR Y0, Y0, Y0
	VPXOR Y1, Y1, Y1
	VMOVDQU (DX), Y2
	VMOVDQU (DX)(R9*1), Y3
	VPUNPCKLWD Y3, Y2, Y4
	VPUNPCKHWD Y3, Y2, Y5
	VPMADDWD Y8, Y4, Y0
	VPMADDWD Y8, Y5, Y1
	LEAQ (DX)(R9*2), DX
	VMOVDQU (DX), Y2
	VMOVDQU (DX)(R9*1), Y3
	VPUNPCKLWD Y3, Y2, Y4
	VPUNPCKHWD Y3, Y2, Y5
	VPMADDWD Y9, Y4, Y4
	VPMADDWD Y9, Y5, Y5
	VPADDD Y4, Y0, Y0
	VPADDD Y5, Y1, Y1
	CMPQ R12, $2
	JEQ  vend16
	LEAQ (DX)(R9*2), DX
	VMOVDQU (DX), Y2
	VMOVDQU (DX)(R9*1), Y3
	VPUNPCKLWD Y3, Y2, Y4
	VPUNPCKHWD Y3, Y2, Y5
	VPMADDWD Y10, Y4, Y4
	VPMADDWD Y10, Y5, Y5
	VPADDD Y4, Y0, Y0
	VPADDD Y5, Y1, Y1
	LEAQ (DX)(R9*2), DX
	VMOVDQU (DX), Y2
	VMOVDQU (DX)(R9*1), Y3
	VPUNPCKLWD Y3, Y2, Y4
	VPUNPCKHWD Y3, Y2, Y5
	VPMADDWD Y11, Y4, Y4
	VPMADDWD Y11, Y5, Y5
	VPADDD Y4, Y0, Y0
	VPADDD Y5, Y1, Y1
vend16:
	VPSRAD X15, Y0, Y0
	VPSRAD X15, Y1, Y1
	VPACKSSDW Y1, Y0, Y0
	VMOVDQU Y0, (DI)(AX*1)
	ADDQ $32, AX
	JMP  vloop16
vtail8:
	LEAQ 16(AX), DX
	CMPQ DX, R10
	JGT  vtail4
	LEAQ (SI)(AX*1), DX
	VMOVDQU (DX), X2
	VMOVDQU (DX)(R9*1), X3
	VPUNPCKLWD X3, X2, X4
	VPUNPCKHWD X3, X2, X5
	VPMADDWD X8, X4, X0
	VPMADDWD X8, X5, X1
	LEAQ (DX)(R9*2), DX
	VMOVDQU (DX), X2
	VMOVDQU (DX)(R9*1), X3
	VPUNPCKLWD X3, X2, X4
	VPUNPCKHWD X3, X2, X5
	VPMADDWD X9, X4, X4
	VPMADDWD X9, X5, X5
	VPADDD X4, X0, X0
	VPADDD X5, X1, X1
	CMPQ R12, $2
	JEQ  vend8
	LEAQ (DX)(R9*2), DX
	VMOVDQU (DX), X2
	VMOVDQU (DX)(R9*1), X3
	VPUNPCKLWD X3, X2, X4
	VPUNPCKHWD X3, X2, X5
	VPMADDWD X10, X4, X4
	VPMADDWD X10, X5, X5
	VPADDD X4, X0, X0
	VPADDD X5, X1, X1
	LEAQ (DX)(R9*2), DX
	VMOVDQU (DX), X2
	VMOVDQU (DX)(R9*1), X3
	VPUNPCKLWD X3, X2, X4
	VPUNPCKHWD X3, X2, X5
	VPMADDWD X11, X4, X4
	VPMADDWD X11, X5, X5
	VPADDD X4, X0, X0
	VPADDD X5, X1, X1
vend8:
	VPSRAD X15, X0, X0
	VPSRAD X15, X1, X1
	VPACKSSDW X1, X0, X0
	VMOVDQU X0, (DI)(AX*1)
	ADDQ $16, AX
vtail4:
	LEAQ 8(AX), DX
	CMPQ DX, R10
	JGT  vnext
	LEAQ (SI)(AX*1), DX
	VMOVQ (DX), X2
	VMOVQ (DX)(R9*1), X3
	VPUNPCKLWD X3, X2, X4
	VPMADDWD X8, X4, X0
	LEAQ (DX)(R9*2), DX
	VMOVQ (DX), X2
	VMOVQ (DX)(R9*1), X3
	VPUNPCKLWD X3, X2, X4
	VPMADDWD X9, X4, X4
	VPADDD X4, X0, X0
	CMPQ R12, $2
	JEQ  vend4
	LEAQ (DX)(R9*2), DX
	VMOVQ (DX), X2
	VMOVQ (DX)(R9*1), X3
	VPUNPCKLWD X3, X2, X4
	VPMADDWD X10, X4, X4
	VPADDD X4, X0, X0
	LEAQ (DX)(R9*2), DX
	VMOVQ (DX), X2
	VMOVQ (DX)(R9*1), X3
	VPUNPCKLWD X3, X2, X4
	VPMADDWD X11, X4, X4
	VPADDD X4, X0, X0
vend4:
	VPSRAD X15, X0, X0
	VPACKSSDW X0, X0, X0
	VMOVQ X0, (DI)(AX*1)
vnext:
	ADDQ R8, DI
	ADDQ R9, SI
	DECQ R11
	JNZ  vrow

vdone:
	VZEROUPPER
	RET

// func copyAVX2(dst *int16, dstride int, src *uint16, sstride int, w4, h int, shift int)
TEXT ·copyAVX2(SB), NOSPLIT, $0-56
	MOVQ dst+0(FP), DI
	MOVQ dstride+8(FP), R8
	SHLQ $1, R8
	MOVQ src+16(FP), SI
	MOVQ sstride+24(FP), R9
	SHLQ $1, R9
	MOVQ w4+32(FP), R10
	SHLQ $1, R10
	MOVQ h+40(FP), R11
	MOVQ shift+48(FP), AX
	VMOVQ AX, X15
	TESTQ R11, R11
	JZ    cdone
crow:
	XORQ AX, AX
cloop16:
	LEAQ 32(AX), DX
	CMPQ DX, R10
	JGT  ctail8
	VMOVDQU (SI)(AX*1), Y0
	VPSLLW  X15, Y0, Y0
	VMOVDQU Y0, (DI)(AX*1)
	ADDQ $32, AX
	JMP  cloop16
ctail8:
	LEAQ 16(AX), DX
	CMPQ DX, R10
	JGT  ctail4
	VMOVDQU (SI)(AX*1), X0
	VPSLLW  X15, X0, X0
	VMOVDQU X0, (DI)(AX*1)
	ADDQ $16, AX
ctail4:
	LEAQ 8(AX), DX
	CMPQ DX, R10
	JGT  cnext
	VMOVQ  (SI)(AX*1), X0
	VPSLLW X15, X0, X0
	VMOVQ  X0, (DI)(AX*1)
cnext:
	ADDQ R8, DI
	ADDQ R9, SI
	DECQ R11
	JNZ  crow
cdone:
	VZEROUPPER
	RET

// PUT computes clip(((a*w0 + b*w1 + add) >> shift) + o) of the words in A
// and B (A and B are overwritten; the result is in A).
#define PUT(A, B, T, W, ADD, O, MAX, ZERO) \
	VPUNPCKHWD B, A, T;  \
	VPUNPCKLWD B, A, A;  \
	VPMADDWD W, A, A;    \
	VPMADDWD W, T, T;    \
	VPADDD   ADD, A, A;  \
	VPADDD   ADD, T, T;  \
	VPSRAD   X15, A, A;  \
	VPSRAD   X15, T, T;  \
	VPADDD   O, A, A;    \
	VPADDD   O, T, T;    \
	VPACKSSDW T, A, A;   \
	VPMAXSW  ZERO, A, A; \
	VPMINSW  MAX, A, A

// func putAVX2(dst *uint16, dstride int, a, b *int16, abstride, w4, h, wts, add, shift, o, maxV int)
TEXT ·putAVX2(SB), NOSPLIT, $0-96
	MOVQ dst+0(FP), DI
	MOVQ dstride+8(FP), R8
	SHLQ $1, R8
	MOVQ a+16(FP), SI
	MOVQ b+24(FP), BX
	MOVQ abstride+32(FP), R9
	SHLQ $1, R9
	MOVQ w4+40(FP), R10
	SHLQ $1, R10
	MOVQ h+48(FP), R11
	MOVQ wts+56(FP), AX
	VMOVQ AX, X8
	VPBROADCASTD X8, Y8
	MOVQ add+64(FP), AX
	VMOVQ AX, X9
	VPBROADCASTD X9, Y9
	MOVQ shift+72(FP), AX
	VMOVQ AX, X15
	MOVQ o+80(FP), AX
	VMOVQ AX, X10
	VPBROADCASTD X10, Y10
	MOVQ maxV+88(FP), AX
	VMOVQ AX, X11
	VPBROADCASTW X11, Y11
	VPXOR Y12, Y12, Y12
	TESTQ R11, R11
	JZ    pdone
prow:
	XORQ AX, AX
ploop16:
	LEAQ 32(AX), DX
	CMPQ DX, R10
	JGT  ptail8
	VMOVDQU (SI)(AX*1), Y0
	VMOVDQU (BX)(AX*1), Y1
	PUT(Y0, Y1, Y2, Y8, Y9, Y10, Y11, Y12)
	VMOVDQU Y0, (DI)(AX*1)
	ADDQ $32, AX
	JMP  ploop16
ptail8:
	LEAQ 16(AX), DX
	CMPQ DX, R10
	JGT  ptail4
	VMOVDQU (SI)(AX*1), X0
	VMOVDQU (BX)(AX*1), X1
	PUT(X0, X1, X2, X8, X9, X10, X11, X12)
	VMOVDQU X0, (DI)(AX*1)
	ADDQ $16, AX
ptail4:
	LEAQ 8(AX), DX
	CMPQ DX, R10
	JGT  pnext
	VMOVQ (SI)(AX*1), X0
	VMOVQ (BX)(AX*1), X1
	PUT(X0, X1, X2, X8, X9, X10, X11, X12)
	VMOVQ X0, (DI)(AX*1)
pnext:
	ADDQ R8, DI
	ADDQ R9, SI
	ADDQ R9, BX
	DECQ R11
	JNZ  prow
pdone:
	VZEROUPPER
	RET

// The packing kernels: 16 samples at a time, n a multiple of 16.

// func packBytesAVX2(dst *byte, src *uint16, n int)
TEXT ·packBytesAVX2(SB), NOSPLIT, $0-24
	MOVQ dst+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ n+16(FP), CX
pbloop:
	VMOVDQU (SI), Y0
	// The two lanes' bytes, then lane 0's eight to the bottom.
	VPACKUSWB Y0, Y0, Y0
	VPERMQ $0x08, Y0, Y0
	VMOVDQU X0, (DI)
	ADDQ $32, SI
	ADDQ $16, DI
	SUBQ $16, CX
	JNZ  pbloop
	VZEROUPPER
	RET

// func packWordsAVX2(dst *byte, src *uint16, n int, shift int)
TEXT ·packWordsAVX2(SB), NOSPLIT, $0-32
	MOVQ dst+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ n+16(FP), CX
	MOVQ shift+24(FP), AX
	VMOVQ AX, X15
pwloop:
	VMOVDQU (SI), Y0
	VPSLLW  X15, Y0, Y0
	VMOVDQU Y0, (DI)
	ADDQ $32, SI
	ADDQ $32, DI
	SUBQ $16, CX
	JNZ  pwloop
	VZEROUPPER
	RET

// func weaveBytesAVX2(dst *byte, cb, cr *uint16, n int)
TEXT ·weaveBytesAVX2(SB), NOSPLIT, $0-32
	MOVQ dst+0(FP), DI
	MOVQ cb+8(FP), SI
	MOVQ cr+16(FP), DX
	MOVQ n+24(FP), CX
wbloop:
	// Cr's samples into the high bytes of Cb's words: cb | cr<<8.
	VMOVDQU (SI), Y0
	VMOVDQU (DX), Y1
	VPSLLW  $8, Y1, Y1
	VPOR    Y1, Y0, Y0
	VMOVDQU Y0, (DI)
	ADDQ $32, SI
	ADDQ $32, DX
	ADDQ $32, DI
	SUBQ $16, CX
	JNZ  wbloop
	VZEROUPPER
	RET

// func weaveWordsAVX2(dst *byte, cb, cr *uint16, n int, shift int)
TEXT ·weaveWordsAVX2(SB), NOSPLIT, $0-40
	MOVQ dst+0(FP), DI
	MOVQ cb+8(FP), SI
	MOVQ cr+16(FP), DX
	MOVQ n+24(FP), CX
	MOVQ shift+32(FP), AX
	VMOVQ AX, X15
wwloop:
	VMOVDQU (SI), Y0
	VMOVDQU (DX), Y1
	VPSLLW  X15, Y0, Y0
	VPSLLW  X15, Y1, Y1
	// In-lane interleaving gives samples 0-3, 8-11 and 4-7, 12-15; the
	// lanes are put back in order.
	VPUNPCKLWD Y1, Y0, Y2
	VPUNPCKHWD Y1, Y0, Y3
	VPERM2I128 $0x20, Y3, Y2, Y4
	VPERM2I128 $0x31, Y3, Y2, Y5
	VMOVDQU Y4, (DI)
	VMOVDQU Y5, 32(DI)
	ADDQ $32, SI
	ADDQ $32, DX
	ADDQ $64, DI
	SUBQ $16, CX
	JNZ  wwloop
	VZEROUPPER
	RET
