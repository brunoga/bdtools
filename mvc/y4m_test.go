package mvc

import (
	"bytes"
	"fmt"
	"testing"
)

// y4mFrame is a small frame whose samples are all different.
func y4mFrame(base byte) *Frame {
	f := &Frame{Width: 4, Height: 2, StrideY: 6, StrideC: 3}
	f.Y = make([]byte, 6*2)
	f.Cb, f.Cr = make([]byte, 3), make([]byte, 3)
	for i := range f.Y {
		f.Y[i] = base + byte(i)
	}
	for i := range f.Cb {
		f.Cb[i], f.Cr[i] = base+100+byte(i), base+150+byte(i)
	}
	return f
}

// The 10-bit stream is the 8-bit one with every sample four times its
// value, two bytes little-endian, and says so in its header.
func TestY4MWriter10Bit(t *testing.T) {
	sf := &StereoFrame{Base: y4mFrame(1), Dependent: y4mFrame(60)}
	for _, l := range []Layout{LayoutSideBySide, LayoutTopBottom, LayoutBase, LayoutDependent} {
		var b8, b10 bytes.Buffer
		w8 := NewY4MWriter(&b8, l)
		w10 := NewY4MWriter(&b10, l)
		w10.Depth = 10
		for _, w := range []*Y4MWriter{w8, w10} {
			if err := w.Write(sf); err != nil {
				t.Fatal(err)
			}
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
		}
		h8, f8, _ := bytes.Cut(b8.Bytes(), []byte("FRAME\n"))
		h10, f10, _ := bytes.Cut(b10.Bytes(), []byte("FRAME\n"))
		if !bytes.HasSuffix(h8, []byte(" C420jpeg\n")) || !bytes.HasSuffix(h10, []byte(" C420p10\n")) {
			t.Fatalf("%s: headers %q and %q", l, h8, h10)
		}
		if string(bytes.TrimSuffix(h8, []byte(" C420jpeg\n"))) != string(bytes.TrimSuffix(h10, []byte(" C420p10\n"))) {
			t.Errorf("%s: headers differ beyond the colour space: %q, %q", l, h8, h10)
		}
		if len(f10) != 2*len(f8) {
			t.Fatalf("%s: %d bytes of 10-bit samples for %d 8-bit ones", l, len(f10), len(f8))
		}
		for i, v := range f8 {
			if got, want := int(f10[2*i])|int(f10[2*i+1])<<8, 4*int(v); got != want {
				t.Fatalf("%s: sample %d is %d, want %d", l, i, got, want)
			}
		}
	}
}

// The side-by-side 8-bit frame is the two views' rows side by side, plane
// by plane, with the strides' padding left out.
func TestY4MWriterSideBySide(t *testing.T) {
	a, b := y4mFrame(1), y4mFrame(60)
	var out bytes.Buffer
	w := NewY4MWriter(&out, LayoutSideBySide)
	if err := w.Write(&StereoFrame{Base: a, Dependent: b}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	fmt.Fprintf(&want, "YUV4MPEG2 W8 H2 F24000:1001 Ip A1:1 C420jpeg\nFRAME\n")
	for row := range 2 {
		want.Write(a.Y[row*6 : row*6+4])
		want.Write(b.Y[row*6 : row*6+4])
	}
	want.Write(a.Cb[:2])
	want.Write(b.Cb[:2])
	want.Write(a.Cr[:2])
	want.Write(b.Cr[:2])
	if !bytes.Equal(out.Bytes(), want.Bytes()) {
		t.Errorf("got\n% x\nwant\n% x", out.Bytes(), want.Bytes())
	}
}
