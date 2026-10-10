package convert

import (
	"strings"
	"testing"
)

// disc is a representative BD3D listing: lossless English, a lossy English
// mix, two other languages, one untagged track, and subtitles.
func disc() Selection {
	return Selection{
		Base:      Track{ID: 4113, StreamID: "V_MPEG4/ISO/AVC", Type: "H.264"},
		Dependent: Track{ID: 4114, StreamID: "V_MPEG4/ISO/MVC", Type: "MVC"},
		Audio: []Track{
			{ID: 4352, StreamID: "A_TRUEHD", Type: "TrueHD Atmos", Lang: "eng"},
			{ID: 4353, StreamID: "A_AC3", Type: "AC3", Lang: "eng"},
			{ID: 4354, StreamID: "A_DTS", Type: "DTS-HD Master Audio", Lang: "fra"},
			{ID: 4355, StreamID: "A_AC3", Type: "AC3", Lang: "spa"},
			{ID: 4356, StreamID: "A_AC3", Type: "AC3"}, // commentary, untagged
		},
		Subtitles: []Track{
			{ID: 4608, StreamID: "S_HDMV/PGS", Type: "PGS", Lang: "eng"},
			{ID: 4609, StreamID: "S_HDMV/PGS", Type: "PGS", Lang: "fra"},
			{ID: 4610, StreamID: "S_HDMV/PGS", Type: "PGS", Lang: "spa"},
		},
	}
}

func ids(tracks []Track) []int {
	out := make([]int, 0, len(tracks))
	for _, t := range tracks {
		out = append(out, t.ID)
	}
	return out
}

