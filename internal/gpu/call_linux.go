//go:build linux && (amd64 || arm64)

package gpu

import "github.com/ebitengine/purego"

func call(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}
