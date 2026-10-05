package m2ts

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "testdata", "bluray", "folder", "BDMV", "STREAM", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestReaderProgramAndPES(t *testing.T) {
	r := NewReader(openFixture(t, "00000.m2ts"))
	prog, err := r.ReadProgram()
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint16]byte{0x1011: TypeAVC, 0x1100: TypeAC3, 0x1101: TypeLPCM, 0x1102: TypeEAC3}
	for pid, typ := range want {
		s, ok := prog.Stream(pid)
		if !ok || s.Type != typ {
			t.Errorf("PID %04x: %+v, want type %02x", pid, s, typ)
		}
	}
	counts := map[uint16]int{}
	var firstVideo PES
	for {
		p, err := r.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if p.PID == 0x1011 && counts[p.PID] == 0 {
			firstVideo = p
		}
		counts[p.PID]++
		if p.PID == 0x1011 && !bytes.HasPrefix(p.Payload, []byte{0, 0, 0, 1, 9}) {
			counts[0]++ // tsMuxeR ends the stream with an SEI-only PES
		}
	}
	if counts[0x1011]-counts[0] != 9 {
		t.Errorf("%d picture PES, want one per picture (9)", counts[0x1011]-counts[0])
	}
	if counts[0x1100] == 0 || counts[0x1101] == 0 || counts[0x1102] == 0 {
		t.Errorf("audio PES counts %v", counts)
	}
	if !bytes.HasPrefix(firstVideo.Payload, []byte{0, 0, 0, 1, 9}) || firstVideo.PTS < 0 || firstVideo.DTS > firstVideo.PTS {
		t.Errorf("first picture: pts %d dts %d % x", firstVideo.PTS, firstVideo.DTS, firstVideo.Payload[:8])
	}
}

func TestSelectLimitsPES(t *testing.T) {
	r := NewReader(openFixture(t, "00000.m2ts"))
	r.Select(0x1100)
	for {
		p, err := r.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if p.PID != 0x1100 {
			t.Fatalf("got PID %04x", p.PID)
		}
	}
	if r.Program() == nil {
		t.Error("the tables are read regardless of the selection")
	}
}

// The tables a remux writes must read back as the program they describe.
func TestSectionRoundTrip(t *testing.T) {
	prog := &Program{Number: 1, PCRPID: 0x1001, Version: 3, Info: []byte{0x05, 0x04, 'H', 'D', 'M', 'V'},
		Streams: []ProgramStream{{PID: 0x1011, Type: TypeAVC}, {PID: 0x1100, Type: TypeAC3, Descriptors: []byte{0x0a, 4, 'e', 'n', 'g', 0}}}}
	var ts []byte
	for _, t := range []struct {
		pid uint16
		sec []byte
	}{{0, Section(0, 0, 0, PATBody(1, 0x100))}, {0x100, Section(2, 1, 3, PMTBody(prog))}} {
		pkt := make([]byte, 188)
		pkt[0], pkt[1], pkt[2], pkt[3] = 0x47, 0x40|byte(t.pid>>8), byte(t.pid), 0x10
		pkt[4] = 0
		n := copy(pkt[5:], t.sec)
		for i := 5 + n; i < 188; i++ {
			pkt[i] = 0xff
		}
		ts = append(ts, pkt...)
	}
	ts = append(ts, make([]byte, 188*2)...) // padding so the size can be detected
	for i := 2; i < 4; i++ {
		ts[i*188] = 0x47
		ts[i*188+1], ts[i*188+2] = 0x1f, 0xff
	}
	r := NewReader(bytes.NewReader(ts))
	got, err := r.ReadProgram()
	if err != nil {
		t.Fatal(err)
	}
	if got.PCRPID != 0x1001 || got.Version != 3 || !bytes.Equal(got.Info, prog.Info) || len(got.Streams) != 2 ||
		got.Streams[1].Lang != "eng" {
		t.Errorf("read back %+v", got)
	}
}

func FuzzReader(f *testing.F) {
	b, err := os.ReadFile(filepath.Join("..", "testdata", "bluray", "folder", "BDMV", "STREAM", "00001.m2ts"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(b[:192*40])
	f.Fuzz(func(t *testing.T, b []byte) {
		r := NewReader(bytes.NewReader(b))
		for i := 0; i < 1000; i++ {
			if _, err := r.Next(); err != nil {
				break
			}
		}
	})
}
