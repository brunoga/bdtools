//go:build linux && (amd64 || arm64)

package hwenc

import "encoding/binary"

// guidArg passes a GUID by value: on x86-64 System V and arm64 a 16-byte
// struct of integers goes in two registers, low half first.
func guidArg(g []byte) []uintptr {
	return []uintptr{uintptr(binary.LittleEndian.Uint64(g)), uintptr(binary.LittleEndian.Uint64(g[8:]))}
}
