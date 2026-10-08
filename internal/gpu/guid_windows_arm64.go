//go:build windows

package gpu

import "encoding/binary"

// guidArg passes a GUID by value: Windows on Arm follows AAPCS64, where a
// 16-byte struct of integers goes in two registers, low half first.
func guidArg(g []byte) []uintptr {
	return []uintptr{uintptr(binary.LittleEndian.Uint64(g)), uintptr(binary.LittleEndian.Uint64(g[8:]))}
}
