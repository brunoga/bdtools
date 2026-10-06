package mkv

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// readID and readSize decode EBML element headers, for checking files.
func readID(b []byte) (uint32, int) {
	l := 1
	for l <= 4 && b[0]&(0x80>>(l-1)) == 0 {
		l++
	}
	var id uint32
	for i := 0; i < l; i++ {
		id = id<<8 | uint32(b[i])
	}
	return id, l
}

func readSize(b []byte) (uint64, int) {
	l := 1
	for l <= 8 && b[0]&(0x80>>(l-1)) == 0 {
		l++
	}
	v := uint64(b[0] & (0xff >> l))
	for i := 1; i < l; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, l
}

type element struct {
	id         uint32
	pos, start int // header position, data start
	data       []byte
}

func children(b []byte, base int) []element {
	var out []element
	for i := 0; i < len(b); {
		id, il := readID(b[i:])
		size, sl := readSize(b[i+il:])
		start := i + il + sl
		end := start + int(size)
		if end > len(b) {
			end = len(b)
		}
		out = append(out, element{id: id, pos: base + i, start: base + start, data: b[start:end]})
		i = end
	}
	return out
}

func find(es []element, id uint32) *element {
	for i := range es {
		if es[i].id == id {
			return &es[i]
		}
	}
	return nil
}

func uintOf(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func TestFileStructure(t *testing.T) {
	v, err := NewVideoSource(fixture(t, "mkv", "bframes.264"), H264, 24000, 1001, 1)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAudioSource(fixture(t, "bluray", "src", "a.ac3"), AC3, false, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	chs := []Chapter{{Start: 0, Name: "One"}, {Start: 500 * time.Millisecond, Name: "Two"}}
	if err := Mux(f, []Source{v, a}, Options{Chapters: chs}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	b, _ := os.ReadFile(path)

	top := children(b, 0)
	if len(top) != 2 || top[0].id != idEBML || top[1].id != idSegment {
		t.Fatalf("top level: %d elements", len(top))
	}
	seg := top[1]
	if seg.start+len(seg.data) != len(b) {
		t.Errorf("segment size %d does not reach the end of the file (%d)", len(seg.data), len(b)-seg.start)
	}
	segEls := children(seg.data, seg.start)
	byID := map[uint32]*element{}
	for i := range segEls {
		if _, ok := byID[segEls[i].id]; !ok {
			byID[segEls[i].id] = &segEls[i]
		}
	}
	// The seek head points at the elements.
	sh := byID[idSeekHead]
	if sh == nil {
		t.Fatal("no seek head")
	}
	for _, s := range children(sh.data, sh.start) {
		kids := children(s.data, s.start)
		target, _ := readID(find(kids, idSeekID).data)
		pos := int(uintOf(find(kids, idSeekPos).data))
		if id, _ := readID(b[seg.start+pos:]); id != target {
			t.Errorf("seek entry for %x points at %x", target, id)
		}
	}
	// The duration is the last frame's time.
	info := children(byID[idInfo].data, byID[idInfo].start)
	dur := math.Float64frombits(binary.BigEndian.Uint64(find(info, idDuration).data))
	if dur < 1000 || dur > 1100 {
		t.Errorf("duration %v ms", dur)
	}
	// Two tracks; the audio one is "und", not Matroska's default English.
	tracks := children(byID[idTracks].data, byID[idTracks].start)
	if len(tracks) != 2 {
		t.Fatalf("%d tracks", len(tracks))
	}
	at := children(tracks[1].data, tracks[1].start)
	if string(find(at, idLanguage).data) != "und" {
		t.Errorf("audio language %q", find(at, idLanguage).data)
	}
	vt := children(tracks[0].data, tracks[0].start)
	vid := children(find(vt, idVideo).data, 0)
	if uintOf(find(vid, idStereoMode).data) != 1 {
		t.Error("the video must be flagged side by side, left first")
	}
	// Chapters.
	ed := children(byID[idChapters].data, 0)
	if atoms := children(ed[0].data, 0); len(atoms) != 3 { // UID + two atoms
		t.Errorf("chapters: %d children", len(atoms))
	}
	// Every cue points at a cluster, and blocks inside a cluster are in
	// file order with sane relative times.
	cues := children(byID[idCues].data, byID[idCues].start)
	if len(cues) != 3 {
		t.Errorf("%d cues, want one per keyframe (3)", len(cues))
	}
	for _, c := range cues {
		tp := find(children(c.data, c.start), idCueTrackPositions)
		pos := int(uintOf(find(children(tp.data, tp.start), idCueClusterPosition).data))
		if id, _ := readID(b[seg.start+pos:]); id != idCluster {
			t.Errorf("cue points at %x, not a cluster", id)
		}
	}
	blocks := map[int]int{}
	for _, e := range segEls {
		if e.id != idCluster {
			continue
		}
		for _, k := range children(e.data, e.start) {
			if k.id == idSimpleBlock {
				blocks[int(k.data[0]&0x7f)]++
			}
		}
	}
	if blocks[1] != 27 || blocks[2] != 29 {
		t.Errorf("blocks per track %v, want 27 video and 29 audio", blocks)
	}
}
