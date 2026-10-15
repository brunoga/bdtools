package mvc

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"
)

// TestAUReader checks that splitting into access units and decoding each
// with DecodeAU gives the same output as decoding the whole stream.
func TestAUReader(t *testing.T) {
	for _, n := range []string{"mvc/MVCDS-4", "2d/CVSE2_Sony_B", "2d/MR1_BT_A", "mvc/MVCRP_2"} {
		data, err := os.ReadFile("../testdata/conformance/" + n + ".264")
		if err != nil {
			t.Skip(err)
		}
		_, wantB, wantD, _, _ := decodeFile(data, Options{})
		r := NewAUReader(bytes.NewReader(data))
		d := NewDecoder(Options{})
		gotB, gotD := newHash128(), newHash128()
		drain := func() {
			for {
				f, ok := d.NextFrame()
				if !ok {
					return
				}
				gotB.frame(f.Base)
				if f.Dependent != nil {
					gotD.frame(f.Dependent)
				}
				f.Release()
			}
		}
		aus := 0
		var total int
		for {
			au, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			aus++
			total += len(au)
			d.DecodeAU(au, int64(aus))
			drain()
		}
		d.Flush()
		drain()
		if total != len(data) {
			t.Errorf("%s: split %d bytes, want %d", n, total, len(data))
		}
		if gotB != wantB || gotD != wantD {
			t.Errorf("%s: output differs after AU splitting (%d AUs)", n, aus)
		}
	}
}

// An encoder writing access units into a pipe gets each one back as soon
// as the next begins: the reader must not wait for a buffer's worth of
// input.
func TestAUReaderReturnsAccessUnitsAsPiped(t *testing.T) {
	pr, pw := io.Pipe()
	au := func(frameNum byte) []byte {
		// one P slice: first_mb_in_slice 0, slice_type 0, pps 0, frame_num
		return []byte{0, 0, 1, 0x41, 0xe0 | frameNum>>3, frameNum<<5 | 0x08, 0x80}
	}
	go func() {
		for i := range byte(3) {
			if _, err := pw.Write(au(i)); err != nil {
				return
			}
		}
	}()
	r := NewAUReader(pr)
	got := make(chan []byte, 1)
	go func() {
		a, err := r.Next()
		if err != nil {
			t.Error(err)
		}
		got <- a
	}()
	select {
	case a := <-got:
		if len(a) != 7 || a[3] != 0x41 {
			t.Errorf("first access unit = % x", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first access unit was not returned once the second had been written")
	}
	_ = pw.Close()
}
