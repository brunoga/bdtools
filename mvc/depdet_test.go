package mvc

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
)

// The dependent view is the same however its pictures' work falls on the
// CPUs: two encodes of a stream are one.
func TestDepWriterIsDeterministic(t *testing.T) {
	raw, err := os.ReadFile("../testdata/mvc-source/mvc_base.264")
	if err != nil {
		t.Skip(err)
	}
	var data []byte
	if err := forEachNAL(raw, func(nal []byte) error {
		if nal[0]&31 != nalPrefix {
			data = append(append(data, 0, 0, 0, 1), nal...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srcs, _, _ := depTestSources(t, data)
	encode := func() []byte {
		dw := NewDepWriter(28)
		r := NewAUReader(bytes.NewReader(data))
		var out bytes.Buffer
		pick := func(poc int32, _ bool) (*DepSource, error) { return srcs[poc], nil }
		for {
			au, err := r.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			maus, err := dw.Submit(au, pick)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range maus {
				out.Write(m)
			}
		}
		maus, err := dw.Flush()
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range maus {
			out.Write(m)
		}
		return out.Bytes()
	}
	a := encode()
	for i := range 4 {
		if b := encode(); !bytes.Equal(a, b) {
			t.Fatalf("encode %d: %d bytes, the first %d", i+2, len(b), len(a))
		}
	}
}
