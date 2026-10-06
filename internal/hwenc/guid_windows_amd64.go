//go:build windows

package hwenc

// guidArg passes a GUID by value: on Windows x64 a 16-byte struct goes by
// reference to a copy.
func guidArg(g []byte) []uintptr { return []uintptr{gp(g)} }
