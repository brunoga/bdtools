//go:build (darwin || linux) && (amd64 || arm64)

package hwenc

import (
	"fmt"

	"github.com/ebitengine/purego"
)

// openLib loads the first library of names that loads.
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
