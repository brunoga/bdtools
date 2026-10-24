// Package cuda runs compute kernels on an NVIDIA GPU through the CUDA
// driver API, loaded at run time (no cgo, no CUDA toolkit: the driver
// compiles the kernels' PTX for the GPU it has).
//
// A Context is the GPU's: device memory, modules of kernels, launches,
// all in order on the context's one stream. Its methods may be called
// from any goroutine, one at a time (the context is made current on the
// calling thread for each).
package cuda

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"unsafe"
)

// ErrUnavailable says there is no CUDA here: no driver, no NVIDIA GPU, or
// a system the package does not run on.
var ErrUnavailable = errors.New("cuda: not available")

type api struct {
	cuInit, cuDeviceGet, cuDeviceGetName, cuDeviceTotalMem uintptr
	cuCtxCreate, cuCtxDestroy, cuCtxPush, cuCtxPop         uintptr
	cuCtxSynchronize                                       uintptr
	cuModuleLoadData, cuModuleUnload, cuModuleGetFunction  uintptr
	cuLaunchKernel                                         uintptr
	cuMemAlloc, cuMemFree, cuMemcpyHtoD, cuMemcpyDtoH      uintptr
	cuMemcpyDtoD, cuMemsetD32                              uintptr
	cuGetErrorString, cuDeviceGetAttribute                 uintptr
}

var (
	loadOnce sync.Once
	loaded   *api
	loadErr  error
)

func load() (*api, error) {
	loadOnce.Do(func() {
		lib, err := openLib(libNames...)
		if err != nil {
			loadErr = err
			return
		}
		a := &api{}
		for _, f := range []struct {
			p    *uintptr
			name string
		}{
			{&a.cuInit, "cuInit"}, {&a.cuDeviceGet, "cuDeviceGet"}, {&a.cuDeviceGetName, "cuDeviceGetName"},
			{&a.cuDeviceTotalMem, "cuDeviceTotalMem_v2"},
			{&a.cuDeviceGetAttribute, "cuDeviceGetAttribute"},
			{&a.cuCtxCreate, "cuCtxCreate_v2"}, {&a.cuCtxDestroy, "cuCtxDestroy_v2"},
			{&a.cuCtxPush, "cuCtxPushCurrent_v2"}, {&a.cuCtxPop, "cuCtxPopCurrent_v2"},
			{&a.cuCtxSynchronize, "cuCtxSynchronize"},
			{&a.cuModuleLoadData, "cuModuleLoadData"}, {&a.cuModuleUnload, "cuModuleUnload"},
			{&a.cuModuleGetFunction, "cuModuleGetFunction"}, {&a.cuLaunchKernel, "cuLaunchKernel"},
			{&a.cuMemAlloc, "cuMemAlloc_v2"}, {&a.cuMemFree, "cuMemFree_v2"},
			{&a.cuMemcpyHtoD, "cuMemcpyHtoD_v2"}, {&a.cuMemcpyDtoH, "cuMemcpyDtoH_v2"},
			{&a.cuMemcpyDtoD, "cuMemcpyDtoD_v2"}, {&a.cuMemsetD32, "cuMemsetD32_v2"},
			{&a.cuGetErrorString, "cuGetErrorString"},
		} {
			if *f.p, err = sym(lib, f.name); err != nil {
				loadErr = err
				return
			}
		}
		if r := call(a.cuInit, 0); r != 0 {
			loadErr = fmt.Errorf("%w: cuInit: %d (no NVIDIA GPU?)", ErrUnavailable, r)
			return
		}
		loaded = a
	})
	return loaded, loadErr
}

// Context is a CUDA context on a GPU.
type Context struct {
	a    *api
	ctx  uintptr
	Name string // the GPU's
	Mem  int64  // its memory, in bytes
	SMs  int    // its multiprocessors
}

