// The GPU backend's kernels (gpu.go), compiled to PTX by go generate:
//
//	nvcc -ptx -arch=compute_80 -o kernels.ptx kernels.cu
//
// kernels.ptx is checked in, so a build needs no CUDA toolkit; the driver
// compiles it for the GPU it runs on. Each kernel is the GPU's form of an
// op in ops.go (or backend.go), with the same arithmetic up to the order
// of its sums.

#define GELU_K 0.70710678118654752f

// --- matrix products --------------------------------------------------
//
// C[i, j] = alpha sum_k A[i, k] B[k, j] (+ bias), tiles of 128 x 128 by
// 256 threads, each 8 x 8 of them; A is row-major (A[i lda + k]); B is
// row-major (B_N: B[k ldb + j]), transposed (B_T: B[j ldb + k]) or a
// convolution's patches (B_CONV, gathered from the picture as they are
// read); batches along the grid's z, each offset by sA, sB, sC. bias is
// per column (biasMode 1) or per row (2).

enum { B_N, B_T, B_CONV };

#define BM 128
#define BN 128
#define BK 8

template <int MODE>
__device__ __forceinline__ void gemm(int M, int N, int K, const float *__restrict__ A, int lda, long long sA,
	const float *__restrict__ B, int ldb, long long sB, float *__restrict__ C, int ldc, long long sC,
	float alpha, const float *__restrict__ bias, int biasMode,
	int cc, int ch, int cw, int kh, int kw, int stride, int pad, int wo) {
	__shared__ float As[2][BK][BM + 4];
	__shared__ float Bs[2][BK][BN + 4];
	A += blockIdx.z * sA;
	B += blockIdx.z * sB;
	C += blockIdx.z * sC;
	int tid = threadIdx.x;
	int i0 = blockIdx.y * BM, j0 = blockIdx.x * BN;
	int tx = tid % 16, ty = tid / 16;
	float acc[8][8];
#pragma unroll
	for (int a = 0; a < 8; a++)
#pragma unroll
		for (int b = 0; b < 8; b++) acc[a][b] = 0;

	float ra[4], rb[4];
	// A: row tid/2, k (tid%2)*4..+4
	int ar = tid / 2, ak = (tid % 2) * 4;
	auto loadA = [&](int k0) {
		int i = i0 + ar;
#pragma unroll
		for (int q = 0; q < 4; q++) {
			int k = k0 + ak + q;
			ra[q] = (i < M && k < K) ? A[(long long)i * lda + k] : 0.f;
		}
	};
	auto storeA = [&](int s) {
#pragma unroll
		for (int q = 0; q < 4; q++) As[s][ak + q][ar] = ra[q];
	};
	// B: N and CONV: k tid/32, j (tid%32)*4..+4; T: j tid/2, k (tid%2)*4..+4
	auto loadB = [&](int k0) {
		if (MODE == B_T) {
			int j = j0 + tid / 2, kb = (tid % 2) * 4;
#pragma unroll
			for (int q = 0; q < 4; q++) {
				int k = k0 + kb + q;
				rb[q] = (j < N && k < K) ? B[(long long)j * ldb + k] : 0.f;
			}
		} else {
			int k = k0 + tid / 32, jb = j0 + (tid % 32) * 4;
			if (MODE == B_N) {
#pragma unroll
				for (int q = 0; q < 4; q++) {
					int j = jb + q;
					rb[q] = (j < N && k < K) ? B[(long long)k * ldb + j] : 0.f;
				}
			} else {
				int ci = 0, ky = 0, kx = 0;
				if (k < K) {
					ci = k / (kh * kw);
					int r = k % (kh * kw);
					ky = r / kw;
					kx = r % kw;
				}
#pragma unroll
				for (int q = 0; q < 4; q++) {
					int j = jb + q;
					float v = 0.f;
					if (j < N && k < K) {
						int oy = j / wo, ox = j % wo;
						int sy = oy * stride - pad + ky, sx = ox * stride - pad + kx;
						if (sy >= 0 && sy < ch && sx >= 0 && sx < cw) v = B[((long long)ci * ch + sy) * cw + sx];
					}
					rb[q] = v;
				}
			}
		}
	};
	auto storeB = [&](int s) {
		if (MODE == B_T) {
			int jj = tid / 2, kb = (tid % 2) * 4;
#pragma unroll
			for (int q = 0; q < 4; q++) Bs[s][kb + q][jj] = rb[q];
		} else {
			int kk = tid / 32, jb = (tid % 32) * 4;
#pragma unroll
			for (int q = 0; q < 4; q++) Bs[s][kk][jb + q] = rb[q];
		}
	};

	loadA(0);
	loadB(0);
	storeA(0);
	storeB(0);
	__syncthreads();
	int s = 0;
	for (int k0 = 0; k0 < K; k0 += BK) {
		bool more = k0 + BK < K;
		if (more) {
			loadA(k0 + BK);
			loadB(k0 + BK);
		}
#pragma unroll
		for (int kk = 0; kk < BK; kk++) {
			float a[8], b[8];
#pragma unroll
			for (int q = 0; q < 4; q++) {
				a[q] = As[s][kk][ty * 4 + q];
				a[q + 4] = As[s][kk][64 + ty * 4 + q];
				b[q] = Bs[s][kk][tx * 4 + q];
				b[q + 4] = Bs[s][kk][64 + tx * 4 + q];
			}
#pragma unroll
			for (int x = 0; x < 8; x++)
#pragma unroll
				for (int y = 0; y < 8; y++) acc[x][y] += a[x] * b[y];
		}
		if (more) {
			storeA(s ^ 1);
			storeB(s ^ 1);
		}
		__syncthreads();
		s ^= 1;
	}
#pragma unroll
	for (int x = 0; x < 8; x++) {
		int i = i0 + (x < 4 ? ty * 4 + x : 64 + ty * 4 + x - 4);
		if (i >= M) continue;
#pragma unroll
		for (int y = 0; y < 8; y++) {
			int j = j0 + (y < 4 ? tx * 4 + y : 64 + tx * 4 + y - 4);
			if (j >= N) continue;
			float v = alpha * acc[x][y];
			if (biasMode == 1) v += bias[j];
			else if (biasMode == 2) v += bias[i];
			C[(long long)i * ldc + j] = v;
		}
	}
}