func sameIDs(got []Track, want ...int) bool {
	g := ids(got)
	if len(g) != len(want) {
		return false
	}
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

// The default keeps the disc as it is: tracks are the disc's business unless
// the operator says otherwise.
func TestEmptyFilterKeepsEverything(t *testing.T) {
	s := disc()
	got, err := s.Apply(TrackFilter{}, TrackFilter{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got.Audio) != 5 || len(got.Subtitles) != 3 {
		t.Errorf("kept %d audio / %d subs, want all of them", len(got.Audio), len(got.Subtitles))
	}
}

func TestFilterByLanguage(t *testing.T) {
	got, err := disc().Apply(TrackFilter{Langs: []string{"eng"}}, TrackFilter{Langs: []string{"eng"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !sameIDs(got.Audio, 4352, 4353) {
		t.Errorf("audio = %v, want the two English tracks", ids(got.Audio))
	}
	if !sameIDs(got.Subtitles, 4608) {
		t.Errorf("subs = %v, want the English subtitle", ids(got.Subtitles))
	}
}

// A codec name is matched as a substring of both the stream ID and the human
// type, because the disc's own spelling varies: "dts" has to find
// A_DTS/"DTS-HD Master Audio" without the operator knowing which form
// tsMuxeR used.
func TestFilterByCodec(t *testing.T) {
	for _, c := range []struct {
		codec string
		want  []int
	}{
		{"truehd", []int{4352}},
		{"A_TRUEHD", []int{4352}},
		{"atmos", []int{4352}},
		{"dts", []int{4354}},
		{"ac3", []int{4353, 4355, 4356}},
	} {
		got, err := disc().Apply(TrackFilter{Codecs: []string{c.codec}}, TrackFilter{})
		if err != nil {
			t.Fatalf("%s: %v", c.codec, err)
		}
		if !sameIDs(got.Audio, c.want...) {
			t.Errorf("codec %q kept %v, want %v", c.codec, ids(got.Audio), c.want)
		}
	}
}

// A Matroska source's TrueHD track is "TRUE-HD", its stream ID AC-3's: the
// codec filter matches it by spelling aside, as it does DTS-HD by "dtshd".
func TestFilterByCodecSpelling(t *testing.T) {
	for _, c := range []struct {
		track Track
		codec string
	}{
		{Track{StreamID: "A_AC3", Type: "TRUE-HD"}, "truehd"},
		{Track{StreamID: "A_AC3", Type: "TRUE-HD"}, "true-hd"},
		{Track{StreamID: "A_DTS", Type: "DTS-HD Master Audio"}, "dtshd"},
		{Track{StreamID: "A_DTS", Type: "DTS-HD Master Audio"}, "dts-hd ma"},
	} {
		if !(TrackFilter{Codecs: []string{c.codec}}).Matches(c.track) {
			t.Errorf("%q does not match %s %s", c.codec, c.track.StreamID, c.track.Type)
		}
	}
	if (TrackFilter{Codecs: []string{"truehd"}}).Matches(Track{StreamID: "A_DTS", Type: "DTS-HD Master Audio"}) {
		t.Error("truehd matches DTS-HD")
	}
}

// Both dimensions must pass, so the pair names one track rather than the union
// of two sets. This is the case the feature exists for: the English lossless
// mix and nothing else.
func TestLanguageAndCodecAreBothRequired(t *testing.T) {
	got, err := disc().Apply(
		TrackFilter{Langs: []string{"eng"}, Codecs: []string{"truehd"}}, TrackFilter{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !sameIDs(got.Audio, 4352) {
		t.Errorf("kept %v, want only the English TrueHD track", ids(got.Audio))
	}
	// The French lossless track must not survive an English filter, even
	// though its codec matches.
	got, err = disc().Apply(
		TrackFilter{Langs: []string{"eng"}, Codecs: []string{"dts"}}, TrackFilter{})
	if err == nil {
		t.Errorf("an English DTS track does not exist here, but Apply kept %v", ids(got.Audio))
	}
}

// A disc that states no language for a track still has to be selectable.
// "und" is what Matroska already calls an absent tag, so that is the name used
// rather than inventing one.
func TestUndMatchesUntaggedTracks(t *testing.T) {
	got, err := disc().Apply(TrackFilter{Langs: []string{"und"}}, TrackFilter{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !sameIDs(got.Audio, 4356) {
		t.Errorf("kept %v, want the untagged commentary track", ids(got.Audio))
	}
	// And asking for English must not sweep it in.
	got, _ = disc().Apply(TrackFilter{Langs: []string{"eng"}}, TrackFilter{})
	for _, t2 := range got.Audio {
		if t2.ID == 4356 {
			t.Error("an untagged track was treated as English")
		}
	}
}

func TestFilterIsCaseInsensitive(t *testing.T) {
	got, err := disc().Apply(TrackFilter{Langs: []string{"ENG"}, Codecs: []string{"TrueHD"}}, TrackFilter{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !sameIDs(got.Audio, 4352) {
		t.Errorf("kept %v, want the match regardless of case", ids(got.Audio))
	}
}

func TestFilterAcceptsSeveralValues(t *testing.T) {
	got, err := disc().Apply(TrackFilter{Langs: ParseList("eng, spa")}, TrackFilter{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !sameIDs(got.Audio, 4352, 4353, 4355) {
		t.Errorf("kept %v, want the English and Spanish tracks", ids(got.Audio))
	}
}

// A filter matching nothing is an error, not a silent pass or a silent drop.
// Carrying every track on would defeat the request; dropping all audio would
// produce a film nobody can watch, discovered hours later.
func TestFilterMatchingNothingIsAnError(t *testing.T) {
	_, err := disc().Apply(TrackFilter{Langs: []string{"jpn"}}, TrackFilter{})
	if err == nil {
		t.Fatal("a filter that matches no audio must fail")
	}
	// The message has to say what the disc does have, or the operator has to
	// run it again just to look.
	for _, want := range []string{"jpn", "TrueHD Atmos (eng)", "AC3 (spa)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
	if _, err := disc().Apply(TrackFilter{}, TrackFilter{Langs: []string{"jpn"}}); err == nil {
		t.Error("a filter that matches no subtitle must fail too")
	}
}

// A source with no tracks of a kind at all is not a filter error: a disc with
// no subtitles asked to keep English subtitles has simply nothing to do.
func TestNoTracksOfAKindIsNotAnError(t *testing.T) {
	s := disc()
	s.Subtitles = nil
	got, err := s.Apply(TrackFilter{}, TrackFilter{Langs: []string{"eng"}})
	if err != nil {
		t.Fatalf("a disc with no subtitles should not fail: %v", err)
	}
	if len(got.Subtitles) != 0 {
		t.Errorf("got %d subtitles from a disc with none", len(got.Subtitles))
	}
}

func TestParseList(t *testing.T) {
	for in, want := range map[string]int{"": 0, "eng": 1, "eng,fra": 2, " eng , , fra ": 2, ",": 0} {
		if got := ParseList(in); len(got) != want {
			t.Errorf("ParseList(%q) = %v, want %d entries", in, got, want)
		}
	}
}

// --- best-of selection and language aliases ---

// "the best English track" is the request this exists for: the language
// filter and Best compose, so it is the best of the English tracks and not
// the best track if it happens to be English.
func TestBestComposesWithTheLanguageFilter(t *testing.T) {
	s := Selection{
		Base:      Track{StreamID: "V_MPEG4/ISO/AVC"},
		Dependent: Track{StreamID: "V_MPEG4/ISO/MVC"},
		Audio: []Track{
			// A French lossless 7.1 track is the best on the disc, but not English.
			{ID: 1, StreamID: "A_DTS", Type: "DTS-HD Master Audio", Lang: "fra", Info: "Channels: 7.1"},
			{ID: 2, StreamID: "A_AC3", Type: "AC3", Lang: "eng", Info: "Bitrate: 640Kbps Channels: 5.1"},
			{ID: 3, StreamID: "A_TRUEHD", Type: "TrueHD Atmos", Lang: "eng", Info: "Channels: 7.1"},
			{ID: 4, StreamID: "A_AC3", Type: "AC3", Lang: "eng", Info: "Bitrate: 192Kbps Channels: 2"},
		},
	}
	got, err := s.Apply(TrackFilter{Langs: []string{"eng"}, Best: true}, TrackFilter{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got.Audio) != 1 || got.Audio[0].ID != 3 {
		t.Fatalf("kept %v, want only the English TrueHD 7.1 (track 3)", ids(got.Audio))
	}
}

func TestBestAloneRanksTheWholeDisc(t *testing.T) {
	got, err := disc().Apply(TrackFilter{Best: true}, TrackFilter{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got.Audio) != 1 || got.Audio[0].ID != 4352 {
		t.Errorf("kept %v, want the single best track", ids(got.Audio))
	}
}

// Best is not Empty: a filter that only ranks still has work to do, and
// treating it as empty would skip it entirely.
func TestBestIsNotAnEmptyFilter(t *testing.T) {
	if (TrackFilter{Best: true}).Empty() {
		t.Error("a Best-only filter reported itself empty, so it would be skipped")
	}
}

// Best never applies to subtitles. Several are routinely wanted at once, and
// ranking PGS streams against each other would mean nothing.
func TestBestDoesNotNarrowSubtitles(t *testing.T) {
	got, err := disc().Apply(TrackFilter{}, TrackFilter{Langs: []string{"eng", "fra"}, Best: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !sameIDs(got.Subtitles, 4608, 4609) {
		t.Errorf("kept %v, want both requested subtitle languages", ids(got.Subtitles))
	}
}

// Several subtitle languages at once is the other half of the request.
func TestSeveralSubtitleLanguages(t *testing.T) {
	s := disc()
	s.Subtitles = append(s.Subtitles, Track{ID: 4611, StreamID: "S_HDMV/PGS", Type: "PGS", Lang: "por"})
	got, err := s.Apply(TrackFilter{}, TrackFilter{Langs: ParseList("eng,pt-br")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !sameIDs(got.Subtitles, 4608, 4611) {
		t.Errorf("kept %v, want the English and Portuguese subtitles", ids(got.Subtitles))
	}
}

// A Blu-ray cannot say "Brazilian Portuguese" in ISO-639-2 — there is only
// "por" — and authoring tools emit the non-standard "pob" or "ptb" instead.
// Asking for pt-br must match whichever the disc chose, without the operator
// knowing which.
func TestPortugueseAliasesAllMatch(t *testing.T) {
	for _, discTag := range []string{"por", "pob", "ptb"} {
		for _, asked := range []string{"pt-br", "ptbr", "pt", "por", "pob"} {
			f := TrackFilter{Langs: []string{asked}}
			if !f.Matches(Track{Lang: discTag}) {
				t.Errorf("asking for %q did not match a disc tagged %q", asked, discTag)
			}
		}
	}
	// And it must not match something unrelated.
	if (TrackFilter{Langs: []string{"pt-br"}}).Matches(Track{Lang: "eng"}) {
		t.Error("pt-br matched an English track")
	}
}

// ISO-639-2 has bibliographic and terminological codes for several languages
// and sources disagree about which to use, so both spellings resolve alike.
func TestBibliographicAndTerminologicalCodesMatch(t *testing.T) {
	for _, pair := range [][2]string{{"fra", "fre"}, {"deu", "ger"}, {"zho", "chi"}} {
		if !(TrackFilter{Langs: []string{pair[0]}}).Matches(Track{Lang: pair[1]}) {
			t.Errorf("%q did not match a track tagged %q", pair[0], pair[1])
		}
		if !(TrackFilter{Langs: []string{pair[1]}}).Matches(Track{Lang: pair[0]}) {
			t.Errorf("%q did not match a track tagged %q", pair[1], pair[0])
		}
	}
}

// An unknown code is passed through rather than dropped, so a language with
// no alias entry still works.
func TestUnknownLanguageCodesStillMatch(t *testing.T) {
	if !(TrackFilter{Langs: []string{"swe"}}).Matches(Track{Lang: "swe"}) {
		t.Error("a code with no alias entry should still match itself")
	}
}
