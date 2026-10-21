// The test kernel: y[i] = a x[i] + y[i].
// nvcc -ptx -arch=compute_80 -o add.ptx add.cu
extern "C" __global__ void axpy(int n, float a, const float *x, float *y) {
	int i = blockIdx.x * blockDim.x + threadIdx.x;
	if (i < n) y[i] = a * x[i] + y[i];
}
