package mkv

import (
	"io"
	"testing"
	"time"
)

// annexBUnits splits an Annex B HEVC stream into access units: each starts
// at a parameter set, a delimiter or a picture's first slice segment met
// after a picture.
func annexBUnits(b []byte) [][]byte {
	nals, at := splitNALsAt(b)
	var aus [][]byte
	start, seen := 0, false
	for i, n := range nals {
		t := n[0] >> 1 & 0x3f
		first := t < 32 && n[2]&0x80 != 0
		if seen && (first || t >= 32 && t <= 35) {
			aus = append(aus, b[start:at[i]])
			start, seen = at[i], false
		}
		seen = seen || t < 32
	}
	return append(aus, b[start:])
}

// Keys sort a stream's pictures as their presentation times do, through
// an open GOP's leading pictures and into a second coded video sequence.
func TestHEVCOrder(t *testing.T) {
	b, err := io.ReadAll(fixture(t, "mkv", "bframes.265"))
	if err != nil {
		t.Fatal(err)
	}
	aus := annexBUnits(b)
	v, err := NewVideoSource(fixture(t, "mkv", "bframes.265"), HEVC, 24000, 1001, 1)
	if err != nil {
		t.Fatal(err)
	}
	var pts []time.Duration
	for {
		f, err := v.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		pts = append(pts, f.PTS)
	}
	if len(aus) != len(pts) {
		t.Fatalf("%d access units, %d frames", len(aus), len(pts))
	}
	var o HEVCOrder
	var keys []int64
	for range 2 { // the stream twice: its IDR starts a second sequence
		for _, au := range aus {
			k, err := o.Key(au)
			if err != nil {
				t.Fatal(err)
			}
			keys = append(keys, k)
		}
	}
	n := len(aus)
	for i := range keys {
		for j := range keys {
			want := i/n < j/n || i/n == j/n && pts[i%n] < pts[j%n]
			if (keys[i] < keys[j]) != want {
				t.Fatalf("access units %d and %d: keys %d, %d out of display order", i, j, keys[i], keys[j])
			}
		}
	}
	if _, err := o.Key(aus[0][:4]); err == nil {
		t.Error("an access unit without a picture took a key")
	}
}
