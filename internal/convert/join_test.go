package convert

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/brunoga/bdtools/m2ts"
	"github.com/brunoga/bdtools/mvc"
)

// basePES lists the base view's PES of a clip in file order: timestamps and
// whether each opens a GOP.
type pesInfo struct {
	pts, dts int64
	rap      bool
}

func clipBasePES(t *testing.T, c clipRef) []pesInfo {
	t.Helper()
	f, _, err := c.open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	r := m2ts.NewReader(f)
	var out []pesInfo
	for {
		p, err := r.Next()
		if err != nil {
			break
		}
		if p.PID == 0x1011 && p.PTS >= 0 && hasSlice(p.Payload) {
			dts := p.DTS
			if dts < 0 {
				dts = p.PTS
			}
			out = append(out, pesInfo{pts: p.PTS, dts: dts, rap: hasNAL(p.Payload, 7) || hasNAL(p.Payload, 5)})
		}
	}
	return out
}

// windowPictures is how many pictures a clip contributes for a window: from
// the GOP opening at or before IN, while their decoding time is before OUT.
func windowPictures(pes []pesInfo, in, out int64) int {
	rap := int64(-1)
	start := false
	n := 0
	for _, p := range pes {
		if !start {
			if p.rap {
				rap = p.dts
			}
			if rap >= 0 && p.pts >= in {
				start = true
			}
		}
	}
	for _, p := range pes {
		if rap >= 0 && p.dts >= rap && p.dts < out {
			n++
		}
	}
	return n
}

