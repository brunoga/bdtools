//go:build (darwin || linux || windows) && (amd64 || arm64)

package gpu

import "unsafe"

// cptr turns an address returned by C into a pointer. The memory is the
// driver's, outside the Go heap.
func cptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// cbytes views n bytes of C memory.
func cbytes(p uintptr, n int) []byte {
	if p == 0 || n <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(cptr(p)), n)
}
