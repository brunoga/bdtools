package convert

import (
	"bytes"
	"reflect"
	"testing"
	"time"
)

// ofmdPayload builds offset metadata as a Blu-ray carries it.
func ofmdPayload(pts int64, offsets [][]int8) []byte {
	p := append(append([]byte(nil), ofmdUUID...), "OFMD"...)
	frames := len(offsets[0])
	p = append(p,
		0x81, // reserved, frame_rate 23.976
		byte(pts>>30&7),
		0x80|byte(pts>>23&0x7f), byte(pts>>15),
		0x80|byte(pts>>8&0x7f), byte(pts),
		0x80|byte(len(offsets)), byte(frames),
		0x80, 0x00)
	for _, row := range offsets {
		for _, v := range row {
			b := byte(v)
			if v < 0 {
				b = 0x80 | byte(-v)
			}
			p = append(p, b)
		}
	}
	return p
}

// seiMessage wraps a payload as an SEI message.
func seiMessage(typ int, payload []byte) []byte {
	var out []byte
	for ; typ >= 255; typ -= 255 {
		out = append(out, 0xff)
	}
	out = append(out, byte(typ))
	n := len(payload)
	for ; n >= 255; n -= 255 {
		out = append(out, 0xff)
	}
	return append(append(out, byte(n)), payload...)
}

// nal makes an Annex B NAL unit of RBSP, with emulation prevention.
func nal(typ byte, rbsp []byte) []byte {
	out := []byte{0, 0, 1, typ}
	zeros := 0
	for _, c := range rbsp {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

func TestOffsetMetadata(t *testing.T) {
	offsets := [][]int8{{0, 0, 0, 0, 6, 6}, {-3, -3, 0, 0, 0, 0}, {127, -127, 1, -1, 0, 0}}
	const pts = int64(1<<32 + 0x2345678)
	payload := ofmdPayload(pts, offsets)
	nested := seiMessage(37, append([]byte{0x40}, seiMessage(5, payload)...))
	for _, c := range []struct {
		name string
		au   []byte
	}{
		{"nested, dependent view", append(nal(6, append(nested, 0x80)), nal(20, []byte{1, 2, 3})...)},
		{"direct", append(nal(6, append(seiMessage(5, payload), 0x80)), nal(20, []byte{1})...)},
		{"after another message, both views", bytes.Join([][]byte{
			nal(5, []byte("base slice")), nal(15, []byte{9}),
			nal(6, append(append(seiMessage(4, []byte("GA94")), nested...), 0x80)), nal(20, []byte{1}),
		}, nil)},
	} {
		if !bytes.Contains(c.au, []byte{0, 0, 3}) {
			t.Fatalf("%s: the test means the offsets to need emulation prevention", c.name)
		}
		g, ok := parseOffsetMetadata(c.au)
		if !ok || g.pts != pts || !reflect.DeepEqual(g.offsets, offsets) {
			t.Errorf("%s: %v %d %v", c.name, ok, g.pts, g.offsets)
		}
	}
	// "OFMD" inside a picture's data is not offset metadata, nor is it
	// after the dependent view's slices, nor under another UUID.
	other := append([]byte(nil), payload...)
	other[0] ^= 1
	for _, au := range [][]byte{
		nal(20, append([]byte{0x80}, payload...)),
		append(nal(20, []byte{1}), nal(6, append(seiMessage(5, payload), 0x80))...),
		nal(6, append(seiMessage(5, other), 0x80)),
		nal(6, append(seiMessage(5, payload[:len(payload)-5]), 0x80)),
	} {
		if g, ok := parseOffsetMetadata(au); ok {
			t.Errorf("found offsets that are not there: %v", g)
		}
	}
}

// The offset at a time is the frame showing then, of the GOP it is in.
func TestDepthMap(t *testing.T) {
	const frame = 40 * time.Millisecond
	m := &depthMap{frame: frame}
	m.add(time.Second, offsetGOP{offsets: [][]int8{{1, 2, 3}, {9, 9, 9}}})
	m.add(2*time.Second, offsetGOP{offsets: [][]int8{{-4, -5}}})
	m.add(500*time.Millisecond, offsetGOP{offsets: [][]int8{{7}}}) // out of order
	m.sort()
	for _, c := range []struct {
		seq  int
		at   time.Duration
		want int
	}{
		{0, 0, 0}, // before any
		{0, 500 * time.Millisecond, 7},
		{0, time.Second, 1},
		{0, time.Second + frame, 2},
		{0, time.Second + 2*frame - 5*time.Millisecond, 3}, // rounds to the nearest frame
		{0, time.Second + 10*frame, 3},                     // past the GOP: its last
		{1, time.Second + frame, 9},
		{1, 2 * time.Second, 0}, // a sequence the GOP has not got
		{0, 2*time.Second + frame, -5},
		{-1, time.Second, 0},
		{frontMost, time.Second, 9},            // the nearer of 1 and 9
		{frontMost, 2*time.Second + frame, -5}, // the only one there
		{frontMost, 500 * time.Millisecond, 7},
	} {
		if got := m.offset(c.seq, c.at); got != c.want {
			t.Errorf("sequence %d at %s: %d, want %d", c.seq, c.at, got, c.want)
		}
	}
	var none *depthMap
	if none.offset(0, time.Second) != 0 {
		t.Error("no map, yet an offset")
	}
}