extern "C" __global__ void __launch_bounds__(256) gemm_nn(int M, int N, int K, const float *A, int lda, long long sA,
	const float *B, int ldb, long long sB, float *C, int ldc, long long sC, float alpha, const float *bias, int biasMode) {
	gemm<B_N>(M, N, K, A, lda, sA, B, ldb, sB, C, ldc, sC, alpha, bias, biasMode, 0, 0, 0, 1, 1, 1, 0, 1);
}

extern "C" __global__ void __launch_bounds__(256) gemm_nt(int M, int N, int K, const float *A, int lda, long long sA,
	const float *B, int ldb, long long sB, float *C, int ldc, long long sC, float alpha, const float *bias, int biasMode) {
	gemm<B_T>(M, N, K, A, lda, sA, B, ldb, sB, C, ldc, sC, alpha, bias, biasMode, 0, 0, 0, 1, 1, 1, 0, 1);
}

// conv2d: y [O, Ho Wo] = w [O, C kh kw] times x's patches, plus b per row.
extern "C" __global__ void __launch_bounds__(256) conv2d(int O, int N, int K, const float *w, const float *x, float *y,
	const float *bias, int biasMode, int cc, int ch, int cw, int kh, int kw, int stride, int pad, int wo) {
	gemm<B_CONV>(O, N, K, w, K, 0, x, 0, 0, y, N, 0, 1.f, bias, biasMode, cc, ch, cw, kh, kw, stride, pad, wo);
}

// --- matrix products on the tensor cores --------------------------------
//
// The same products (and modes) as gemm's, the tiles' values rounded to
// half precision on their way into shared memory and summed in single
// precision by mma.sync (m16n8k16): tiles of 128 x 128 by 256 threads, 8
// warps of 64 x 32 each, 32 of K a step. The tensors stay float: only the
// products' inputs are rounded.

#include <cuda_fp16.h>

#define TK 32
#define TKP (TK + 8) // a shared row's halves, padded off the banks

__device__ __forceinline__ void mma16816(float *c, const unsigned *a, const unsigned *b) {
	asm volatile("mma.sync.aligned.m16n8k16.row.col.f32.f16.f16.f32 {%0,%1,%2,%3}, {%4,%5,%6,%7}, {%8,%9}, {%0,%1,%2,%3};\n"
		: "+f"(c[0]), "+f"(c[1]), "+f"(c[2]), "+f"(c[3])
		: "r"(a[0]), "r"(a[1]), "r"(a[2]), "r"(a[3]), "r"(b[0]), "r"(b[1]));
}

