package convert

import (
	"strings"
	"testing"
)

// A real listing, from tsMuxeR 2.7.0 on a 3D m2ts carrying both views and an
// audio track.
const realListing = `tsMuxeR version 2.7.0. github.com/justdan96/tsMuxer
Track ID:    4113
Stream type: H.264
Stream ID:   V_MPEG4/ISO/AVC
Stream info: Profile: High@3.0  Resolution: 640:480p  Frame rate: 23.976
Stream lang: 

Track ID:    4114
Stream type: MVC
Stream ID:   V_MPEG4/ISO/MVC
Stream info: H.264/MVC Views: 2 Profile: High@3.0  Resolution: 640:480p  Frame rate: 23.976
Stream lang: 

Track ID:    4352
Stream type: AC3
Stream ID:   A_AC3
Stream info: Bitrate: 192Kbps Sample Rate: 44KHz Channels: 1
Stream lang: eng

Duration: 00:00:01.065
`

func TestParseListingOnARealSource(t *testing.T) {
	tracks, err := ParseListing(realListing)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 3 {
		t.Fatalf("got %d tracks, want 3: %+v", len(tracks), tracks)
	}
	want := []struct {
		id       int
		streamID string
		lang     string
		kind     Kind
	}{
		{4113, "V_MPEG4/ISO/AVC", "", KindBaseView},
		{4114, "V_MPEG4/ISO/MVC", "", KindDependentView},
		{4352, "A_AC3", "eng", KindAudio},
	}
	for i, w := range want {
		got := tracks[i]
		if got.ID != w.id || got.StreamID != w.streamID || got.Lang != w.lang || got.Kind() != w.kind {
			t.Errorf("track %d = %+v (kind %v), want id=%d id=%q lang=%q kind=%v",
				i, got, got.Kind(), w.id, w.streamID, w.lang, w.kind)
		}
	}
	// The banner and the trailing Duration line must not become tracks.
	if tracks[2].Info == "" {
		t.Error("the audio track's info line should be captured")
	}
}

// An elementary stream has no Track ID lines: tsMuxeR describes it but has
// nothing to demux, and the conversion needs the views as separate tracks.
func TestParseListingRejectsAnElementaryStream(t *testing.T) {
	const es = `tsMuxeR version 2.7.0. github.com/justdan96/tsMuxer
Stream type: H.264
Stream ID:   V_MPEG4/ISO/AVC
Stream info: Profile: High@3.0  Resolution: 640:480p  Frame rate: not found
Stream lang: 
`
	_, err := ParseListing(es)
	if err == nil {
		t.Fatal("an elementary stream must be refused")
	}
	if !strings.Contains(err.Error(), "elementary stream") {
		t.Errorf("the error should say what the source looks like, got %q", err)
	}
}

func TestParseListingOnEmptyOutput(t *testing.T) {
	if _, err := ParseListing(""); err == nil {
		t.Error("empty output must be an error, not an empty selection")
	}
}

func TestSelectTracksOnARealSource(t *testing.T) {
	tracks, err := ParseListing(realListing)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := SelectTracks(tracks)
	if err != nil {
		t.Fatal(err)
	}
	if sel.Base.ID != 4113 {
		t.Errorf("base view = track %d, want 4113", sel.Base.ID)
	}
	if sel.Dependent.ID != 4114 {
		t.Errorf("dependent view = track %d, want 4114", sel.Dependent.ID)
	}
	if len(sel.Audio) != 1 || sel.Audio[0].ID != 4352 {
		t.Errorf("audio = %+v, want just track 4352", sel.Audio)
	}
	if len(sel.Subtitles) != 0 {
		t.Errorf("subtitles = %+v, want none", sel.Subtitles)
	}
}

