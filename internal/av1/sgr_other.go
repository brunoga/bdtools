//go:build !amd64 || purego

package av1

func sgrRows(out, u, a, b []int32, i, k, bw, pass int) int { return 0 }
