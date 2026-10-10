package convert

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// TrackFilter narrows which audio or subtitle tracks are carried into the
// output. A zero TrackFilter keeps everything, which is the default: a disc's
// tracks are the disc's business unless the operator says otherwise.
//
// Languages and codecs are matched independently and both must pass, so
// `--audio-lang eng --audio-codec truehd` means "the English TrueHD track",
// not "anything English or anything TrueHD".
type TrackFilter struct {
	// Langs are ISO-639-2 codes as the source gives them ("eng", "fra"). The
	// special value "und" matches a track the source gave no language for,
	// which is what Matroska already calls undetermined.
	Langs []string
	// Best keeps only the single highest-quality track of those matching,
	// which is what "the best English track" means. Lossless beats lossy,
	// then more channels, then higher bitrate. It applies after the language
	// and codec filters, so the two compose: language eng plus Best is "the
	// best English track", not "the best track, if it happens to be English".
	Best bool
	// Codecs are matched against the track's stream ID and its human type,
	// case-insensitively, as substrings: "truehd" matches A_TRUEHD, and "dts"
	// matches both A_DTS and DTS-HD Master Audio. A substring is the right
	// shape here because the disc's own spelling varies and an operator
	// should not have to know which form the source used.
	Codecs []string
}

// Empty reports whether the filter would keep everything.
func (f TrackFilter) Empty() bool {
	return len(f.Langs) == 0 && len(f.Codecs) == 0 && !f.Best
}

// langAliases maps the spellings people and discs actually use onto the
// ISO-639-2 code a source gives.
//
// A Blu-ray has no way to say "Brazilian Portuguese" in ISO-639-2 — there is
// only "por" — so discs variously tag it "por", or use the non-standard "pob"
// or "ptb" that authoring tools emit. An operator asking for "pt-br" should
// not have to know which, nor which of them a given disc chose, so every
// spelling resolves to the same set and a filter for one matches them all.
var langAliases = map[string][]string{
	"pt":    {"por", "pob", "ptb"},
	"pt-br": {"por", "pob", "ptb"},
	"ptbr":  {"por", "pob", "ptb"},
	"pob":   {"por", "pob", "ptb"},
	"ptb":   {"por", "pob", "ptb"},
	"por":   {"por", "pob", "ptb"},
	"en":    {"eng"},
	"es":    {"spa", "esp"},
	"fr":    {"fra", "fre"},
	"de":    {"deu", "ger"},
	"it":    {"ita"},
	"ja":    {"jpn"},
	"zh":    {"zho", "chi"},
	// ISO-639-2 has both a bibliographic and a terminological code for
	// several languages, and sources disagree about which to use.
	"fra": {"fra", "fre"},
	"fre": {"fra", "fre"},
	"deu": {"deu", "ger"},
	"ger": {"deu", "ger"},
	"zho": {"zho", "chi"},
	"chi": {"zho", "chi"},
}

// expandLangs resolves each requested language to every spelling that means
// it, so the filter can be compared against whatever the disc said.
func expandLangs(langs []string) []string {
	var out []string
	for _, l := range langs {
		key := strings.ToLower(strings.TrimSpace(l))
		if key == "" {
			continue
		}
		if alts, ok := langAliases[key]; ok {
			out = append(out, alts...)
			continue
		}
		out = append(out, key)
	}
	return out
}

// Matches reports whether t survives the filter.
func (f TrackFilter) Matches(t Track) bool {
	if len(f.Langs) > 0 {
		lang := strings.ToLower(strings.TrimSpace(t.Lang))
		if lang == "" {
			lang = "und"
		}
		if !containsFold(expandLangs(f.Langs), lang) {
			return false
		}
	}
	if len(f.Codecs) > 0 {
		// Spelling aside: "truehd" finds a "TRUE-HD" track, as a Matroska
		// source names one (its codec ID being AC-3's).
		hay := strings.ToLower(t.StreamID + " " + t.Type)
		bare := alnum(hay)
		var hit bool
		for _, c := range f.Codecs {
			c = strings.ToLower(c)
			if c != "" && (strings.Contains(hay, c) || alnum(c) != "" && strings.Contains(bare, alnum(c))) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// alnum is s with only its letters and digits.
func alnum(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
}

func containsFold(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.EqualFold(strings.TrimSpace(h), needle) {
			return true
		}
	}
	return false
}

// ParseList splits a comma-separated flag value, dropping blanks. Returns nil
// for an empty value so a flag left unset is indistinguishable from absent.
func ParseList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Apply narrows a selection's audio and subtitle tracks.
//
// A filter that matches nothing is an error rather than a silent pass or a
// silent drop. Either behaviour would be defensible in isolation and both are
// wrong here: carrying every track on when the operator asked for one defeats
// the request, and dropping all audio produces a film nobody can watch after
// hours of conversion. Saying so up front, with the tracks the disc actually
// has, lets the request be corrected before any work is done.
func (s Selection) Apply(audio, subs TrackFilter) (Selection, error) {
	out := s
	if !audio.Empty() {
		kept := filterTracks(s.Audio, audio)
		if len(kept) == 0 && len(s.Audio) > 0 {
			return Selection{}, fmt.Errorf("no audio track matches %s; this source has %s",
				audio, describeTracks(s.Audio))
		}
		if audio.Best && len(kept) > 1 {
			if best, ok := BestAudio(kept); ok {
				kept = []Track{best}
			}
		}
		out.Audio = kept
	}
	if !subs.Empty() {
		kept := filterTracks(s.Subtitles, subs)
		if len(kept) == 0 && len(s.Subtitles) > 0 {
			return Selection{}, fmt.Errorf("no subtitle track matches %s; this source has %s",
				subs, describeTracks(s.Subtitles))
		}
		// Best is not applied to subtitles: they are picked by language, and
		// several are routinely wanted at once. Ranking PGS streams against
		// each other would mean nothing anyway.
		out.Subtitles = kept
	}
	return out, nil
}

func filterTracks(tracks []Track, f TrackFilter) []Track {
	var out []Track
	for _, t := range tracks {
		if f.Matches(t) {
			out = append(out, t)
		}
	}
	return out
}

// String renders a filter the way it was asked for, for an error message.
func (f TrackFilter) String() string {
	var parts []string
	if len(f.Langs) > 0 {
		parts = append(parts, "language "+strings.Join(f.Langs, "/"))
	}
	if len(f.Codecs) > 0 {
		parts = append(parts, "codec "+strings.Join(f.Codecs, "/"))
	}
	if f.Best {
		parts = append(parts, "the best of them")
	}
	if len(parts) == 0 {
		return "anything"
	}
	return strings.Join(parts, " and ")
}

// describeTracks lists what a source offers, so a filter that matched nothing
// can be corrected without a second run to go and look.
func describeTracks(tracks []Track) string {
	seen := map[string]bool{}
	var parts []string
	for _, t := range tracks {
		lang := strings.TrimSpace(t.Lang)
		if lang == "" {
			lang = "und"
		}
		d := fmt.Sprintf("%s (%s)", t.Type, lang)
		if !seen[d] {
			seen[d] = true
			parts = append(parts, d)
		}
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