template <int MODE>
__device__ __forceinline__ void gemmTC(int M, int N, int K, const float *__restrict__ A, int lda, long long sA,
	const float *__restrict__ B, int ldb, long long sB, float *__restrict__ C, int ldc, long long sC,
	float alpha, const float *__restrict__ bias, int biasMode,
	int cc, int ch, int cw, int kh, int kw, int stride, int pad, int wo) {
	// As[m][k], Bs[n][k]: K contiguous, as the fragments read them
	__shared__ __align__(16) __half As[2][BM][TKP];
	__shared__ __align__(16) __half Bs[2][BN][TKP];
	A += blockIdx.z * sA;
	B += blockIdx.z * sB;
	C += blockIdx.z * sC;
	int tid = threadIdx.x, lane = tid % 32, warp = tid / 32;
	int i0 = blockIdx.y * BM, j0 = blockIdx.x * BN;
	int wm = (warp / 4) * 64, wn = (warp % 4) * 32;
	int g = lane / 4, t = lane % 4;
	float acc[4][4][4];
#pragma unroll
	for (int a = 0; a < 4; a++)
#pragma unroll
		for (int b = 0; b < 4; b++)
#pragma unroll
			for (int c = 0; c < 4; c++) acc[a][b][c] = 0;

	// Each thread loads 16 of A's tile (row tid/2, k (tid%2)*16..+16) and
	// 16 of B's: for B_T as A's; for B_N and B_CONV column tid%128, k
	// (tid/128)*16..+16, the warp's columns side by side.
	float ra[16], rb[16];
	int ar = tid / 2, ak = (tid % 2) * 16;
	int bj = tid % 128, bk = (tid / 128) * 16;
	auto load = [&](int k0) {
		int i = i0 + ar;
#pragma unroll
		for (int q = 0; q < 16; q++) {
			int k = k0 + ak + q;
			ra[q] = (i < M && k < K) ? A[(long long)i * lda + k] : 0.f;
		}
		if (MODE == B_T) {
			int j = j0 + ar;
#pragma unroll
			for (int q = 0; q < 16; q++) {
				int k = k0 + ak + q;
				rb[q] = (j < N && k < K) ? B[(long long)j * ldb + k] : 0.f;
			}
		} else if (MODE == B_N) {
			int j = j0 + bj;
#pragma unroll
			for (int q = 0; q < 16; q++) {
				int k = k0 + bk + q;
				rb[q] = (j < N && k < K) ? B[(long long)k * ldb + j] : 0.f;
			}
		} else {
			int j = j0 + bj;
			int oy = j / wo, ox = j % wo;
			int k = k0 + bk, khw = kh * kw;
			int ci = k / khw, r = k % khw, ky = r / kw, kx = r % kw;
#pragma unroll
			for (int q = 0; q < 16; q++) {
				float v = 0.f;
				if (j < N && k + q < K) {
					int sy = oy * stride - pad + ky, sx = ox * stride - pad + kx;
					if (sy >= 0 && sy < ch && sx >= 0 && sx < cw) v = B[((long long)ci * ch + sy) * cw + sx];
				}
				rb[q] = v;
				if (++kx == kw) {
					kx = 0;
					if (++ky == kh) {
						ky = 0;
						ci++;
					}
				}
			}
		}
	};
	auto store = [&](int s) {
		__half2 *pa = (__half2 *)&As[s][ar][ak];
#pragma unroll
		for (int q = 0; q < 8; q++) pa[q] = __floats2half2_rn(ra[2 * q], ra[2 * q + 1]);
		__half2 *pb = MODE == B_T ? (__half2 *)&Bs[s][ar][ak] : (__half2 *)&Bs[s][bj][bk];
#pragma unroll
		for (int q = 0; q < 8; q++) pb[q] = __floats2half2_rn(rb[2 * q], rb[2 * q + 1]);
	};

	load(0);
	store(0);
	__syncthreads();
	int s = 0;
	for (int k0 = 0; k0 < K; k0 += TK) {
		bool more = k0 + TK < K;
		if (more) load(k0 + TK);
#pragma unroll
		for (int kk = 0; kk < TK; kk += 16) {
			unsigned af[4][4], bf[4][2];
#pragma unroll
			for (int mi = 0; mi < 4; mi++) {
				int r = wm + mi * 16 + g;
				af[mi][0] = *(const unsigned *)&As[s][r][kk + 2 * t];
				af[mi][1] = *(const unsigned *)&As[s][r + 8][kk + 2 * t];
				af[mi][2] = *(const unsigned *)&As[s][r][kk + 2 * t + 8];
				af[mi][3] = *(const unsigned *)&As[s][r + 8][kk + 2 * t + 8];
			}
#pragma unroll
			for (int ni = 0; ni < 4; ni++) {
				int c = wn + ni * 8 + g;
				bf[ni][0] = *(const unsigned *)&Bs[s][c][kk + 2 * t];
				bf[ni][1] = *(const unsigned *)&Bs[s][c][kk + 2 * t + 8];
			}
#pragma unroll
			for (int mi = 0; mi < 4; mi++)
#pragma unroll
				for (int ni = 0; ni < 4; ni++) mma16816(acc[mi][ni], af[mi], bf[ni]);
		}
		if (more) store(s ^ 1);
		__syncthreads();
		s ^= 1;
	}
#pragma unroll
	for (int mi = 0; mi < 4; mi++)
#pragma unroll
		for (int ni = 0; ni < 4; ni++)
#pragma unroll
			for (int c = 0; c < 4; c++) {
				int i = i0 + wm + mi * 16 + g + (c >= 2 ? 8 : 0);
				int j = j0 + wn + ni * 8 + 2 * t + (c & 1);
				if (i >= M || j >= N) continue;
				float v = alpha * acc[mi][ni][c];
				if (biasMode == 1) v += bias[j];
				else if (biasMode == 2) v += bias[i];
				C[(long long)i * ldc + j] = v;
			}
}

extern "C" __global__ void __launch_bounds__(256) gemm_nn_tc(int M, int N, int K, const float *A, int lda, long long sA,
	const float *B, int ldb, long long sB, float *C, int ldc, long long sC, float alpha, const float *bias, int biasMode) {
	gemmTC<B_N>(M, N, K, A, lda, sA, B, ldb, sB, C, ldc, sC, alpha, bias, biasMode, 0, 0, 0, 1, 1, 1, 0, 1);
}

extern "C" __global__ void __launch_bounds__(256) gemm_nt_tc(int M, int N, int K, const float *A, int lda, long long sA,
	const float *B, int ldb, long long sB, float *C, int ldc, long long sC, float alpha, const float *bias, int biasMode) {
	gemmTC<B_T>(M, N, K, A, lda, sA, B, ldb, sB, C, ldc, sC, alpha, bias, biasMode, 0, 0, 0, 1, 1, 1, 0, 1);
}

extern "C" __global__ void __launch_bounds__(256) conv2d_tc(int O, int N, int K, const float *w, const float *x, float *y,
	const float *bias, int biasMode, int cc, int ch, int cw, int kh, int kw, int stride, int pad, int wo) {
	gemmTC<B_CONV>(O, N, K, w, K, 0, x, 0, 0, y, N, 0, 1.f, bias, biasMode, cc, ch, cw, kh, kw, stride, pad, wo);
}

// --- reductions ---------------------------------------------------------

__device__ __forceinline__ float warpSum(float v) {
	for (int o = 16; o > 0; o >>= 1) v += __shfl_xor_sync(0xffffffff, v, o);
	return v;
}

__device__ __forceinline__ float warpMax(float v) {
	for (int o = 16; o > 0; o >>= 1) v = fmaxf(v, __shfl_xor_sync(0xffffffff, v, o));
	return v;
}

