package av1

import "testing"

// scenePictures is n moving pictures, the ones from cut on another scene
// (the luma inverted).
func scenePictures(w, h, depth, n, cut int) []*Source {
	var pics []*Source
	maxV := uint16(int(1)<<uint(depth) - 1)
	for i := range n {
		p := noisy(cleanPicture(w, h, depth, i, i/2), depth, 2, uint64(i+1))
		if i >= cut {
			for k, v := range p.Y {
				p.Y[k] = maxV - v
			}
		}
		pics = append(pics, p)
	}
	return pics
}

func TestSceneCuts(t *testing.T) {
	const w, h, depth, n, cut = 96, 64, 8, 12, 7
	pics := scenePictures(w, h, depth, n, cut)
	o := EncoderOptions{Width: w, Height: h, BitDepth: depth, QIndex: 140, FPSNum: 25, FPSDen: 1}
	e, err := NewEncoder(o)
	if err != nil {
		t.Fatal(err)
	}
	encodeAll(t, e, pics)
	for i := 1; i < n; i++ {
		if got := e.isCut(i); got != (i == cut) {
			t.Errorf("picture %d: cut %v", i, got)
		}
	}
	checkDecodes(t, o, pics)
}
