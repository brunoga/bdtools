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

// layernorm: a block a row of d.
extern "C" __global__ void layernorm(int d, const float *x, const float *w, const float *b, float *y, float eps) {
	__shared__ double sh[32];
	const float *r = x + (long long)blockIdx.x * d;
	float *o = y + (long long)blockIdx.x * d;
	double s = 0;
	for (int i = threadIdx.x; i < d; i += blockDim.x) s += r[i];
	double mean = blockSumD(s, sh) / d;
	double v = 0;
	for (int i = threadIdx.x; i < d; i += blockDim.x) {
		double dv = r[i] - mean;
		v += dv * dv;
	}
	double var = blockSumD(v, sh) / d;
	float inv = (float)(1.0 / sqrt(var + eps));
	float m = (float)mean;
	for (int i = threadIdx.x; i < d; i += blockDim.x) o[i] = (r[i] - m) * inv * w[i] + b[i];
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
