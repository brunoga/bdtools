package stereo

import "testing"

// A 1080p picture's two eyes.
func BenchmarkPair1080p(b *testing.B) {
	f, d := scene(1920, 1080)
	b.ResetTimer()
	for range b.N {
		Pair(f, d)
	}
}
