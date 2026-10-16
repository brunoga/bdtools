package convert

import (
	"fmt"
	"testing"

	"github.com/brunoga/bdtools/internal/gpu"
	"github.com/brunoga/bdtools/mvc"
)

func BenchmarkSplitFramed(b *testing.B) {
	for _, l := range []Layout{LayoutFullSBS, LayoutHalfSBS, LayoutHalfOU} {
		b.Run(string(l), func(b *testing.B) {
			w, h := 3840, 1080
			if l.halved() {
				w = 1920
			}
			g := &gpu.DecodedPicture{Width: w, Height: h, Depth: 8, Pitch: w, Y: make([]byte, w*h), UV: make([]byte, w*h/2)}
			for b.Loop() {
				splitFramed(picture{gpu: g}, l)
			}
		})
	}
}

func BenchmarkDrawOverUnder(b *testing.B) {
	sf := &mvc.StereoFrame{Base: testFrame(1920, 1080, 0), Dependent: testFrame(1920, 1080, 9)}
	for _, l := range []Layout{LayoutFullOU, LayoutHalfOU, LayoutFullSBS} {
		for _, depth := range []int{8, 10} {
			b.Run(fmt.Sprint(l, depth), func(b *testing.B) {
				bps := 1 + depth/10
				p := &gpu.Picture{Y: make([]byte, 3840*2160*bps), UV: make([]byte, 3840*1080*bps), Pitch: 1920 * bps, Depth: depth}
				if l == LayoutFullSBS {
					p.Pitch = 3840 * bps
				}
				for b.Loop() {
					drawFramed(p, sf, false, l)
				}
			})
		}
	}
}
