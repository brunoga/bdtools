//go:build !amd64 || purego

package av1

func sgrRows(out, u, a, b []int32, i, k, bw, pass int) int { return 0 }

func sgrABRow(sq, sum, a, b []int32, k0, n, nn, s, oneOverN int, sh uint) int { return 0 }