// blockSum sums v over the block (256 threads at most a multiple of 32).
__device__ float blockSum(float v, float *sh) {
	int lane = threadIdx.x % 32, wid = threadIdx.x / 32, nw = (blockDim.x + 31) / 32;
	v = warpSum(v);
	__syncthreads();
	if (lane == 0) sh[wid] = v;
	__syncthreads();
	float t = 0;
	for (int i = 0; i < nw; i++) t += sh[i];
	return t;
}

__device__ double blockSumD(double v, double *sh) {
	int lane = threadIdx.x % 32, wid = threadIdx.x / 32, nw = (blockDim.x + 31) / 32;
	for (int o = 16; o > 0; o >>= 1) v += __shfl_xor_sync(0xffffffff, v, o);
	__syncthreads();
	if (lane == 0) sh[wid] = v;
	__syncthreads();
	double t = 0;
	for (int i = 0; i < nw; i++) t += sh[i];
	return t;
}

// layernorm normalises rows of d values, a warp a row (in single
// precision: double is slow on most GPUs, and a row's two passes in single
// lose nothing that shows).
extern "C" __global__ void layernorm(int rows, int d, const float *x, const float *w, const float *b, float *y, float eps) {
	int row = blockIdx.x * (blockDim.x / 32) + threadIdx.x / 32, lane = threadIdx.x % 32;
	if (row >= rows) return;
	const float *r = x + (long long)row * d;
	float *o = y + (long long)row * d;
	float s = 0;
	for (int i = lane; i < d; i += 32) s += r[i];
	for (int k = 16; k > 0; k /= 2) s += __shfl_xor_sync(0xffffffff, s, k);
	float mean = s / d, v = 0;
	for (int i = lane; i < d; i += 32) {
		float dv = r[i] - mean;
		v += dv * dv;
	}
	for (int k = 16; k > 0; k /= 2) v += __shfl_xor_sync(0xffffffff, v, k);
	float inv = rsqrtf(v / d + eps);
	for (int i = lane; i < d; i += 32) o[i] = (r[i] - mean) * inv * w[i] + b[i];
}

// groupnorm: a block a group of cg channels of per values each.
extern "C" __global__ void groupnorm(int cg, long long per, const float *x, const float *w, const float *b, float *y, float eps) {
	__shared__ double sh[32];
	long long n = cg * per, base = (long long)blockIdx.x * n;
	double s = 0;
	for (long long i = threadIdx.x; i < n; i += blockDim.x) s += x[base + i];
	double mean = blockSumD(s, sh) / n;
	double v = 0;
	for (long long i = threadIdx.x; i < n; i += blockDim.x) {
		double dv = x[base + i] - mean;
		v += dv * dv;
	}
	double var = blockSumD(v, sh) / n;
	float inv = (float)(1.0 / sqrt(var + eps));
	float m = (float)mean;
	for (long long i = threadIdx.x; i < n; i += blockDim.x) {
		int c = blockIdx.x * cg + (int)(i / per);
		y[base + i] = (x[base + i] - m) * inv * w[c] + b[c];
	}
}

// softmax: a block a row of n, each value times scale first.
extern "C" __global__ void softmax(int n, float *x, float scale) {
	__shared__ float sh[32];
	float *r = x + (long long)blockIdx.x * n;
	float m = -3.4e38f;
	for (int i = threadIdx.x; i < n; i += blockDim.x) m = fmaxf(m, r[i] * scale);
	m = warpMax(m);
	__syncthreads();
	if (threadIdx.x % 32 == 0) sh[threadIdx.x / 32] = m;
	__syncthreads();
	m = sh[0];
	for (int i = 1; i < (blockDim.x + 31) / 32; i++) m = fmaxf(m, sh[i]);
	float s = 0;
	for (int i = threadIdx.x; i < n; i += blockDim.x) {
		float e = __expf(r[i] * scale - m);
		r[i] = e;
		s += e;
	}
	s = blockSum(s, sh);
	float inv = 1.f / s;
	for (int i = threadIdx.x; i < n; i += blockDim.x) r[i] *= inv;
}

// --- elementwise ------------------------------------------------------

#define EACH(n) for (long long i = (long long)blockIdx.x * blockDim.x + threadIdx.x; i < (n); i += (long long)gridDim.x * blockDim.x)

extern "C" __global__ void gelu(long long n, float *x) {
	EACH(n) {
		float v = x[i];
		x[i] = 0.5f * v * (1.f + erff(v * GELU_K));
	}
}

extern "C" __global__ void relu(long long n, float *x) {
	EACH(n) x[i] = fmaxf(x[i], 0.f);
}

extern "C" __global__ void add(long long n, float *a, const float *b) {
	EACH(n) a[i] += b[i];
}

// addscaled: x += y g, g per channel of d.
extern "C" __global__ void addscaled(long long n, int d, float *x, const float *y, const float *g) {
	EACH(n) x[i] += y[i] * g[i % d];
}

// geglu: out [n, d] = x[:, :d] gelu(x[:, d:]), x [n, 2d].
extern "C" __global__ void geglu(long long n, int d, const float *x, float *out) {
	EACH(n) {
		long long r = i / d;
		int c = i % d;
		float g = x[r * 2 * d + d + c];
		out[i] = x[r * 2 * d + c] * 0.5f * g * (1.f + erff(g * GELU_K));
	}
}

// split3: [n, 3d] rows into their thirds.
extern "C" __global__ void split3(long long n, int d, const float *x, float *q, float *k, float *v) {
	EACH(n) {
		long long r = i / d;
		int c = i % d;
		const float *row = x + r * 3 * d;
		q[i] = row[c];
		k[i] = row[d + c];
		v[i] = row[2 * d + c];
	}
}

