//go:build windows && (amd64 || arm64)

package gpu

import "sync"

// guidCopies keeps the GUIDs C sees by address alive: it gets only their
// address, as an integer. One copy per GUID, and a callee may change it.
var guidCopies sync.Map

// gp is a pointer to a GUID, for an argument passed as REFGUID or REFIID.
func gp(g []byte) uintptr {
	c, _ := guidCopies.LoadOrStore(string(g), func() cstruct { s := newStruct(16); copy(s, g); return s }())
	return c.(cstruct).ptr() //nolint:forcetypeassert // only cstructs are stored
}
