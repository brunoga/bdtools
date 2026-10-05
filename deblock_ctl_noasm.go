//go:build !amd64 || purego

package mvc

type deblockArgs struct{}

func (fc *frameCtx) deblockMBFast(mbX, mbY int, cur *mbInfo, sp *sliceParams, filterLeft, filterTop bool) bool {
	return false
}