// New makes a context on the first GPU.
func New() (*Context, error) {
	a, err := load()
	if err != nil {
		return nil, err
	}
	var dev int32
	if r := call(a.cuDeviceGet, uintptr(unsafe.Pointer(&dev)), 0); r != 0 {
		return nil, fmt.Errorf("%w: cuDeviceGet: %d", ErrUnavailable, r)
	}
	c := &Context{a: a}
	name := make([]byte, 256)
	if call(a.cuDeviceGetName, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)), uintptr(dev)) == 0 {
		c.Name = cstring(name)
	}
	var mem uint64
	if call(a.cuDeviceTotalMem, uintptr(unsafe.Pointer(&mem)), uintptr(dev)) == 0 {
		c.Mem = int64(mem) //nolint:gosec // a GPU's memory
	}
	var sms int32
	const multiprocessors = 16 // CU_DEVICE_ATTRIBUTE_MULTIPROCESSOR_COUNT
	if call(a.cuDeviceGetAttribute, uintptr(unsafe.Pointer(&sms)), multiprocessors, uintptr(dev)) == 0 {
		c.SMs = int(sms)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r := call(a.cuCtxCreate, uintptr(unsafe.Pointer(&c.ctx)), 0, uintptr(dev)); r != 0 {
		return nil, c.err("cuCtxCreate", r)
	}
	// cuCtxCreate makes it current: let it go, each call pushes it
	var prev uintptr
	call(a.cuCtxPop, uintptr(unsafe.Pointer(&prev)))
	return c, nil
}

// Close frees the context and everything in it.
func (c *Context) Close() {
	if c.ctx != 0 {
		call(c.a.cuCtxDestroy, c.ctx)
		c.ctx = 0
	}
}

// do runs f with the context current on the calling thread.
func (c *Context) do(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r := call(c.a.cuCtxPush, c.ctx); r != 0 {
		return c.err("cuCtxPushCurrent", r)
	}
	err := f()
	var prev uintptr
	call(c.a.cuCtxPop, uintptr(unsafe.Pointer(&prev)))
	return err
}

func (c *Context) err(what string, r uintptr) error {
	var s *byte
	if c.a != nil && call(c.a.cuGetErrorString, r, uintptr(unsafe.Pointer(&s))) == 0 && s != nil {
		return fmt.Errorf("cuda: %s: %s (%d)", what, cstringAt(s), r)
	}
	return fmt.Errorf("cuda: %s: error %d", what, r)
}

// Buffer is device memory.
type Buffer struct {
	ptr  uintptr
	Size int // bytes
}

// Ptr is the buffer's device address.
func (b *Buffer) Ptr() uintptr { return b.ptr }

// Alloc allocates n bytes of device memory.
func (c *Context) Alloc(n int) (*Buffer, error) {
	b := &Buffer{Size: n}
	err := c.do(func() error {
		if r := call(c.a.cuMemAlloc, uintptr(unsafe.Pointer(&b.ptr)), uintptr(max(n, 4))); r != 0 {
			return c.err(fmt.Sprintf("cuMemAlloc %d bytes", n), r)
		}
		return nil
	})
	return b, err
}

// Free frees a buffer.
func (c *Context) Free(b *Buffer) {
	if b == nil || b.ptr == 0 {
		return
	}
	_ = c.do(func() error { call(c.a.cuMemFree, b.ptr); return nil })
	b.ptr = 0
}

// Upload copies src to the start of dst.
func (c *Context) Upload(dst *Buffer, src []float32) error {
	if len(src) == 0 {
		return nil
	}
	if 4*len(src) > dst.Size {
		return fmt.Errorf("cuda: upload of %d bytes to a buffer of %d", 4*len(src), dst.Size)
	}
	return c.do(func() error {
		r := call(c.a.cuMemcpyHtoD, dst.ptr, uintptr(unsafe.Pointer(&src[0])), uintptr(4*len(src)))
		runtime.KeepAlive(src)
		if r != 0 {
			return c.err("cuMemcpyHtoD", r)
		}
		return nil
	})
}

