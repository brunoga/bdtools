package deint

import (
	"math"
	"os/exec"
	"testing"
)

// Deinterlaced interlaced video is near what ffmpeg's yadif makes of it
// (the same algorithm; the borders are done differently), and far nearer
// the progressive pictures it was made from than the woven frames are.
func TestNearYadif(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	const w, h = 320, 240
	// Progressive pictures at 60 fps, interlaced to 30 fps, top field first.
	src := "testsrc2=s=320x240:r=60:d=1"
	inter, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-f", "lavfi", "-i", src, "-vf", "interlace=scan=tff",
		"-pix_fmt", "yuv420p", "-f", "rawvideo", "-").Output()
	if err != nil {
		t.Skip(err)
	}
	ref, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-f", "lavfi", "-i", src,
		"-vf", "interlace=scan=tff,setfield=tff,yadif=mode=send_frame:parity=tff", "-pix_fmt", "yuv420p", "-f", "rawvideo", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	size := w * h * 3 / 2
	var got [][]byte
	d := New(func(f *Frame) error {
		b := make([]byte, 0, size)
		for p := range 3 {
			pw, ph := w, h
			if p > 0 {
				pw, ph = w/2, h/2
			}
			for y := range ph {
				b = append(b, f.Planes[p][y*f.Strides[p]:y*f.Strides[p]+pw]...)
			}
		}
		got = append(got, b)
		return nil
	})
	for i := 0; (i+1)*size <= len(inter); i++ {
		b := inter[i*size : (i+1)*size]
		f := &Frame{Planes: [3][]byte{b[:w*h], b[w*h : w*h*5/4], b[w*h*5/4:]}, Strides: [3]int{w, w / 2, w / 2},
			Width: w, Height: h, Interlaced: true, TopFieldFirst: true}
		if err := d.Push(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(got)*size != len(ref) {
		t.Fatalf("%d frames, yadif %d", len(got), len(ref)/size)
	}
	worst := math.Inf(1)
	for i, g := range got {
		worst = min(worst, psnr(g[:w*h], ref[i*size:i*size+w*h]))
	}
	t.Logf("%d frames, worst luma %.1f dB from yadif's", len(got), worst)
	if worst < 40 {
		t.Errorf("worst luma %.1f dB from yadif's", worst)
	}
}

func psnr(a, b []byte) float64 {
	var se float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		se += d * d
	}
	if se == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255*float64(len(a))/se)
}
