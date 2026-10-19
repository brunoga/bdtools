//go:build !amd64 || purego

package av1

func gram(y []int16, n, k int, g *[17 * 17]int64) { gramGo(y, n, k, g) }

func dot32(a, b []int32) int64 { return dot32Go(a, b) }
