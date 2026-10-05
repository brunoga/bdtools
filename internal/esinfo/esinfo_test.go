package esinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func sample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bluray", "src", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAC3(t *testing.T) {
	a, ok := ParseAC3(sample(t, "a.ac3"), false)
	if !ok {
		t.Fatal("not parsed")
	}
	if a.Codec != "AC3" || a.Bitrate != 384 || a.SampleRate != 44100 || a.Layout() != "5.1" {
		t.Errorf("got %+v, want AC3 384 kbps 44.1 kHz 5.1", a)
	}
}

// ffmpeg's E-AC-3 is a single independent substream; the PMT type decides
// the label.
func TestEAC3(t *testing.T) {
	a, ok := ParseAC3(sample(t, "c.eac3"), true)
	if !ok {
		t.Fatal("not parsed")
	}
	if a.Codec != "E-AC3 (DD+)" || a.SampleRate != 44100 || a.Layout() != "5.1" || a.Bitrate == 0 {
		t.Errorf("got %+v", a)
	}
}

// A Blu-ray 7.1 E-AC-3 track: an AC-3 5.1 frame, then an E-AC-3 dependent
// substream whose channel map adds the back pair. The total is 7.1, and the
// bitrate counts both, with the AC-3 core's beside it.
func TestEAC3DependentSubstreamAddsChannels(t *testing.T) {
	core := sample(t, "a.ac3")
	// One AC-3 frame, up to the next sync word.
	end := 2
	for end+1 < len(core) && (core[end] != 0x0b || core[end+1] != 0x77) {
		end++
	}
	frame := append([]byte(nil), core[:end]...)
	// A dependent E-AC-3 frame: strmtyp 1, substream 0, frmsiz 255 (512
	// bytes), fscod 1 (44.1 kHz), numblkscod 3, acmod 2, lfeon 0, bsid 16,
	// dialnorm, no compr, chanmape 1 with Lrs/Rrs (bit 6).
	dep := make([]byte, 512)
	w := bitWriter{b: dep}
	w.put(0x0b77, 16)
	w.put(1, 2)
	w.put(0, 3)
	w.put(255, 11)
	w.put(1, 2)
	w.put(3, 2)
	w.put(2, 3)
	w.put(0, 1)
	w.put(16, 5)
	w.put(31, 5)
	w.put(0, 1)
	w.put(1, 1)
	w.put(1<<(15-6), 16)
	stream := append(frame, dep...)
	a, ok := ParseAC3(stream, true)
	if !ok {
		t.Fatal("not parsed")
	}
	if a.Layout() != "7.1" || a.Codec != "E-AC3 (DD+)" || a.CoreBitrate != 384 || a.Bitrate <= 384 {
		t.Errorf("got %+v (%s), want 7.1 E-AC-3 over a 384 kbps core", a, a.Layout())
	}
}

type bitWriter struct {
	b   []byte
	pos int
}

func (w *bitWriter) put(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		if v>>i&1 != 0 {
			w.b[w.pos>>3] |= 0x80 >> (w.pos & 7)
		}
		w.pos++
	}
}

func TestDTS(t *testing.T) {
	a, ok := ParseDTS(sample(t, "d.dts"), "")
	if !ok {
		t.Fatal("not parsed")
	}
	if a.Codec != "DTS" || a.SampleRate != 48000 || a.Layout() != "5.1" || a.Bitrate != 1536 {
		t.Errorf("got %+v (%s), want DTS 1536 kbps 48 kHz 5.1", a, a.Layout())
	}
}

func TestTrueHD(t *testing.T) {
	// A Blu-ray TrueHD stream interleaves an AC-3 core; prepend one so the
	// core's bitrate is found too.
	core := sample(t, "a.ac3")
	end := 2
	for core[end] != 0x0b || core[end+1] != 0x77 {
		end++
	}
	a, ok := ParseTrueHD(append(core[:end:end], sample(t, "e.thd")...))
	if !ok {
		t.Fatal("not parsed")
	}
	if a.Codec != "TRUE-HD" || a.SampleRate != 48000 || a.Channels != 2 || a.CoreBitrate != 384 || a.Atmos {
		t.Errorf("got %+v", a)
	}
}

func TestLPCMHeader(t *testing.T) {
	// 7.1 (channel assignment 11), 96 kHz (4), 24 bits (3).
	a, ok := ParseLPCM([]byte{0x00, 0x00, 11<<4 | 4, 3 << 6})
	if !ok || a.Channels != 8 || !a.LFE || a.SampleRate != 96000 || a.Bits != 24 || a.Layout() != "7.1" {
		t.Errorf("got %+v", a)
	}
	if _, ok := ParseLPCM([]byte{0, 0, 0}); ok {
		t.Error("a short header must not parse")
	}
}