// transpose: in [r, c] to out [c, r].
extern "C" __global__ void transpose(int r, int c, const float *in, float *out) {
	__shared__ float t[32][33];
	int x = blockIdx.x * 32 + threadIdx.x, y = blockIdx.y * 32 + threadIdx.y;
	for (int k = 0; k < 32; k += 8)
		if (x < c && y + k < r) t[threadIdx.y + k][threadIdx.x] = in[(long long)(y + k) * c + x];
	__syncthreads();
	x = blockIdx.y * 32 + threadIdx.x;
	y = blockIdx.x * 32 + threadIdx.y;
	for (int k = 0; k < 32; k += 8)
		if (x < r && y + k < c) out[(long long)(y + k) * r + x] = t[threadIdx.x][threadIdx.y + k];
}

// tokens: the encoder's tokens, the class token's and the patches' ([d,
// n] channels first) plus the position embedding: out [1+n, d].
extern "C" __global__ void tokens(int n, int d, const float *patches, const float *cls, const float *pos, float *out) {
	EACH((long long)(n + 1) * d) {
		long long t = i / d;
		int c = i % d;
		float v = t == 0 ? cls[c] : patches[(long long)c * n + t - 1];
		out[i] = v + pos[i];
	}
}

// bilinear: x [c, h, w] to y [c, oh, ow], align_corners.
extern "C" __global__ void bilinear(int c, int h, int w, int oh, int ow, float ry, float rx, const float *x, float *y) {
	EACH((long long)c * oh * ow) {
		int ox = i % ow;
		long long t = i / ow;
		int oy = t % oh, ci = t / oh;
		float fy = oy * ry, fx = ox * rx;
		int y0 = min((int)fy, h - 1), x0 = min((int)fx, w - 1);
		int y1 = min(y0 + 1, h - 1), x1 = min(x0 + 1, w - 1);
		float ly = fy - y0, lx = fx - x0;
		const float *p = x + (long long)ci * h * w;
		float top = p[y0 * w + x0] * (1 - lx) + p[y0 * w + x1] * lx;
		float bot = p[y1 * w + x0] * (1 - lx) + p[y1 * w + x1] * lx;
		y[i] = top * (1 - ly) + bot * ly;
	}
}

// convt: ConvTranspose2d with a kernel k its stride: x [c, h, w], w [c, o,
// k, k], b [o], y [o, h k, w k].
extern "C" __global__ void convt(int c, int h, int w, int o, int k, const float *x, const float *wt, const float *b, float *y) {
	int W = w * k, H = h * k;
	EACH((long long)o * H * W) {
		int X = i % W;
		long long t = i / W;
		int Y = t % H, oi = t / H;
		int yy = Y / k, xx = X / k, ky = Y % k, kx = X % k;
		float s = b ? b[oi] : 0.f;
		for (int ci = 0; ci < c; ci++) s += wt[((ci * o + oi) * k + ky) * k + kx] * x[((long long)ci * h + yy) * w + xx];
		y[i] = s;
	}
}

// temporal: a temporal attention at each of d positions (a block each, a
// thread a channel of c, heads of hd): the query q[p] + peQ, over t
// frames' keys fr.k[f][p] + peK[f] and values fr.v[f][p] + peV[f].
#define MAXFRAMES 32
struct frames {
	const float *k[MAXFRAMES], *v[MAXFRAMES];
};

extern "C" __global__ void temporal(int d, int c, int hd, int t, const float *q, const float *peQ,
	frames fr, const float *peK, const float *peV, float scale, float *out) {
	extern __shared__ float sh[]; // prod[c], scores[heads][t]
	float *prod = sh, *scores = sh + c;
	int p = blockIdx.x, i = threadIdx.x, heads = c / hd;
	float qi = q[(long long)p * c + i] + peQ[i];
	for (int f = 0; f < t; f++) {
		const float *kf = fr.k[f];
		prod[i] = qi * (kf[(long long)p * c + i] + peK[f * c + i]);
		__syncthreads();
		if (i % hd == 0) {
			float s = 0;
			for (int j = 0; j < hd; j++) s += prod[i + j];
			scores[(i / hd) * t + f] = s * scale;
		}
		__syncthreads();
	}
	if (i < heads) {
		float *r = scores + i * t, m = r[0];
		for (int f = 1; f < t; f++) m = fmaxf(m, r[f]);
		float s = 0;
		for (int f = 0; f < t; f++) {
			r[f] = __expf(r[f] - m);
			s += r[f];
		}
		for (int f = 0; f < t; f++) r[f] /= s;
	}
	__syncthreads();
	float o = 0;
	const float *r = scores + (i / hd) * t;
	for (int f = 0; f < t; f++) {
		const float *vf = fr.v[f];
		o += r[f] * (vf[(long long)p * c + i] + peV[f * c + i]);
	}
	out[(long long)p * c + i] = o;
}

// The picture ops: a decoded picture's 8-bit planes as the depth network
// and its refinement take them, as stereo's netInput and RefineInput make
// them on the CPU (the products and sums rounded one by one, unfused, as
// Go does).

// sample8 is a plane's sample at (fx, fy), bilinearly; the plane pw x ph,
// its rows stride apart.
static __device__ float sample8(const unsigned char *p, int stride, int pw, int ph, double fx, double fy) {
	int x0 = (int)fx, y0 = (int)fy;
	int x1 = min(x0 + 1, pw - 1), y1 = min(y0 + 1, ph - 1);
	float ax = (float)(fx - x0), ay = (float)(fy - y0);
	const unsigned char *r0 = p + (long long)y0 * stride, *r1 = p + (long long)y1 * stride;
	float t = __fadd_rn(__fmul_rn(r0[x0], 1 - ax), __fmul_rn(r0[x1], ax));
	float b = __fadd_rn(__fmul_rn(r1[x0], 1 - ax), __fmul_rn(r1[x1], ax));
	return __fadd_rn(__fmul_rn(t, 1 - ay), __fmul_rn(b, ay));
}

