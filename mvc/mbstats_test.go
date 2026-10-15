package mvc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
)

// TestMBStats prints inter prediction statistics of the stream named by
// MVC_STATS: how far motion vectors reach outside the picture, partition
// sizes, reference indexes. A measurement aid, skipped otherwise.
func TestMBStats(t *testing.T) {
	path := os.Getenv("MVC_STATS")
	if path == "" {
		t.Skip("MVC_STATS not set")
	}
	wantView := 0
	if os.Getenv("MVC_STATS_VIEW") == "1" {
		wantView = 1
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var (
		parts             = map[[2]int]int{}
		subs              = map[uint8]int{}
		refs              = map[int]int{}
		maxLeft, maxRight int
		maxTop, maxBottom int
		outside           = map[int]int{} // reach beyond the edge, in 16-pixel steps
		maxMVX, maxMVY    int
		n                 int
	)
	mcHook = func(view, list, ref, px, py, pw, ph int, v mv, picW, picH int) {
		if view != wantView {
			return
		}
		n++
		parts[[2]int{pw, ph}]++
		refs[list*16+ref]++
		x0, y0 := px+int(v.x>>2), py+int(v.y>>2)
		x1, y1 := x0+pw, y0+ph
		over := 0
		if -x0 > over {
			over = -x0
		}
		if -y0 > over {
			over = -y0
		}
		if x1-picW > over {
			over = x1 - picW
		}
		if y1-picH > over {
			over = y1 - picH
		}
		if over > 0 {
			outside[(over+15)/16]++
		}
		maxLeft, maxTop = max(maxLeft, -x0), max(maxTop, -y0)
		maxRight, maxBottom = max(maxRight, x1-picW), max(maxBottom, y1-picH)
		ax, ay := int(v.x), int(v.y)
		if ax < 0 {
			ax = -ax
		}
		if ay < 0 {
			ay = -ay
		}
		maxMVX, maxMVY = max(maxMVX, ax), max(maxMVY, ay)
	}
	subHook = func(shape [4]uint8, direct uint8) {
		for i, sh := range shape {
			if direct>>i&1 == 0 {
				subs[sh]++
			}
		}
	}
	defer func() { mcHook, subHook = nil, nil }()
	d := NewDecoder(Options{Threads: 1})
	r := NewAUReader(f)
	for {
		au, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := d.DecodeAU(au, -1); err != nil {
			t.Fatal(err)
		}
		for {
			if _, ok := d.NextFrame(); !ok {
				break
			}
		}
	}
	_ = d.Flush()
	fmt.Printf("partitions %d: %v\n", n, parts)
	fmt.Printf("sub-8x8 shapes (0=8x8 1=8x4 2=4x8 3=4x4): %v\n", subs)
	fmt.Printf("refs (list*16+idx): %v\n", refs)
	fmt.Printf("max reach outside: left %d top %d right %d bottom %d; by 16px steps: %v\n", maxLeft, maxTop, maxRight, maxBottom, outside)
	fmt.Printf("max |mv| quarter-pel: x %d y %d\n", maxMVX, maxMVY)
}