// UploadBytes copies src to dst at byte offset off.
func (c *Context) UploadBytes(dst *Buffer, off int, src []byte) error {
	if len(src) == 0 {
		return nil
	}
	if off < 0 || off+len(src) > dst.Size {
		return fmt.Errorf("cuda: upload of %d bytes at %d to a buffer of %d", len(src), off, dst.Size)
	}
	return c.do(func() error {
		r := call(c.a.cuMemcpyHtoD, dst.ptr+uintptr(off), uintptr(unsafe.Pointer(&src[0])), uintptr(len(src)))
		runtime.KeepAlive(src)
		if r != 0 {
			return c.err("cuMemcpyHtoD", r)
		}
		return nil
	})
}

// UploadU64 copies 64-bit values (device addresses, say) to the start of
// dst.
func (c *Context) UploadU64(dst *Buffer, src []uint64) error {
	if len(src) == 0 {
		return nil
	}
	if 8*len(src) > dst.Size {
		return fmt.Errorf("cuda: upload of %d bytes to a buffer of %d", 8*len(src), dst.Size)
	}
	return c.do(func() error {
		r := call(c.a.cuMemcpyHtoD, dst.ptr, uintptr(unsafe.Pointer(&src[0])), uintptr(8*len(src)))
		runtime.KeepAlive(src)
		if r != 0 {
			return c.err("cuMemcpyHtoD", r)
		}
		return nil
	})
}

// Download copies the start of src to dst (once the kernels before have
// run).
func (c *Context) Download(dst []float32, src *Buffer) error {
	if len(dst) == 0 {
		return nil
	}
	if 4*len(dst) > src.Size {
		return fmt.Errorf("cuda: download of %d bytes from a buffer of %d", 4*len(dst), src.Size)
	}
	return c.do(func() error {
		r := call(c.a.cuMemcpyDtoH, uintptr(unsafe.Pointer(&dst[0])), src.ptr, uintptr(4*len(dst)))
		runtime.KeepAlive(dst)
		if r != 0 {
			return c.err("cuMemcpyDtoH", r)
		}
		return nil
	})
}

// DownloadBytes copies the start of src to dst (once the kernels before
// have run).
func (c *Context) DownloadBytes(dst []byte, src *Buffer) error {
	if len(dst) == 0 {
		return nil
	}
	if len(dst) > src.Size {
		return fmt.Errorf("cuda: download of %d bytes from a buffer of %d", len(dst), src.Size)
	}
	return c.do(func() error {
		r := call(c.a.cuMemcpyDtoH, uintptr(unsafe.Pointer(&dst[0])), src.ptr, uintptr(len(dst)))
		runtime.KeepAlive(dst)
		if r != 0 {
			return c.err("cuMemcpyDtoH", r)
		}
		return nil
	})
}

// Copy copies n bytes from src (at byte offset so) to dst (at do).
func (c *Context) Copy(dst *Buffer, do int, src *Buffer, so, n int) error {
	if n == 0 {
		return nil
	}
	if do+n > dst.Size || so+n > src.Size {
		return fmt.Errorf("cuda: a copy of %d bytes out of its buffers", n)
	}
	return c.do(func() error {
		if r := call(c.a.cuMemcpyDtoD, dst.ptr+uintptr(do), src.ptr+uintptr(so), uintptr(n)); r != 0 {
			return c.err("cuMemcpyDtoD", r)
		}
		return nil
	})
}

// Zero clears a buffer.
func (c *Context) Zero(b *Buffer) error {
	return c.do(func() error {
		if r := call(c.a.cuMemsetD32, b.ptr, 0, uintptr(b.Size/4)); r != 0 {
			return c.err("cuMemsetD32", r)
		}
		return nil
	})
}

// Sync waits for every kernel launched to have run, and reports a
// failure of any.
func (c *Context) Sync() error {
	return c.do(func() error {
		if r := call(c.a.cuCtxSynchronize); r != 0 {
			return c.err("a kernel", r)
		}
		return nil
	})
}

// Module is a loaded module of kernels.
type Module struct {
	c   *Context
	mod uintptr
}

// Load loads a module from its PTX (or a cubin), compiled by the driver
// for the GPU.
func (c *Context) Load(image []byte) (*Module, error) {
	img := append(append([]byte(nil), image...), 0)
	m := &Module{c: c}
	err := c.do(func() error {
		r := call(c.a.cuModuleLoadData, uintptr(unsafe.Pointer(&m.mod)), uintptr(unsafe.Pointer(&img[0])))
		runtime.KeepAlive(img)
		if r != 0 {
			return c.err("cuModuleLoadData", r)
		}
		return nil
	})
	return m, err
}

