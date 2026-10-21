//go:build linux && (amd64 || arm64)

package cuda

import (
	"fmt"

	"github.com/ebitengine/purego"
)

var libNames = []string{"libcuda.so.1", "libcuda.so"}

func openLib(names ...string) (uintptr, error) {
	var last error
	for _, n := range names {
		h, err := purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil {
			return h, nil
		}
		last = err
	}
	return 0, fmt.Errorf("%w: %v", ErrUnavailable, last)
}

func sym(lib uintptr, name string) (uintptr, error) {
	p, err := purego.Dlsym(lib, name)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %v", ErrUnavailable, name, err)
	}
	return p, nil
}

func call(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}