// net_input is the active rows [top, top+ah) of a w x h picture (its
// planes Y and, at half size, cb and cr) as the depth network takes them:
// RGB (BT.709, limited range), resized bilinearly to nw x nh, normalised by
// ImageNet's mean and deviation, [3, nh, nw].
extern "C" __global__ void net_input(int w, int h, int top, int ah, int nw, int nh, const unsigned char *Y, int sy,
	const unsigned char *cb, const unsigned char *cr, int sc, float *x) {
	const float mean[3] = {0.485f, 0.456f, 0.406f}, sd[3] = {0.229f, 0.224f, 0.225f};
	int cw = (w + 1) / 2, ch = (h + 1) / 2;
	EACH((long long)nw * nh) {
		int ox = i % nw, oy = i / nw;
		double fy = fmax((oy + 0.5) * ah / nh - 0.5, 0.0) + top;
		double fyc = fmax((fy + 0.5) / 2 - 0.5, 0.0);
		double fx = fmax((ox + 0.5) * w / nw - 0.5, 0.0);
		double fxc = fmax((fx + 0.5) / 2 - 0.5, 0.0);
		float yv = __fmul_rn(1.164f, sample8(Y, sy, w, h, fx, fy) - 16);
		float u = sample8(cb, sc, cw, ch, fxc, fyc) - 128;
		float v = sample8(cr, sc, cw, ch, fxc, fyc) - 128;
		float rgb[3] = {
			__fadd_rn(yv, __fmul_rn(1.793f, v)),
			__fadd_rn(__fadd_rn(yv, -__fmul_rn(0.213f, u)), -__fmul_rn(0.533f, v)),
			__fadd_rn(yv, __fmul_rn(2.112f, u)),
		};
		for (int c = 0; c < 3; c++) {
			float t = fminf(fmaxf(rgb[c] / 255, 0), 1);
			x[((long long)c * nh + oy) * nw + ox] = (t - mean[c]) / sd[c];
		}
	}
}

// moments sums x's values and their squares (rows of cols, stride apart)
// into out[0] and out[1]; one block of 1024 threads.
extern "C" __global__ void __launch_bounds__(1024) moments(int rows, int cols, int stride, const float *x, double *out) {
	__shared__ double s[1024], ss[1024];
	double a = 0, b = 0;
	for (long long i = threadIdx.x; i < (long long)rows * cols; i += blockDim.x) {
		double v = x[(i / cols) * stride + i % cols];
		a += v;
		b = __dadd_rn(b, __dmul_rn(v, v));
	}
	s[threadIdx.x] = a;
	ss[threadIdx.x] = b;
	__syncthreads();
	for (int n = blockDim.x / 2; n > 0; n /= 2) {
		if (threadIdx.x < n) {
			s[threadIdx.x] += s[threadIdx.x + n];
			ss[threadIdx.x] += ss[threadIdx.x + n];
		}
		__syncthreads();
	}
	if (threadIdx.x == 0) {
		out[0] = s[0];
		out[1] = ss[0];
	}
}

// meanSD is the mean and deviation of n values from their moments.
static __device__ void meanSD(const double *mom, double n, double *mean, double *sd) {
	*mean = mom[0] / n;
	*sd = sqrt(fmax(mom[1] / n - __dmul_rn(*mean, *mean), 1e-12));
}

// refine_input is the refinement's input, [4, ph, pw]: the active rows'
// Y (its 2x2 averages; Y from the first active row), cb and cr (from the
// first active row's), all w x h, and the depth d (w x h) standardised by
// its moments mom; the sides padded to pw x ph by repeating the last row
// and column.
extern "C" __global__ void refine_input(int w, int h, int pw, int ph, const unsigned char *Y, int sy,
	const unsigned char *cb, const unsigned char *cr, int sc, const float *d, const double *mom, float *x) {
	double mean, sd;
	meanSD(mom, (double)w * h, &mean, &sd);
	float m32 = (float)mean, inv = (float)(1 / sd);
	long long plane = (long long)pw * ph;
	EACH(plane) {
		int c = i % pw, r = i / pw;
		int sr = min(r, h - 1), sx = min(c, w - 1);
		const unsigned char *p = Y + (long long)(2 * sr) * sy + 2 * sx, *q = p + sy;
		int yb = (p[0] + p[1] + q[0] + q[1] + 2) / 4;
		x[i] = ((float)yb - 16) / 219 - 0.5f;
		x[plane + i] = ((float)cb[(long long)sr * sc + sx] - 128) / 112;
		x[2 * plane + i] = ((float)cr[(long long)sr * sc + sx] - 128) / 112;
		x[3 * plane + i] = (d[(long long)sr * w + sx] - m32) * inv;
	}
}

// renorm is the refinement's output (w x h of rows pw apart, its moments
// got) put back to the depth's mean and deviation (from its moments
// want): d, w x h.
extern "C" __global__ void renorm(int w, int h, int pw, const float *out, const double *want, const double *got, float *d) {
	double mean, sd, m, s;
	meanSD(want, (double)w * h, &mean, &sd);
	meanSD(got, (double)w * h, &m, &s);
	double k = sd / s;
	EACH((long long)w * h) {
		int x = i % w, r = i / w;
		d[i] = (float)__dadd_rn(mean, __dmul_rn((double)out[(long long)r * pw + x] - m, k));
	}
}

