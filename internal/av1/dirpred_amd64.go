//go:build amd64 && !purego

package av1

//go:noescape
func dirRowAVX2(out *uint16, a *int32, n int, sh int32)

//go:noescape
func dirGatherAVX2(out *uint16, edge *int32, base, sh *int32, il int32, n int)

// dirRow interpolates out[j] between a[j] and a[j+1] by sh/32 (a one
// longer than out), as a directional prediction's row.
func dirRow(out []uint16, a []int32, sh int32) {
	n := 0
	if useAVX2 {
		n = len(out) &^ 7
		if n > 0 {
			_ = a[n]
			dirRowAVX2(&out[0], &a[0], n, sh)
		}
	}
	a = a[n : len(out)+1]
	for j := n; j < len(out); j++ {
		out[j] = uint16((a[j-n]*(32-sh) + a[j-n+1]*sh + 16) >> 5) //nolint:gosec // a sample
	}
}

// dirGather interpolates out[j] between edge[o+il+base[j]] and the next
// sample by sh[j]/32, as a directional prediction's samples from the left
// edge (o its offset in edge, base and sh at least as long as out).
func dirGather(out []uint16, edge []int32, o int, base, sh []int32, il int32) {
	n := 0
	if useAVX2 {
		n = len(out) &^ 7
		if n > 0 {
			dirGatherAVX2(&out[0], &edge[o], &base[0], &sh[0], il, n)
		}
	}
	for j := n; j < len(out); j++ {
		b := o + int(il+base[j])
		out[j] = uint16((edge[b]*(32-sh[j]) + edge[b+1]*sh[j] + 16) >> 5) //nolint:gosec // a sample
	}
}
