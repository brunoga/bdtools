//go:build windows && (amd64 || arm64)

package hwenc

import "sync"

// guidCopies keeps the copies guidArg hands out alive: C sees only their
// address, as an integer.
var guidCopies sync.Map

// guidArg passes a GUID by value: on Windows x64 a 16-byte struct goes by
// reference to a copy the callee may change, one per GUID here.
func guidArg(g []byte) []uintptr {
	c, _ := guidCopies.LoadOrStore(string(g), func() cstruct { s := newStruct(16); copy(s, g); return s }())
	return []uintptr{c.(cstruct).ptr()} //nolint:forcetypeassert // only cstructs are stored
}
