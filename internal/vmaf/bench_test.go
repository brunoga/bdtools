package vmaf

import "testing"

func BenchmarkVIF1080(b *testing.B) {
	ref, dis := testFrames(1920, 1080, 8, 1, 1)
	b.ResetTimer()
	for range b.N {
		vifScores(ref[0], dis[0])
	}
}

func BenchmarkSSIM1080(b *testing.B) {
	ref, dis := testFrames(1920, 1080, 8, 1, 1)
	b.ResetTimer()
	for range b.N {
		SSIM(ref[0], dis[0])
	}
}
