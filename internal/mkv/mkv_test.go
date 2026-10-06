package mkv

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, parts ...string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join(append([]string{"..", "..", "testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// Display times in decode order, as mkvmerge gives them for the same
// streams (see testdata/mkv): x264's B-pyramid, and x265's open GOP whose
// CRA is followed by pictures shown before it.
var wantPTS = map[Codec]string{
	H264: "0K 83 42 167 125 250 209 417 334 292 375 459 500K 542 626 584 792 709 667 751 834 918 876 959 1001K 1084 1043",
	HEVC: "0K 167 83 42 125 250 209 292 500K 417 334 375 459 542 709 626 584 667 918 834 751 792 876 1001K 959 1084 1043",
}

func TestVideoFrameOrder(t *testing.T) {
	for codec, name := range map[Codec]string{H264: "bframes.264", HEVC: "bframes.265"} {
		v, err := NewVideoSource(fixture(t, "mkv", name), codec, 24000, 1001, 1)
		if err != nil {
			t.Fatal(err)
		}
		tr := v.Track()
		if tr.Width != 1280 || tr.Height != 480 || len(tr.CodecPrivate) < 20 {
			t.Errorf("%s: %dx%d, %d bytes of codec private", name, tr.Width, tr.Height, len(tr.CodecPrivate))
		}
		var got []string
		for {
			f, err := v.Next()
			if err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			s := itoa(ticks(f.PTS))
			if f.Keyframe {
				s += "K"
			}
			got = append(got, s)
			// Length-prefixed NAL units, no start codes.
			for b := f.Data; len(b) > 0; {
				n := int(binary.BigEndian.Uint32(b))
				if n == 0 || n+4 > len(b) {
					t.Fatalf("%s: bad NAL length %d", name, n)
				}
				b = b[4+n:]
			}
		}
		if strings.Join(got, " ") != wantPTS[codec] {
			t.Errorf("%s:\n got %s\nwant %s", name, strings.Join(got, " "), wantPTS[codec])
		}
	}
}

func itoa(v int64) string {
	b := []byte{}
	if v == 0 {
		return "0"
	}
	for ; v > 0; v /= 10 {
		b = append([]byte{byte('0' + v%10)}, b...)
	}
	return string(b)
}

func TestAudioFrames(t *testing.T) {
	for _, c := range []struct {
		file    string
		format  AudioFormat
		codec   string
		rate    int
		ch      int
		frames  int
		frameMs float64
	}{
		{"a.ac3", AC3, "A_AC3", 44100, 6, 29, 1536.0 / 44.1},
		{"c.eac3", AC3, "A_EAC3", 44100, 6, 29, 1536.0 / 44.1},
		{"d.dts", DTS, "A_DTS", 48000, 6, 19, 512.0 / 48},
		{"e.thd", TrueHD, "A_TRUEHD", 48000, 2, 120, 40.0 / 48},
		{"b.wav", WAV, "A_PCM/INT/LIT", 48000, 2, 25, 40},
	} {
		a, err := NewAudioSource(fixture(t, "bluray", "src", c.file), c.format, false, "fra")
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		tr := a.Track()
		if tr.CodecID != c.codec || tr.SampleRate != c.rate || tr.Channels != c.ch || tr.Language != "fra" {
			t.Errorf("%s: %+v", c.file, tr)
		}
		n := 0
		var last time.Duration
		for {
			f, err := a.Next()
			if err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			want := time.Duration(float64(n) * c.frameMs * float64(time.Millisecond))
			if d := f.PTS - want; d > time.Microsecond || d < -time.Microsecond {
				t.Errorf("%s frame %d at %v, want %v", c.file, n, f.PTS, want)
				break
			}
			last = f.PTS
			n++
		}
		if n != c.frames {
			t.Errorf("%s: %d frames, want %d (last at %v)", c.file, n, c.frames, last)
		}
	}
}

// A Blu-ray TrueHD stream interleaves an AC-3 core: the main track takes
// the TrueHD access units, the core track the AC-3 frames.
func TestTrueHDWithCore(t *testing.T) {
	ac3, _ := io.ReadAll(fixture(t, "bluray", "src", "a.ac3"))
	thd, _ := io.ReadAll(fixture(t, "bluray", "src", "e.thd"))
	var ac3Frames, thdUnits [][]byte
	for b := ac3; len(b) > 0; {
		h, ok := ac3Header(b)
		if !ok || h.size > len(b) {
			break
		}
		ac3Frames, b = append(ac3Frames, b[:h.size]), b[h.size:]
	}
	for b := thd; len(b) >= 4; {
		n := int(binary.BigEndian.Uint16(b)&0x0fff) * 2
		if n > len(b) {
			break
		}
		thdUnits, b = append(thdUnits, b[:n]), b[n:]
	}
	var mixed []byte
	for i, u := range thdUnits {
		if i%40 == 0 && i/40 < len(ac3Frames) {
			mixed = append(mixed, ac3Frames[i/40]...)
		}
		mixed = append(mixed, u...)
	}
	if !HasCore(bytes.NewReader(mixed), TrueHD) {
		t.Fatal("the core should be found")
	}
	count := func(core bool) (int, string) {
		a, err := NewAudioSource(bytes.NewReader(mixed), TrueHD, core, "")
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for {
			if _, err := a.Next(); err != nil {
				break
			}
			n++
		}
		return n, a.Track().CodecID
	}
	if n, id := count(false); n != len(thdUnits) || id != "A_TRUEHD" {
		t.Errorf("main: %d %s, want %d TrueHD units", n, id, len(thdUnits))
	}
	if n, id := count(true); n != (len(thdUnits)+39)/40 || id != "A_AC3" {
		t.Errorf("core: %d %s", n, id)
	}
}

func TestPGSDisplaySets(t *testing.T) {
	seg := func(pts uint32, typ byte, body ...byte) []byte {
		b := []byte{'P', 'G', 0, 0, 0, 0, 0, 0, 0, 0, typ, 0, byte(len(body))}
		binary.BigEndian.PutUint32(b[2:], pts)
		return append(b, body...)
	}
	var sup []byte
	sup = append(sup, seg(90000, 0x16, 1, 2)...)
	sup = append(sup, seg(90000, 0x17, 3)...)
	sup = append(sup, seg(90000, 0x80)...)
	sup = append(sup, seg(180000, 0x16, 4)...)
	sup = append(sup, seg(180000, 0x80)...)
	p := NewPGSSource(bytes.NewReader(sup), "eng")
	f1, _ := p.Next()
	f2, _ := p.Next()
	if _, err := p.Next(); err != io.EOF {
		t.Errorf("want two display sets, got more: %v", err)
	}
	if f1.PTS != time.Second || !bytes.Equal(f1.Data, []byte{0x16, 0, 2, 1, 2, 0x17, 0, 1, 3, 0x80, 0, 0}) {
		t.Errorf("first: %v % x", f1.PTS, f1.Data)
	}
	if f2.PTS != 2*time.Second || len(f2.Data) != 7 {
		t.Errorf("second: %v % x", f2.PTS, f2.Data)
	}
}

func TestSizes(t *testing.T) {
	for _, c := range []struct {
		n    uint64
		want []byte
	}{{0, []byte{0x80}}, {126, []byte{0xfe}}, {127, []byte{0x40, 0x7f}}, {16382, []byte{0x7f, 0xfe}}, {16383, []byte{0x20, 0x3f, 0xff}}} {
		if got := putSize(nil, c.n); !bytes.Equal(got, c.want) {
			t.Errorf("size %d: % x, want % x", c.n, got, c.want)
		}
	}
	for n := 2; n < 20; n++ {
		if v := void(n); len(v) != n {
			t.Errorf("void(%d) is %d bytes", n, len(v))
		}
	}
}

// Malformed input must give an error, never a panic or a hang.
func FuzzSources(f *testing.F) {
	for _, n := range []string{"bframes.264", "bframes.265"} {
		b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "mkv", n))
		f.Add(b[:4096], uint8(0))
	}
	for i, n := range []string{"a.ac3", "e.thd", "d.dts", "b.wav"} {
		b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "bluray", "src", n))
		f.Add(b[:min(4096, len(b))], uint8(i+2))
	}
	f.Fuzz(func(t *testing.T, b []byte, kind uint8) {
		var s Source
		switch kind % 6 {
		case 0, 1:
			v, err := NewVideoSource(bytes.NewReader(b), Codec(kind%2), 24000, 1001, 1)
			if err != nil {
				return
			}
			s = v
		default:
			a, err := NewAudioSource(bytes.NewReader(b), AudioFormat(kind%6-2), kind&8 != 0, "")
			if err != nil {
				return
			}
			s = a
		}
		for i := 0; i < 10000; i++ {
			if _, err := s.Next(); err != nil {
				return
			}
		}
	})
}