// joined remuxes a title of the given clips (the fixture's one clip, cut
// to different windows) and returns the output path.
func joined(t *testing.T, form string, windows func(c clipRef) []clipRef) (string, *goSource, []clipRef) {
	t.Helper()
	src, err := resolveGo(bluray(form), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tracks, err := probeGo(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := SelectTracks(tracks)
	if err != nil {
		t.Fatal(err)
	}
	src.clips = windows(src.clips[0])
	o := DefaultOptions()
	o.Output = filepath.Join(t.TempDir(), "joined.m2ts")
	o.Remux = true
	r := NewRunner(CurrentGOOS, o, nil)
	if err := r.remuxBuiltin(t.Context(), src, sel); err != nil {
		t.Fatalf("%s: %v", form, err)
	}
	return o.Output, src, src.clips
}

// checkJoined checks a joined stream: one clock that never runs backwards
// (arrival times, PCR, the base view's DTS), audio on both sides of the
// join, the program table, and the pictures it decodes to.
func checkJoined(t *testing.T, form, path string, clips []clipRef, wantPictures int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b)%192 != 0 {
		t.Fatalf("%s: %d bytes is not whole source packets", form, len(b))
	}
	var lastATS, lastPCR int64 = -1, -1
	cc := map[uint16]byte{}
	for i := 0; i < len(b); i += 192 {
		p := b[i : i+192]
		ats := int64(binary.BigEndian.Uint32(p) & (atsWrap - 1))
		if ats < lastATS {
			t.Fatalf("%s: arrival time runs back at packet %d (%d after %d)", form, i/192, ats, lastATS)
		}
		lastATS = ats
		ts := p[4:]
		if pcr, ok := tsPCR(ts); ok {
			if pcr <= lastPCR {
				t.Fatalf("%s: PCR runs back at packet %d", form, i/192)
			}
			lastPCR = pcr
		}
		pid := uint16(ts[1]&0x1f)<<8 | uint16(ts[2])
		if ts[3]&0x10 != 0 && pid != 0 && pid != 0x100 {
			if prev, ok := cc[pid]; ok && ts[3]&0x0f != (prev+1)&0x0f {
				t.Fatalf("%s: continuity breaks on PID %#x at packet %d", form, pid, i/192)
			}
			cc[pid] = ts[3] & 0x0f
		}
	}

	r := m2ts.NewReader(bytes.NewReader(b))
	prog, err := r.ReadProgram()
	if err != nil {
		t.Fatal(err)
	}
	if prog.Streams[0].PID != 0x1011 || prog.Streams[1].PID != 0x1012 {
		t.Errorf("%s: program %+v", form, prog.Streams)
	}
	// The second clip starts at or after the end of the first's window (later
	// when its opening GOP has to fit after the first clip's last picture).
	joinAt := clips[0].outTime
	var lastDTS = int64(-1)
	audioBefore, audioAfter := 0, 0
	var lastAudio int64 = -1
	for {
		p, err := r.Next()
		if err != nil {
			break
		}
		switch p.PID {
		case 0x1011:
			dts := p.DTS
			if dts < 0 {
				dts = p.PTS
			}
			if dts <= lastDTS {
				t.Fatalf("%s: base view DTS runs back (%d after %d)", form, dts, lastDTS)
			}
			lastDTS = dts
		case 0x1100:
			if p.PTS <= lastAudio {
				t.Fatalf("%s: audio PTS runs back (%d after %d)", form, p.PTS, lastAudio)
			}
			lastAudio = p.PTS
			if p.PTS < joinAt {
				audioBefore++
			} else {
				audioAfter++
			}
		}
	}
	if audioBefore == 0 || audioAfter == 0 {
		t.Errorf("%s: audio frames %d before the join and %d after", form, audioBefore, audioAfter)
	}

	f, _ := os.Open(path) //nolint:gosec // test
	defer func() { _ = f.Close() }()
	st, err := mvc.NewDecoder(mvc.Options{}).DecodeStream(mvc.Source{Format: mvc.FormatM2TS, R: f}, mvc.DecodeOptions{},
		func(*mvc.StereoFrame) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if st.Frames != wantPictures || st.DependentFrames != wantPictures || st.Errors != 0 {
		t.Errorf("%s: decoded %+v, want %d pictures with both views", form, st, wantPictures)
	}
}

// Two play items of the whole clip join into one stream twice as long.
func TestRemuxJoinsClips(t *testing.T) {
	for _, form := range bothForms {
		path, _, clips := joined(t, form, func(c clipRef) []clipRef { return []clipRef{c, c} })
		pes := clipBasePES(t, clips[0])
		n := windowPictures(pes, clips[0].inTime, clips[0].outTime)
		if n == 0 {
			t.Fatal("the fixture's window has no pictures")
		}
		checkJoined(t, form, path, clips, 2*n)
	}
}

// A later play item cut inside its clip starts at the GOP that opens before
// its IN time and stops with the last picture decoded before OUT.
func TestRemuxJoinsCutClips(t *testing.T) {
	for _, form := range bothForms {
		var second clipRef
		path, _, clips := joined(t, form, func(c clipRef) []clipRef {
			second = c
			frame := int64(3754)
			second.inTime = c.inTime + 3*frame
			second.outTime = c.inTime + 5*frame
			return []clipRef{c, second}
		})
		pes := clipBasePES(t, clips[0])
		want := windowPictures(pes, clips[0].inTime, clips[0].outTime) + windowPictures(pes, second.inTime, second.outTime)
		if whole := windowPictures(pes, clips[0].inTime, clips[0].outTime); want >= 2*whole {
			t.Fatalf("the cut window is not shorter (%d of %d)", want-whole, whole)
		}
		checkJoined(t, form, path, clips, want)
	}
}

// Clips that put a kept track on another PID cannot be joined.
func TestJoinRefusesClipsWithOtherStreams(t *testing.T) {
	a := &m2ts.Program{Streams: []m2ts.ProgramStream{{PID: 0x1011, Type: m2ts.TypeAVC}, {PID: 0x1100, Type: m2ts.TypeAC3}}}
	b := &m2ts.Program{Streams: []m2ts.ProgramStream{{PID: 0x1011, Type: m2ts.TypeAVC}, {PID: 0x1101, Type: m2ts.TypeAC3}}}
	keep := map[uint16]bool{0x1011: true, 0x1100: true}
	if err := sameStreams(a, a, keep, "a", "a"); err != nil {
		t.Errorf("the same streams: %v", err)
	}
	if err := sameStreams(a, b, keep, "a", "b"); err == nil {
		t.Error("a track on another PID must be refused")
	}
}

// Timestamps keep their marker bits and wrap at 33 bits.
func TestTimestampFields(t *testing.T) {
	b := []byte{0x31, 0, 1, 0, 1}
	for _, v := range []int64{0, 90000, ts33 - 1, 123456789} {
		writeTS(b, v)
		if got := readTS(b); got != v {
			t.Errorf("%d reads back as %d", v, got)
		}
		if b[0]&0xf0 != 0x30 || b[0]&1 != 1 || b[2]&1 != 1 || b[4]&1 != 1 {
			t.Errorf("%d: prefix or marker bits lost: % x", v, b)
		}
	}
	writeTS(b, ts33+5)
	if readTS(b) != 5 {
		t.Errorf("no wrap: %d", readTS(b))
	}
	writeTS(b, -1)
	if readTS(b) != ts33-1 {
		t.Errorf("no wrap below zero: %d", readTS(b))
	}
	pkt := make([]byte, 188)
	pkt[0], pkt[3], pkt[4], pkt[5] = 0x47, 0x30, 7, 0x10
	for _, v := range []int64{0, 299, 300, pcrWrap - 1, 27000000 * 3600} {
		setPCR(pkt, v)
		if got, ok := tsPCR(pkt); !ok || got != v {
			t.Errorf("PCR %d reads back as %d", v, got)
		}
	}
}

func FuzzJoinFields(f *testing.F) {
	f.Add(make([]byte, 188))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) < 188 {
			return
		}
		ts := b[:188]
		payload := tsPayload(ts)
		_, _ = tsPCR(ts)
		_, _, _ = pesTimes(payload)
		shiftPESTimes(payload, 12345)
		_ = isRandomAccess(payload)
	})
}
