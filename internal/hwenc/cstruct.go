//go:build (linux || windows) && (amd64 || arm64)

package hwenc

import (
	"encoding/binary"
	"sync/atomic"
	"unsafe"
)

// Output parameters must live in a cstruct too, never in a Go variable whose
// address is passed as a uintptr: the compiler cannot see the C side write
// through it, and may keep using the old value.
//
// cstruct is a zeroed C struct of a known size, written field by field at
// offsets taken from the C headers (see the *_layout.go files), so no Go
// type has to mirror the C layout.
type cstruct []byte

func newStruct(size int) cstruct {
	s := make(cstruct, max(size, 1))[:size]
	// Make it escape: only its address, as an integer, reaches C, so escape
	// analysis would otherwise keep it on the stack and the compiler would
	// read back what it wrote rather than what the driver did. Storing it
	// in a global is what moves it to the heap.
	escapeSink.Store(&s[:1][0])
	return s
}

var escapeSink atomic.Pointer[byte]

func (s cstruct) ptr() uintptr          { return uintptr(unsafe.Pointer(&s[0])) }
func (s cstruct) u32(off int, v uint32) { binary.LittleEndian.PutUint32(s[off:], v) }
func (s cstruct) u64(off int, v uint64) { binary.LittleEndian.PutUint64(s[off:], v) }
func (s cstruct) uptr(off int, v uintptr) {
	binary.LittleEndian.PutUint64(s[off:], uint64(v))
}
func (s cstruct) getU32(off int) uint32   { return binary.LittleEndian.Uint32(s[off:]) }
func (s cstruct) getPtr(off int) uintptr  { return uintptr(binary.LittleEndian.Uint64(s[off:])) }
func (s cstruct) bytes(off int, b []byte) { copy(s[off:], b) }

// cstring reads a NUL-terminated C string.
func cstring(p uintptr) string {
	if p == 0 {
		return ""
	}
	var b []byte
	for i := 0; i < 4096; i++ {
		c := *(*byte)(cptr(p + uintptr(i)))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}

// guid is a Windows-style GUID in its in-memory layout.
func guid(d1 uint32, d2, d3 uint16, d4 [8]byte) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b, d1)
	binary.LittleEndian.PutUint16(b[4:], d2)
	binary.LittleEndian.PutUint16(b[6:], d3)
	copy(b[8:], d4[:])
	return b
}
