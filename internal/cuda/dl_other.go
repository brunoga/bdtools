//go:build !((linux || windows) && (amd64 || arm64))

package cuda

var libNames []string

func openLib(...string) (uintptr, error) { return 0, ErrUnavailable }

func sym(uintptr, string) (uintptr, error) { return 0, ErrUnavailable }

func call(uintptr, ...uintptr) uintptr { return 1 }
