//go:build !amd64 || purego

package av1

// dirRow interpolates out[j] between a[j] and a[j+1] by sh/32 (a one
// longer than out), as a directional prediction's row.
func dirRow(out []uint16, a []int32, sh int32) {
	a = a[:len(out)+1]
	for j := range out {
		out[j] = uint16((a[j]*(32-sh) + a[j+1]*sh + 16) >> 5) //nolint:gosec // a sample
	}
}

// dirGather interpolates out[j] between edge[o+il+base[j]] and the next
// sample by sh[j]/32, as a directional prediction's samples from the left
// edge (o its offset in edge, base and sh at least as long as out).
func dirGather(out []uint16, edge []int32, o int, base, sh []int32, il int32) {
	for j := range out {
		b := o + int(il+base[j])
		out[j] = uint16((edge[b]*(32-sh[j]) + edge[b+1]*sh[j] + 16) >> 5) //nolint:gosec // a sample
	}
}