// Kernel is a module's kernel.
type Kernel struct {
	c    *Context
	f    uintptr
	Name string
}

// Kernel finds a kernel by its name.
func (m *Module) Kernel(name string) (*Kernel, error) {
	k := &Kernel{c: m.c, Name: name}
	cname := append([]byte(name), 0)
	err := m.c.do(func() error {
		r := call(m.c.a.cuModuleGetFunction, uintptr(unsafe.Pointer(&k.f)), m.mod, uintptr(unsafe.Pointer(&cname[0])))
		runtime.KeepAlive(cname)
		if r != 0 {
			return m.c.err("cuModuleGetFunction "+name, r)
		}
		return nil
	})
	return k, err
}

// Dim is a grid's or a block's size.
type Dim struct{ X, Y, Z int }

// Launch queues a kernel: its grid of blocks, each of block threads, with
// shared bytes of dynamic shared memory. The arguments are *Buffer (its
// address), uintptr (an address), int (as a 32-bit int), int32, uint32,
// float32, float64, int64 or []uint64 (a struct of so many 64-bit fields, passed
// by value: device addresses, say), in the kernel's order.
func (k *Kernel) Launch(grid, block Dim, shared int, args ...any) error {
	vals := make([]uint64, len(args))
	var blobs [][]uint64
	blobAt := map[int]int{}
	for i, a := range args {
		switch v := a.(type) {
		case []uint64:
			blobAt[i] = len(blobs)
			blobs = append(blobs, append([]uint64(nil), v...))
		case *Buffer:
			vals[i] = uint64(v.ptr)
		case uintptr:
			vals[i] = uint64(v)
		case int:
			vals[i] = uint64(uint32(int32(v))) //nolint:gosec // a kernel's 32-bit int
		case int32:
			vals[i] = uint64(uint32(v)) //nolint:gosec // its bits
		case uint32:
			vals[i] = uint64(v)
		case float32:
			vals[i] = uint64(math.Float32bits(v))
		case float64:
			vals[i] = math.Float64bits(v)
		case int64:
			vals[i] = uint64(v) //nolint:gosec // its bits
		default:
			return fmt.Errorf("cuda: %s: an argument of type %T", k.Name, a)
		}
	}
	// kernelParams: a pointer to each argument's value (little-endian, so
	// a 32-bit argument is the low half of its slot)
	params := make([]uintptr, len(vals))
	var pin runtime.Pinner
	defer pin.Unpin()
	for i := range vals {
		if b, ok := blobAt[i]; ok && len(blobs[b]) > 0 {
			pin.Pin(&blobs[b][0])
			params[i] = uintptr(unsafe.Pointer(&blobs[b][0]))
			continue
		}
		params[i] = uintptr(unsafe.Pointer(&vals[i]))
	}
	var pp uintptr
	if len(params) > 0 {
		pp = uintptr(unsafe.Pointer(&params[0]))
	}
	if len(vals) > 0 {
		pin.Pin(&vals[0])
		pin.Pin(&params[0])
	}
	g := func(v int) uintptr { return uintptr(max(v, 1)) }
	return k.c.do(func() error {
		r := call(k.c.a.cuLaunchKernel, k.f, g(grid.X), g(grid.Y), g(grid.Z), g(block.X), g(block.Y), g(block.Z),
			uintptr(shared), 0, pp, 0)
		runtime.KeepAlive(vals)
		runtime.KeepAlive(params)
		runtime.KeepAlive(blobs)
		if r != 0 {
			return k.c.err("cuLaunchKernel "+k.Name, r)
		}
		return nil
	})
}

func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func cstringAt(p *byte) string {
	var b []byte
	for q := unsafe.Pointer(p); *(*byte)(q) != 0; q = unsafe.Add(q, 1) {
		b = append(b, *(*byte)(q))
	}
	return string(b)
}
