package probe

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/brunoga/bdtools/m2ts"
)

// tsPacket is one 188-byte packet of payload on pid, the payload unit
// starting (a section with a pointer field of 0), padded with 0xff.
func tsPacket(pid uint16, cc byte, section []byte) []byte {
	p := make([]byte, 188)
	p[0] = 0x47
	p[1] = 0x40 | byte(pid>>8)
	p[2] = byte(pid)
	p[3] = 0x10 | cc&0x0f
	p[4] = 0 // pointer field
	n := copy(p[5:], section)
	for i := 5 + n; i < 188; i++ {
		p[i] = 0xff
	}
	return p
}

// nullPacket is a packet on the null PID.
func nullPacket() []byte {
	p := make([]byte, 188)
	p[0], p[1], p[2], p[3] = 0x47, 0x1f, 0xff, 0x10
	for i := 4; i < 188; i++ {
		p[i] = 0xff
	}
	return p
}

// stream is a synthetic transport stream: nulls, the PAT, nulls, the PMT
// of streams, nulls; with Blu-ray's 4-byte header on each packet when
// pktSize is 192.
func stream(pktSize int, streams []m2ts.ProgramStream, nullsBefore, nullsBetween, nullsAfter int) []byte {
	var out bytes.Buffer
	put := func(p []byte) {
		if pktSize == 192 {
			out.Write([]byte{0x00, 0x00, 0x00, 0x00})
		}
		out.Write(p)
	}
	for range nullsBefore {
		put(nullPacket())
	}
	put(tsPacket(0, 0, m2ts.Section(0, 1, 0, m2ts.PATBody(1, 0x100))))
	for range nullsBetween {
		put(nullPacket())
	}
	prog := &m2ts.Program{Number: 1, PCRPID: 0x1011, Streams: streams}
	put(tsPacket(0x100, 0, m2ts.Section(2, 1, 0, m2ts.PMTBody(prog))))
	for range nullsAfter {
		put(nullPacket())
	}
	return out.Bytes()
}

func lang(code string) []byte { return append([]byte{0x0a, 4}, append([]byte(code), 0)...) }

var mvcStreams = []m2ts.ProgramStream{
	{PID: 0x1011, Type: m2ts.TypeAVC},
	{PID: 0x1012, Type: m2ts.TypeMVC},
	{PID: 0x1100, Type: m2ts.TypeDTSHDMA, Lang: "eng", Descriptors: lang("eng")},
	{PID: 0x1101, Type: m2ts.TypeAC3, Lang: "fra", Descriptors: lang("fra")},
	{PID: 0x1200, Type: m2ts.TypePGS, Lang: "eng", Descriptors: lang("eng")},
}

func TestM2TS(t *testing.T) {
	for _, pktSize := range []int{192, 188} {
		b := stream(pktSize, mvcStreams, 3, 5, 20)
		res, err := M2TS(bytes.NewReader(b), int64(len(b)), fmt.Sprintf("mvc-%d", pktSize))
		if err != nil {
			t.Fatalf("%d-byte packets: %v", pktSize, err)
		}
		if res.Kind != KindTransportStream || !res.Is3D || res.Layout != LayoutMVC {
			t.Errorf("%d-byte packets: kind %v, 3D %v, layout %v", pktSize, res.Kind, res.Is3D, res.Layout)
		}
		if len(res.Video) != 2 || res.Video[0].Codec != "H.264" || res.Video[1].Codec != "MVC" || res.Video[0].BitDepth != 8 {
			t.Errorf("%d-byte packets: video %+v", pktSize, res.Video)
		}
		if len(res.Audio) != 2 || res.Audio[0].Codec != "DTS-HD Master Audio" || res.Audio[0].Language != "eng" || res.Audio[1].Codec != "AC3" || res.Audio[1].Language != "fra" {
			t.Errorf("%d-byte packets: audio %+v", pktSize, res.Audio)
		}
		if len(res.Subtitle) != 1 || res.Subtitle[0].Codec != "PGS" || res.Subtitle[0].Language != "eng" {
			t.Errorf("%d-byte packets: subtitles %+v", pktSize, res.Subtitle)
		}
		if res.Duration != 0 {
			t.Errorf("%d-byte packets: a duration %v", pktSize, res.Duration)
		}
	}
}

func TestM2TS2D(t *testing.T) {
	streams := []m2ts.ProgramStream{{PID: 0x1011, Type: m2ts.TypeAVC}, {PID: 0x1100, Type: m2ts.TypeDTS}}
	b := stream(192, streams, 0, 0, 4)
	res, err := M2TS(bytes.NewReader(b), int64(len(b)), "2d")
	if err != nil {
		t.Fatal(err)
	}
	if res.Is3D || res.Layout != Layout2D || len(res.Video) != 1 || res.Video[0].Codec != "H.264" {
		t.Errorf("2D stream: 3D %v, layout %v, video %+v", res.Is3D, res.Layout, res.Video)
	}
	for _, typ := range []byte{m2ts.TypeHEVC, m2ts.TypeVC1} {
		b := stream(192, []m2ts.ProgramStream{{PID: 0x1011, Type: typ}}, 0, 0, 1)
		res, err := M2TS(bytes.NewReader(b), int64(len(b)), "2d")
		if err != nil || res.Is3D || len(res.Video) != 1 {
			t.Errorf("stream type %#x: %v, %+v", typ, err, res)
		}
	}
}

// partialReader has the first n bytes of b; the rest is not there.
type partialReader struct {
	b []byte
	n int
}

var errNotHere = errors.New("the piece is not downloaded")

func (p *partialReader) ReadAt(buf []byte, off int64) (int, error) {
	end := min(p.n, len(p.b))
	if off >= int64(end) {
		return 0, errNotHere
	}
	k := copy(buf, p.b[off:end])
	if k < len(buf) {
		return k, errNotHere
	}
	return k, nil
}

func TestM2TSPartial(t *testing.T) {
	// the PMT lies beyond the first 2,000 packets of 192 bytes
	b := stream(192, mvcStreams, 1, 2000, 10)
	r := &partialReader{b: b, n: 1000 * 192}
	_, err := M2TS(r, int64(len(b)), "partial")
	var missing *MissingDataError
	if !errors.As(err, &missing) {
		t.Fatalf("a partial stream gave %v, not a MissingDataError", err)
	}
	if missing.Offset >= int64(len(b)) || !errors.Is(err, errNotHere) {
		t.Errorf("the missing bytes %d-%d, the error %v", missing.Offset, missing.Offset+missing.Length, err)
	}
	// enough of it: the same answer as the whole
	r = &partialReader{b: b, n: 2100 * 192}
	res, err := M2TS(r, int64(len(b)), "partial")
	if err != nil || !res.Is3D {
		t.Errorf("the first 2,100 packets: %v, %+v", err, res)
	}
}

func TestM2TSMalformed(t *testing.T) {
	for _, b := range [][]byte{
		bytes.Repeat([]byte{0x47, 0x1f, 0xff, 0x10}, 47), // sync bytes, no tables
		bytes.Repeat([]byte{0xab, 0xcd}, 400),            // not a transport stream
		{},
	} {
		if _, err := M2TS(bytes.NewReader(b), int64(len(b)), "bad"); err == nil {
			t.Errorf("%d bytes of nonsense gave no error", len(b))
		}
	}
}
