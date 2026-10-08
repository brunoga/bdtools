//go:build windows && (amd64 || arm64)

package gpu

import (
	"fmt"
	"syscall"
)

func openLib(names ...string) (uintptr, error) {
	var last error
	for _, n := range names {
		h, err := syscall.LoadLibrary(n)
		if err == nil {
			return uintptr(h), nil
		}
		last = err
	}
	return 0, fmt.Errorf("%w: %v", ErrUnavailable, last)
}

func sym(lib uintptr, name string) (uintptr, error) {
	p, err := syscall.GetProcAddress(syscall.Handle(lib), name)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %v", ErrUnavailable, name, err)
	}
	return p, nil
}

func call(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, args...)
	return r
}