// attention_tc is Attention's heads of 64 channels on the tensor cores, in
// one pass (flash attention): a block 64 queries of a head, a warp 16 of
// them; the keys and values 64 at a time through shared memory (half
// precision), the scores' softmax kept running (its maximum and sum a row)
// so that they are never stored. q [nq, d], k and v [nk, d], out [nq, d].
extern "C" __global__ void __launch_bounds__(128) attention_tc(int nq, int nk, int d, const float *q, const float *k,
	const float *v, float *out, float scale) {
	__shared__ __align__(16) __half Ks[64][72]; // [key][channel]
	__shared__ __align__(16) __half Vt[64][72]; // [channel][key]
	const float L2E = 1.4426950408889634f;
	int warp = threadIdx.x / 32, lane = threadIdx.x % 32, g = lane >> 2, c = lane & 3;
	int col = blockIdx.y * 64, q0 = blockIdx.x * 64 + warp * 16;
	auto pack = [](float a, float b) {
		__half2 h = __floats2half2_rn(a, b);
		return *(unsigned *)&h;
	};
	auto Q = [&](int r, int ch) { return r < nq ? q[(long long)r * d + col + ch] : 0.f; };
	unsigned qa[4][4];
#pragma unroll
	for (int ks = 0; ks < 4; ks++) {
		int k0 = ks * 16 + 2 * c;
		qa[ks][0] = pack(Q(q0 + g, k0), Q(q0 + g, k0 + 1));
		qa[ks][1] = pack(Q(q0 + g + 8, k0), Q(q0 + g + 8, k0 + 1));
		qa[ks][2] = pack(Q(q0 + g, k0 + 8), Q(q0 + g, k0 + 9));
		qa[ks][3] = pack(Q(q0 + g + 8, k0 + 8), Q(q0 + g + 8, k0 + 9));
	}
	float o[8][4] = {}, m[2] = {-INFINITY, -INFINITY}, l[2] = {0, 0};
	float sl = scale * L2E;
	for (int t0 = 0; t0 < nk; t0 += 64) {
		__syncthreads();
		for (int i = threadIdx.x; i < 64 * 32; i += 128) {
			int key = i / 32, ch = 2 * (i % 32), kk = t0 + key;
			float2 kv = make_float2(0, 0), vv = make_float2(0, 0);
			if (kk < nk) {
				kv = *(const float2 *)&k[(long long)kk * d + col + ch];
				vv = *(const float2 *)&v[(long long)kk * d + col + ch];
			}
			*(__half2 *)&Ks[key][ch] = __floats2half2_rn(kv.x, kv.y);
			Vt[ch][key] = __float2half_rn(vv.x);
			Vt[ch + 1][key] = __float2half_rn(vv.y);
		}
		__syncthreads();
		float s[8][4] = {};
#pragma unroll
		for (int nt = 0; nt < 8; nt++)
#pragma unroll
			for (int ks = 0; ks < 4; ks++) {
				unsigned b[2] = {*(const unsigned *)&Ks[nt * 8 + g][ks * 16 + 2 * c],
					*(const unsigned *)&Ks[nt * 8 + g][ks * 16 + 2 * c + 8]};
				mma16816(s[nt], qa[ks], b);
			}
		float mx[2] = {-INFINITY, -INFINITY};
#pragma unroll
		for (int nt = 0; nt < 8; nt++)
#pragma unroll
			for (int e = 0; e < 4; e++) {
				int key = t0 + nt * 8 + 2 * c + (e & 1);
				s[nt][e] = key < nk ? s[nt][e] * sl : -INFINITY;
				mx[e >> 1] = fmaxf(mx[e >> 1], s[nt][e]);
			}
		float corr[2];
#pragma unroll
		for (int r = 0; r < 2; r++) {
			mx[r] = fmaxf(mx[r], __shfl_xor_sync(0xffffffff, mx[r], 1));
			mx[r] = fmaxf(mx[r], __shfl_xor_sync(0xffffffff, mx[r], 2));
			float mn = fmaxf(m[r], mx[r]);
			corr[r] = exp2f(m[r] - mn);
			m[r] = mn;
			l[r] *= corr[r];
		}
#pragma unroll
		for (int dt = 0; dt < 8; dt++)
#pragma unroll
			for (int e = 0; e < 4; e++) o[dt][e] *= corr[e >> 1];
#pragma unroll
		for (int nt = 0; nt < 8; nt++)
#pragma unroll
			for (int e = 0; e < 4; e++) {
				s[nt][e] = exp2f(s[nt][e] - m[e >> 1]);
				l[e >> 1] += s[nt][e];
			}
#pragma unroll
		for (int j = 0; j < 4; j++) {
			unsigned a[4] = {pack(s[2 * j][0], s[2 * j][1]), pack(s[2 * j][2], s[2 * j][3]),
				pack(s[2 * j + 1][0], s[2 * j + 1][1]), pack(s[2 * j + 1][2], s[2 * j + 1][3])};
#pragma unroll
			for (int dt = 0; dt < 8; dt++) {
				unsigned b[2] = {*(const unsigned *)&Vt[dt * 8 + g][j * 16 + 2 * c],
					*(const unsigned *)&Vt[dt * 8 + g][j * 16 + 2 * c + 8]};
				mma16816(o[dt], a, b);
			}
		}
	}
#pragma unroll
	for (int r = 0; r < 2; r++) {
		l[r] += __shfl_xor_sync(0xffffffff, l[r], 1);
		l[r] += __shfl_xor_sync(0xffffffff, l[r], 2);
	}
#pragma unroll
	for (int dt = 0; dt < 8; dt++)
#pragma unroll
		for (int r = 0; r < 2; r++) {
			int row = q0 + g + 8 * r;
			if (row < nq) {
				*(float2 *)&out[(long long)row * d + col + dt * 8 + 2 * c] =
					make_float2(o[dt][2 * r] / l[r], o[dt][2 * r + 1] / l[r]);
			}
		}
}