// The views are told apart by stream ID, not by order or track number. A disc
// is not obliged to list them in any order, and taking the wrong one as the
// base gives a stream that cannot decode at all.
func TestViewsAreIdentifiedByStreamIDNotOrder(t *testing.T) {
	reversed := []Track{
		{ID: 4114, StreamID: "V_MPEG4/ISO/MVC"},
		{ID: 4113, StreamID: "V_MPEG4/ISO/AVC"},
	}
	sel, err := SelectTracks(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if sel.Base.ID != 4113 || sel.Dependent.ID != 4114 {
		t.Errorf("base=%d dependent=%d; order in the listing must not decide",
			sel.Base.ID, sel.Dependent.ID)
	}
}

func TestSelectTracksRefusesWhatItCannotConvert(t *testing.T) {
	cases := []struct {
		name   string
		tracks []Track
		want   string
	}{
		{"a 2D source", []Track{
			{ID: 1, StreamID: "V_MPEG4/ISO/AVC"},
			{ID: 2, StreamID: "A_AC3"},
		}, "not 3D"},
		{"no video at all", []Track{
			{ID: 1, StreamID: "A_AC3"},
		}, "not 3D"},
		{"dependent view with no base", []Track{
			{ID: 1, StreamID: "V_MPEG4/ISO/MVC"},
		}, "no AVC base view"},
		{"two MVC tracks", []Track{
			{ID: 1, StreamID: "V_MPEG4/ISO/AVC"},
			{ID: 2, StreamID: "V_MPEG4/ISO/MVC"},
			{ID: 3, StreamID: "V_MPEG4/ISO/MVC"},
		}, "2 MVC tracks"},
		{"two AVC tracks", []Track{
			{ID: 1, StreamID: "V_MPEG4/ISO/AVC"},
			{ID: 2, StreamID: "V_MPEG4/ISO/AVC"},
			{ID: 3, StreamID: "V_MPEG4/ISO/MVC"},
		}, "2 AVC tracks"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := SelectTracks(c.tracks)
			if err == nil {
				t.Fatalf("expected an error mentioning %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err, c.want)
			}
		})
	}
}

// A 2D source's error should say what was there instead of guessing.
func TestNotThreeDErrorNamesWhatItFound(t *testing.T) {
	_, err := SelectTracks([]Track{{ID: 7, StreamID: "V_MPEGH/ISO/HEVC"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "V_MPEGH/ISO/HEVC") || !strings.Contains(err.Error(), "track 7") {
		t.Errorf("error should name the video it found, got %q", err)
	}
}

func TestDemuxMetaNamesEveryTrack(t *testing.T) {
	tracks, _ := ParseListing(realListing)
	sel, _ := SelectTracks(tracks)
	meta := DemuxMeta("/media/Life of Pi (2012)/disc.m2ts", sel)

	if !strings.HasPrefix(meta, "MUXOPT --demux\n") {
		t.Errorf("meta must open with the demux option, got:\n%s", meta)
	}
	for _, want := range []string{
		`V_MPEG4/ISO/AVC, "/media/Life of Pi (2012)/disc.m2ts", track=4113`,
		`V_MPEG4/ISO/MVC, "/media/Life of Pi (2012)/disc.m2ts", track=4114`,
		`A_AC3, "/media/Life of Pi (2012)/disc.m2ts", track=4352, lang=eng`,
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("meta should contain:\n  %s\ngot:\n%s", want, meta)
		}
	}
	// A path with spaces is quoted, which is why the meta file exists rather
	// than tracks being passed as arguments.
	if strings.Count(meta, `"`) != 6 {
		t.Errorf("every path should be quoted, got:\n%s", meta)
	}
}

// The base view is rebuilt with picture timing and repeated parameter sets so
// the extracted stream stands on its own.
func TestDemuxMetaRebuildsTheViews(t *testing.T) {
	tracks, _ := ParseListing(realListing)
	sel, _ := SelectTracks(tracks)
	for _, line := range strings.Split(DemuxMeta("x.m2ts", sel), "\n") {
		if !strings.HasPrefix(line, "V_MPEG4") {
			continue
		}
		if !strings.Contains(line, "insertSEI") || !strings.Contains(line, "contSPS") {
			t.Errorf("view line should rebuild SEI and SPS: %s", line)
		}
	}
}

// Audio order is the source's order, so the first audio track stays first.
func TestAudioKeepsItsSourceOrder(t *testing.T) {
	sel, err := SelectTracks([]Track{
		{ID: 1, StreamID: "V_MPEG4/ISO/AVC"},
		{ID: 2, StreamID: "V_MPEG4/ISO/MVC"},
		{ID: 10, StreamID: "A_AC3", Lang: "eng"},
		{ID: 11, StreamID: "A_DTS", Lang: "fra"},
		{ID: 20, StreamID: "S_HDMV/PGS", Lang: "eng"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Audio) != 2 || sel.Audio[0].ID != 10 || sel.Audio[1].ID != 11 {
		t.Errorf("audio = %+v, want tracks 10 then 11", sel.Audio)
	}
	if len(sel.Subtitles) != 1 || sel.Subtitles[0].ID != 20 {
		t.Errorf("subtitles = %+v, want track 20", sel.Subtitles)
	}
}
