package convert

import (
	"strings"
	"testing"
)

// The tracks of the Blu-ray fixture, as the probe lists them.
var fixtureTracks = []Track{
	{ID: 4113, Type: "H.264", StreamID: "V_MPEG4/ISO/AVC"},
	{ID: 4114, Type: "MVC", StreamID: "V_MPEG4/ISO/MVC"},
	{ID: 4352, Type: "AC3", StreamID: "A_AC3", Lang: "eng"},
}

func TestSelectTracksOnARealSource(t *testing.T) {
	sel, err := SelectTracks(fixtureTracks)
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

// A 2D selection takes a Dolby Vision enhancement layer as the dependent
// track when the video is HEVC, and leaves it when it is not.
func TestSelectTracks2DTakesTheDolbyVisionLayer(t *testing.T) {
	bl := Track{ID: 0x1011, StreamID: "V_MPEGH/ISO/HEVC"}
	el := Track{ID: 0x1015, StreamID: streamDVEL}
	audio := Track{ID: 0x1100, StreamID: "A_AC3"}
	sel, err := SelectTracks2D([]Track{bl, el, audio})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Base.ID != bl.ID || sel.Dependent.ID != el.ID || sel.Dependent.Kind() != KindEnhancement || len(sel.Audio) != 1 {
		t.Errorf("selection %+v", sel)
	}
	sel, err = SelectTracks2D([]Track{{ID: 0x1011, StreamID: "V_MPEG4/ISO/AVC"}, el})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Dependent.StreamID != "" {
		t.Errorf("an H.264 picture took an enhancement layer: %+v", sel)
	}
	if threeD([]Track{bl, el}) {
		t.Error("a Dolby Vision source counted as 3D")
	}
}
