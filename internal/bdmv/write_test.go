package bdmv

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The facts of tsMuxeR's 3D reference (testdata/tsmuxer3d: ten seconds
// of a 1080p MVC film with AC-3 and PGS, tsMuxeR 2.7 --blu-ray --3d).
const (
	refStart, refEnd = 0x019bfcc0, 0x01a2dc52
	refPTS0, refPTS1 = 102<<19 | 1020<<9, 104<<19 | 497<<9
)

func refPlaylist() *Playlist {
	return &Playlist{
		Items: []PlayItem{{Clip: "00000", Codec: "M2TS", ConnectionCondition: 1, InTime: refStart, OutTime: refEnd,
			Streams: []Stream{
				{Kind: KindVideo, PID: 0x1011, Coding: CodingAVC, Format: 6, Rate: 1, OffsetSequence: -1},
				{Kind: KindAudio, PID: 0x1100, Coding: CodingAC3, Format: 6, Rate: 1, Lang: "eng", OffsetSequence: -1},
				{Kind: KindPG, PID: 0x1200, Coding: CodingPGS, Lang: "eng", OffsetSequence: -1},
			},
			DependentPID: 0x1012, OffsetSequences: 1}},
		SubPaths: []SubPath{{Type: SubPathMVC, Items: []SubPlayItem{{Clip: "00001", Codec: "M2TS", ConnectionCondition: 1,
			InTime: refStart, OutTime: refEnd, SyncPTS: refStart}}}},
		Marks: []Mark{{Type: 1, Time: refStart, PID: 0xffff}},
	}
}

func refClips() (base, dep *Clip) {
	base = &Clip{RecordingRate: 0x005b8d80, Packets: 0x8060, PCRPID: 0x1001, Start: refStart, End: refEnd,
		Streams: []ClipStream{
			{PID: 0x1011, Coding: CodingAVC, Format: 6, Rate: 1, Aspect: 3},
			{PID: 0x1100, Coding: CodingAC3, Format: 6, Rate: 1, Lang: "eng"},
			{PID: 0x1200, Coding: CodingPGS, Lang: "eng"},
		},
		EPPID: 0x1011, EP: []EPEntry{{refPTS0, 4, 2}, {refPTS1, 29063, 2}}, Extents: []uint32{0}}
	dep = &Clip{Dependent: true, RecordingRate: 0x0042c1d8, Packets: 0x1660, PCRPID: 0x1001, Start: refStart, End: refEnd,
		Streams: []ClipStream{{PID: 0x1012, Coding: CodingMVC, Format: 6, Rate: 1, Aspect: 3}},
		EPPID:   0x1012, EP: []EPEntry{{refPTS0, 4, 1}, {refPTS1, 0x146e, 1}}, Extents: []uint32{0}}
	return base, dep
}

func reference(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "tsmuxer3d", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sameBytes(t *testing.T, name string, got, want []byte) {
	t.Helper()
	if bytes.Equal(got, want) {
		return
	}
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			t.Errorf("%s: byte %#x is %#02x, tsMuxeR's %#02x (%d bytes, tsMuxeR's %d)", name, i, got[i], want[i], len(got), len(want))
			return
		}
	}
	t.Errorf("%s: %d bytes, tsMuxeR's %d", name, len(got), len(want))
}

// The playlist and the clip infos are tsMuxeR's, byte for byte.
func TestWriteAsTsMuxeR(t *testing.T) {
	base, dep := refClips()
	sameBytes(t, "00000.mpls", refPlaylist().MPLS(), reference(t, "00000.mpls"))
	sameBytes(t, "00000.clpi", base.CLPI(), reference(t, "00000.clpi"))
	sameBytes(t, "00001.clpi", dep.CLPI(), reference(t, "00001.clpi"))
}

// What is written reads back as it was.
func TestWriteReadsBack(t *testing.T) {
	p, err := ParseMPLS(refPlaylist().MPLS())
	if err != nil {
		t.Fatal(err)
	}
	if !p.ThreeD() || p.Items[0].DependentClip != "00001" || p.Items[0].DependentPID != 0x1012 ||
		p.Items[0].OffsetSequences != 1 || len(p.Chapters()) != 1 || len(p.Items[0].Streams) != 3 {
		t.Errorf("the playlist read back as %+v", p.Items[0])
	}
	_, dep := refClips()
	s, err := ParseCLPI(dep.CLPI())
	if err != nil || len(s) != 1 || s[0].PID != 0x1012 || s[0].Coding != CodingMVC {
		t.Errorf("the dependent clip's streams read back as %+v, %v", s, err)
	}
}

// The index is tsMuxeR's but for which movie object starts the disc; the
// movie objects play the film.
func TestWriteNavigation(t *testing.T) {
	ref, got := reference(t, "index.bdmv"), Index(true)
	if len(got) != len(ref) {
		t.Fatalf("index.bdmv: %d bytes, tsMuxeR's %d", len(got), len(ref))
	}
	for i := range ref {
		// FirstPlayback's movie object (tsMuxeR's 2, ours 1)
		if got[i] != ref[i] && i != 0x59 {
			t.Errorf("index.bdmv: byte %#x is %#02x, tsMuxeR's %#02x", i, got[i], ref[i])
		}
	}
	m := MovieObjects()
	if string(m[:8]) != "MOBJ0200" || len(m) != 40+10+2*(4+12) {
		t.Errorf("MovieObject.bdmv: %q, %d bytes", m[:8], len(m))
	}
}
