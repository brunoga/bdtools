package mvc

import (
	"os"
	"testing"
	"time"
)

func FuzzDecode(f *testing.F) {
	for _, n := range []string{
		"2d/BA1_Sony_D", "2d/CABA1_Sony_D", "2d/CVSE2_Sony_B", "2d/FRExt1_Panasonic_D",
		"2d/HCAFR1_HHI_C", "2d/CABAST3_Sony_E", "2d/MR1_BT_A", "mvc/MVCDS-4",
	} {
		data, err := os.ReadFile("testdata/conformance/" + n + ".264")
		if err != nil {
			continue
		}
		if len(data) > 6<<10 {
			data = data[:6<<10]
		}
		f.Add(data)
	}
	recoverPanics = false
	maxFrameMbs = 1620 // 720x576
	f.Fuzz(func(t *testing.T, data []byte) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			for _, threads := range []int{1, 4} {
				d := NewDecoder(Options{Threads: threads})
				d.Decode(data)
				d.Flush()
				for {
					fr, ok := d.NextFrame()
					if !ok {
						break
					}
					fr.Release()
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("decoder hang")
		}
	})
}