// Sync points start a late track late and carry it across a gap, while a
// point that agrees with the sample count (or lags it) changes nothing.
func TestAudioSyncPoints(t *testing.T) {
	read := func(sp []SyncPoint) ([]time.Duration, *AudioSource) {
		a, err := NewAudioSource(fixture(t, "bluray", "src", "a.ac3"), AC3, false, "")
		if err != nil {
			t.Fatal(err)
		}
		a.SetSyncPoints(sp)
		var pts []time.Duration
		for {
			f, err := a.Next()
			if err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			pts = append(pts, f.PTS)
		}
		return pts, a
	}
	plain, _ := read(nil)
	if len(plain) < 12 || plain[0] != 0 {
		t.Fatalf("plain timing %v", plain[:3])
	}
	b, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "bluray", "src", "a.ac3"))
	h, _ := ac3Header(b)
	frame := int64(h.size)
	pts, a := read([]SyncPoint{
		{Offset: 0, At: 300 * time.Millisecond},                  // a late start
		{Offset: 5 * frame, At: 300*time.Millisecond + plain[5]}, // agrees: nothing
		{Offset: 10 * frame, At: 2 * time.Second},                // a gap
		{Offset: 11 * frame, At: time.Second},                    // behind: ignored
	})
	for i, p := range pts {
		want := plain[i] + 300*time.Millisecond
		if i >= 10 {
			want = 2*time.Second + plain[i] - plain[10]
		}
		if d := p - want; d > time.Microsecond || d < -time.Microsecond {
			t.Fatalf("frame %d at %v, want %v", i, p, want)
		}
	}
	if a.Delay() != 300*time.Millisecond || len(a.Gaps()) != 1 || a.Gaps()[0] != 2*time.Second-(300*time.Millisecond+plain[10]) {
		t.Errorf("delay %v, gaps %v", a.Delay(), a.Gaps())
	}
}

// A delayed picture keeps its frame spacing, shifted.
func TestVideoDelay(t *testing.T) {
	read := func(d time.Duration) []time.Duration {
		v, err := NewVideoSource(fixture(t, "mkv", "bframes.264"), H264, 24000, 1001, 1)
		if err != nil {
			t.Fatal(err)
		}
		v.SetDelay(d)
		var pts []time.Duration
		for {
			f, err := v.Next()
			if err == io.EOF {
				return pts
			} else if err != nil {
				t.Fatal(err)
			}
			pts = append(pts, f.PTS, f.Order)
		}
	}
	plain, late := read(0), read(965*time.Millisecond)
	for i := range plain {
		if late[i]-plain[i] != 965*time.Millisecond {
			t.Fatalf("time %d: %v vs %v", i, late[i], plain[i])
		}
	}
}
