package mvc

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// TestAUReader checks that splitting into access units and decoding each
// with DecodeAU gives the same output as decoding the whole stream.
func TestAUReader(t *testing.T) {
	for _, n := range []string{"mvc/MVCDS-4", "2d/CVSE2_Sony_B", "2d/MR1_BT_A", "mvc/MVCRP_2"} {
		data, err := os.ReadFile("testdata/conformance/" + n + ".264")
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
