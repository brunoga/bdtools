package bdmv

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/brunoga/bdtools/m2ts"
)

// frames is a stream of frames from a list.
func frames(fs []MuxFrame) func() (MuxFrame, error) {
	return func() (MuxFrame, error) {
		if len(fs) == 0 {
			return MuxFrame{}, io.EOF
		}
		f := fs[0]
		fs = fs[1:]
		return f, nil
	}
}

// filmFrames is ten seconds of a 24 fps video, IDR every second, I P B B
// order (decode one frame before presentation), and of 640 kbit/s AC-3.
func filmFrames(r *rand.Rand) (video, audio []MuxFrame) {
	const frame = 3754 // 90 kHz ticks of 1/23.976 s
	for i := range 240 {
		size := 20000 + r.IntN(40000)
		if i%24 == 0 {
			size = 150000
		}
		data := make([]byte, size)
		for j := range data {
			data[j] = byte(r.Uint32())
		}
		video = append(video, MuxFrame{PTS: int64(i+1) * frame, DTS: int64(i) * frame, Data: data, Key: i%24 == 0})
	}
	for i := range 10 * 1000 / 32 {
		data := make([]byte, 2560)
		data[0] = byte(i)
		audio = append(audio, MuxFrame{PTS: int64(i) * 2880, DTS: int64(i) * 2880, Data: data})
	}
	return video, audio
}

func TestMuxClip(t *testing.T) {
	video, audio := filmFrames(rand.New(rand.NewPCG(1, 2)))
	var out bytes.Buffer
	c := &ClipMux{W: &out, Rate: 48_000_000, Streams: []*MuxStream{
		{PID: 0x1011, Coding: CodingAVC, Format: 6, Rate: 1, Aspect: 3, Buffer: 4 << 20, Next: frames(video)},
		{PID: 0x1100, Coding: CodingAC3, Format: 6, Rate: 1, Lang: "eng", Buffer: 16 << 10, Next: frames(audio)},
	}}
	const shift = 54_000_000
	const start = shift*300 - Preload
	if err := Mux([]*ClipMux{c}, start, shift); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	if len(b)%6144 != 0 || int(c.Packets)*192 != len(b) {
		t.Fatalf("%d bytes for %d packets: not whole aligned units", len(b), c.Packets)
	}
	// arrival times rise, the PCR comes often enough
	var last, lastPCR int64 = -1, -1
	for i := range int(c.Packets) {
		p := b[i*192:]
		ats := int64(p[0]&0x3f)<<24 | int64(p[1])<<16 | int64(p[2])<<8 | int64(p[3])
		if i == 0 && ats != start%(1<<30) {
			t.Fatalf("the first packet arrives at %d, want %d", ats, start%(1<<30))
		}
		// on a 30-bit clock: it wraps every 40 s
		if d := (ats - last) & (1<<30 - 1); i > 0 && (d == 0 || d > 27_000_000) {
			t.Fatalf("packet %d arrives %d after the one before", i, d)
		}
		last = ats
		if pid := uint16(p[5]&0x1f)<<8 | uint16(p[6]); pid == pcrPID {
			if d := (ats - lastPCR) & (1<<30 - 1); lastPCR >= 0 && d > 27_000_000/10 {
				t.Fatalf("a PCR %d ms after the last", d/27000)
			}
			lastPCR = ats
		}
	}
	// the entry points are the IDR pictures, where they start
	if len(c.EP) != 10 {
		t.Fatalf("%d entry points, want 10", len(c.EP))
	}
	for i, e := range c.EP {
		p := b[int(e.SPN)*192+4:]
		if pid := uint16(p[1]&0x1f)<<8 | uint16(p[2]); pid != 0x1011 || p[1]&0x40 == 0 || e.PTS != uint64(video[24*i].PTS+shift) {
			t.Fatalf("entry point %d at packet %d (PID %#x, PTS %d)", i, e.SPN, pid, e.PTS)
		}
	}
	// every frame back, with its timestamps
	rd := m2ts.NewReader(bytes.NewReader(b))
	got := map[uint16][]m2ts.PES{}
	for {
		p, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[p.PID] = append(got[p.PID], p)
	}
	for _, s := range []struct {
		pid uint16
		fs  []MuxFrame
	}{{0x1011, video}, {0x1100, audio}} {
		if len(got[s.pid]) != len(s.fs) {
			t.Fatalf("%#x: %d frames back of %d", s.pid, len(got[s.pid]), len(s.fs))
		}
		for i, f := range s.fs {
			g := got[s.pid][i]
			dts := g.DTS
			if dts < 0 {
				dts = g.PTS
			}
			if !bytes.Equal(g.Payload, f.Data) || g.PTS != f.PTS+shift || dts != f.DTS+shift {
				t.Fatalf("%#x: frame %d back as %d bytes at %d/%d", s.pid, i, len(g.Payload), g.PTS, g.DTS)
			}
		}
	}
	// replayed from the arrivals: each frame is in by its decoding, and no
	// buffer holds more than its size
	bufs := map[uint16]int{0x1011: 4 << 20, 0x1100: 16 << 10}
	for pid, fs := range map[uint16][]MuxFrame{0x1011: video, 0x1100: audio} {
		var arrive []int64 // each frame's last packet
		var at []int64     // each packet's arrival, and its frame
		var of []int
		k := -1
		for i := range int(c.Packets) {
			p := b[i*192:]
			if uint16(p[5]&0x1f)<<8|uint16(p[6]) != pid {
				continue
			}
			ats := start + (int64(p[0]&0x3f)<<24|int64(p[1])<<16|int64(p[2])<<8|int64(p[3])-start)&(1<<30-1)
			for ats < start {
				ats += 1 << 30
			}
			if p[5]&0x40 != 0 {
				k++
				arrive = append(arrive, 0)
			}
			arrive[k] = ats
			at, of = append(at, ats), append(of, k)
		}
		for i, f := range fs {
			if arrive[i] >= (f.DTS+shift)*300 {
				t.Fatalf("%#x: frame %d in at %d, after its decoding at %d", pid, i, arrive[i], (f.DTS+shift)*300)
			}
		}
		for j, a := range at {
			n := 0
			for q := 0; q <= j; q++ {
				if (fs[of[q]].DTS+shift)*300 > a {
					n += 184
				}
			}
			if n > bufs[pid] {
				t.Fatalf("%#x: %d bytes in a buffer of %d", pid, n, bufs[pid])
			}
		}
	}
	if c.First != shift || c.Last != shift+240*3754 {
		t.Errorf("presented from %d to %d", c.First, c.Last)
	}
}