// disparity is the depth's disparity, a w x h picture's: rows [top,
// bottom) a times the depth up (w a row, from top) plus b, kept within
// lim either way; the other rows 0.
extern "C" __global__ void disparity(int w, int h, int top, int bottom, float a, float b, float lim, const float *up, float *d) {
	EACH((long long)w * h) {
		int r = i / w;
		float v = 0;
		if (r >= top && r < bottom) {
			v = fminf(fmaxf(__fadd_rn(__fmul_rn(a, up[i - (long long)top * w]), b), -lim), lim);
		}
		d[i] = v;
	}
}

// eyes renders a picture's two eyes for its disparity d (w x h), as
// stereo.Render does a row at a time: each pair of neighbours a segment,
// rasterised into the targets it covers (the nearer winning), a segment
// stretched more than gap pixels left open, the holes filled from their
// farther side, mirrored. A thread a row: the luma's h rows, then Cb's
// and Cr's ch, the left eye's (sign +0.5) then the right's (-0.5). out is
// both eyes' planes packed (rows w and cw apart); z and v scratch, a
// row's values nrows apart.
extern "C" __global__ void eyes(int w, int h, const unsigned char *Y, int sy, const unsigned char *cb,
	const unsigned char *cr, int sc, const float *d, float gap, unsigned char *out, float *zs, float *vs) {
	int cw = (w + 1) / 2, ch = (h + 1) / 2;
	int per = h + 2 * ch, nrows = 2 * per;
	int row = blockIdx.x * blockDim.x + threadIdx.x;
	if (row >= nrows) {
		return;
	}
	int eye = row / per, r = row % per;
	float sign = eye == 0 ? 0.5f : -0.5f;
	long long eyeSize = (long long)w * h + 2LL * cw * ch;
	unsigned char *o = out + eye * eyeSize;
	const unsigned char *src;
	const float *dr = 0;
	int n, lr = 0;
	bool luma = r < h;
	if (luma) {
		src = Y + (long long)r * sy;
		dr = d + (long long)r * w;
		o += (long long)r * w;
		n = w;
	} else {
		int c = r - h;
		const unsigned char *plane = cb;
		o += (long long)w * h;
		if (c >= ch) {
			c -= ch;
			plane = cr;
			o += (long long)cw * ch;
		}
		src = plane + (long long)c * sc;
		o += (long long)c * cw;
		lr = min(2 * c, h - 1);
		n = cw;
	}
	float *z = zs + row, *v = vs + row;
#define DISP(x) (luma ? dr[x] : d[(long long)lr * w + min(2 * (x), w - 1)] / 2)
#define POS(x) __fadd_rn((float)(x), __fmul_rn(sign, DISP(x)))
#define Z(x) z[(long long)(x) * nrows]
#define V(x) v[(long long)(x) * nrows]
	for (int x = 0; x < n; x++) {
		Z(x) = -INFINITY;
	}
	auto put = [&](int X, float val, float depth) {
		if (X < 0 || X >= n || depth <= Z(X)) {
			return;
		}
		Z(X) = depth;
		V(X) = val;
	};
	float p1 = POS(0), d1 = DISP(0), b = src[0];
	for (int x = 0; x < n - 1; x++) {
		float p0 = p1, d0 = d1, a = b;
		p1 = POS(x + 1);
		d1 = DISP(x + 1);
		b = src[x + 1];
		float lo = fminf(p0, p1), hi = fmaxf(p0, p1);
		if (hi - lo > gap) {
			put((int)round((double)p0), a, d0);
			put((int)round((double)p1), b, d1);
			continue;
		}
		for (int X = (int)ceil((double)lo); (float)X <= hi; X++) {
			float t = 0;
			if (p1 != p0) {
				t = ((float)X - p0) / (p1 - p0);
			}
			put(X, __fadd_rn(a, __fmul_rn(b - a, t)), __fadd_rn(d0, __fmul_rn(d1 - d0, t)));
		}
	}
	put((int)round((double)p1), b, d1);
	// the holes (z still -inf: nothing landed), from their farther side
	for (int x = 0; x < n;) {
		if (Z(x) != -INFINITY) {
			x++;
			continue;
		}
		int a0 = x;
		while (x < n && Z(x) == -INFINITY) {
			x++;
		}
		int b0 = x, left = a0 - 1, right = b0;
		bool useRight;
		if (left < 0 && right >= n) {
			continue;
		} else if (left < 0) {
			useRight = true;
		} else if (right >= n) {
			useRight = false;
		} else {
			useRight = Z(right) < Z(left);
		}
		for (int X = a0; X < b0; X++) {
			int s;
			if (useRight) {
				s = min(right + (b0 - 1 - X), n - 1);
				while (s > right && Z(s) == -INFINITY) {
					s--;
				}
			} else {
				s = max(left - (X - a0), 0);
				while (s < left && Z(s) == -INFINITY) {
					s++;
				}
			}
			V(X) = V(s);
			Z(X) = Z(s);
		}
	}
	for (int X = 0; X < n; X++) {
		o[X] = (unsigned char)fminf(fmaxf(V(X) + 0.5f, 0), 255);
	}
#undef DISP
#undef POS
#undef Z
#undef V
}
