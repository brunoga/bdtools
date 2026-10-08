//go:build amd64 && !purego

package mvc

import (
	"testing"
	"unsafe"
)

func TestDeblockLayout(t *testing.T) {
	var m mbInfo
	var sp sliceParams
	var a deblockArgs
	if unsafe.Offsetof(m.flags) != 0 || unsafe.Offsetof(m.qp) != 3 || unsafe.Offsetof(m.nzMask) != 12 || unsafe.Offsetof(m.mvEdges) != 14 {
		t.Fatal("mbInfo layout changed; update deblock_ctl_amd64.s")
	}
	if unsafe.Offsetof(sp.alphaOffset) != 8 || unsafe.Offsetof(sp.betaOffset) != 16 || unsafe.Offsetof(sp.chromaQPOffset) != 24 {
		t.Fatal("sliceParams layout changed; update deblock_ctl_amd64.s")
	}
	if unsafe.Offsetof(a.sp) != 24 || unsafe.Offsetof(a.y) != 32 || unsafe.Offsetof(a.stY) != 56 || unsafe.Offsetof(a.refs0) != 72 || unsafe.Offsetof(a.st4) != 104 || unsafe.Offsetof(a.chroma) != 112 {
		t.Fatal("deblockArgs layout changed; update deblock_ctl_amd64.s")
	}
}
