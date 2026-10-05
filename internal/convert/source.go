package convert

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Track is one stream tsMuxeR found in a source.
type Track struct {
	// ID is tsMuxeR's track number, which is what a demux meta file names.
	ID int
	// Type is the human label ("H.264", "MVC", "AC3", "PGS").
	Type string
	// StreamID is the codec identifier a meta file must repeat verbatim
	// ("V_MPEG4/ISO/AVC", "V_MPEG4/ISO/MVC", "A_AC3").
	StreamID string
	// Info is the descriptive line, kept for reporting rather than parsed.
	Info string
	// Lang is the ISO-639 code, empty when the source does not say.
	Lang string
}

// Kind classifies a track by what the conversion must do with it.
type Kind int

const (
	// KindOther is a track the conversion ignores.
	KindOther Kind = iota
	// KindBaseView is the AVC base view: the left eye, and a 2D-playable stream.
	KindBaseView
	// KindDependentView is the MVC dependent view: the right eye, which only
	// decodes against the base view.
	KindDependentView
	// KindAudio is an audio track, carried through to the output unchanged.
	KindAudio
	// KindSubtitle is a subtitle track, carried through unchanged.
	KindSubtitle
)

// Kind reports what the conversion does with this track.
//
// The base and dependent views are told apart by stream ID rather than by order
// or track number: a disc is not obliged to put them in any particular order,
// and picking the wrong one as the base yields a stream that cannot decode at
// all.
func (t Track) Kind() Kind {
	switch {
	case t.StreamID == "V_MPEG4/ISO/MVC":
		return KindDependentView
	case t.StreamID == "V_MPEG4/ISO/AVC":
		return KindBaseView
	case strings.HasPrefix(t.StreamID, "A_"):
		return KindAudio
	case strings.HasPrefix(t.StreamID, "S_"):
		return KindSubtitle
	}
	return KindOther
}

var (
	reDuration   = regexp.MustCompile(`^Duration:\s+(\d+):(\d\d):(\d\d)(?:\.(\d+))?`)
	reBaseView   = regexp.MustCompile(`(?i)^Base view:\s*(left|right)`)
	reTrackID    = regexp.MustCompile(`^Track ID:\s+(\d+)`)
	reStreamType = regexp.MustCompile(`^Stream type:\s+(.+)`)
	reStreamID   = regexp.MustCompile(`^Stream ID:\s+(.+)`)
	reStreamInfo = regexp.MustCompile(`^Stream info:\s*(.*)`)
	reStreamLang = regexp.MustCompile(`^Stream lang:\s*(.*)`)
)

// ParseDuration reads the "Duration: HH:MM:SS.mmm" line tsMuxeR prints after
// the track list. It is how a title's length is known without parsing the
// playlist format, which is what lets the main feature be told from a trailer.
// Returns 0 when the source does not say.
func ParseDuration(out string) time.Duration {
	for _, line := range strings.Split(out, "\n") {
		m := reDuration.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		se, _ := strconv.Atoi(m[3])
		d := time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(se)*time.Second
		// The fraction matters only for a test fixture a few frames long, but
		// dropping it would make such a source indistinguishable from one of
		// unknown length.
		if m[4] != "" {
			ms, _ := strconv.Atoi((m[4] + "000")[:3])
			d += time.Duration(ms) * time.Millisecond
		}
		return d
	}
	return 0
}

// BaseViewIsRightEye reads the "Base view:" line tsMuxeR prints for a 3D
// playlist. Most discs put the left eye in the base view; some do not, and the
// difference is the difference between 3D and a headache.
//
// The decoder stacks base-left unconditionally, so a right-eye base view has to
// be swapped afterwards. Reading it from the disc means the operator does not
// have to watch the result to find out.
func BaseViewIsRightEye(out string) (isRight, known bool) {
	for _, line := range strings.Split(out, "\n") {
		m := reBaseView.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		return strings.EqualFold(m[1], "right"), true
	}
	return false, false
}

// ParseListing reads what tsMuxeR prints when handed a source with no meta
// file: one block per track, the blocks separated by blank lines.
//
// A source with no "Track ID" lines is an elementary stream rather than a
// container — tsMuxeR describes it but has nothing to demux — and that is an
// error here, because the conversion needs the views as separate tracks.
func ParseListing(out string) ([]Track, error) {
	var (
		tracks []Track
		cur    Track
		open   bool
	)
	flush := func() {
		if open {
			tracks = append(tracks, cur)
		}
		cur, open = Track{}, false
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case reTrackID.MatchString(line):
			flush()
			id, _ := strconv.Atoi(reTrackID.FindStringSubmatch(line)[1])
			cur.ID, open = id, true
		case reStreamType.MatchString(line):
			cur.Type = strings.TrimSpace(reStreamType.FindStringSubmatch(line)[1])
		case reStreamID.MatchString(line):
			cur.StreamID = strings.TrimSpace(reStreamID.FindStringSubmatch(line)[1])
		case reStreamInfo.MatchString(line):
			cur.Info = strings.TrimSpace(reStreamInfo.FindStringSubmatch(line)[1])
		case reStreamLang.MatchString(line):
			cur.Lang = strings.TrimSpace(reStreamLang.FindStringSubmatch(line)[1])
		}
	}
	flush()
	if len(tracks) == 0 {
		return nil, fmt.Errorf("tsMuxeR listed no tracks — the source looks like an " +
			"elementary stream rather than a disc, playlist or m2ts")
	}
	return tracks, nil
}

// Selection is the set of tracks a conversion will use.
type Selection struct {
	Base      Track
	Dependent Track
	Audio     []Track
	Subtitles []Track
}

// SelectTracks picks what the conversion needs from a listing.
//
// Both views are required and each must appear exactly once: a source with no
// MVC track is not 3D, and one with several is something this does not
// understand well enough to guess at. Audio and subtitles are carried through
// in the order the source lists them, so the first audio track stays first.
func SelectTracks(tracks []Track) (Selection, error) {
	var (
		sel         Selection
		bases, deps []Track
	)
	for _, t := range tracks {
		switch t.Kind() {
		case KindBaseView:
			bases = append(bases, t)
		case KindDependentView:
			deps = append(deps, t)
		case KindAudio:
			sel.Audio = append(sel.Audio, t)
		case KindSubtitle:
			sel.Subtitles = append(sel.Subtitles, t)
		}
	}
	switch {
	case len(deps) == 0:
		return Selection{}, fmt.Errorf("no MVC track: this source is not 3D "+
			"(found %s)", describeVideo(tracks))
	case len(deps) > 1:
		return Selection{}, fmt.Errorf("found %d MVC tracks; expected one", len(deps))
	case len(bases) == 0:
		return Selection{}, fmt.Errorf("found an MVC dependent view but no AVC base " +
			"view, which cannot be decoded on its own")
	case len(bases) > 1:
		return Selection{}, fmt.Errorf("found %d AVC tracks; cannot tell which is the "+
			"base view of the MVC pair", len(bases))
	}
	sel.Base, sel.Dependent = bases[0], deps[0]
	return sel, nil
}

// describeVideo summarises a listing's video tracks, so "not 3D" says what was
// there instead.
func describeVideo(tracks []Track) string {
	var parts []string
	for _, t := range tracks {
		if strings.HasPrefix(t.StreamID, "V_") {
			parts = append(parts, fmt.Sprintf("%s (track %d)", t.StreamID, t.ID))
		}
	}
	if len(parts) == 0 {
		return "no video tracks at all"
	}
	return strings.Join(parts, ", ")
}

// RemuxMeta renders the meta file tsMuxeR reads to write the selected tracks
// straight back out as a stream, with no re-encoding.
//
// This is the demux meta without --demux: the same track references, pointed
// at an output file instead of a directory. The MVC pair passes through
// untouched, so the result holds the disc's own video bit for bit — the only
// thing lost is the tracks that were filtered out, which is the point.
//
// insertSEI and contSPS are deliberately absent. They rebuild picture timing
// and repeat parameter sets so an extracted elementary stream can stand alone,
// which is what a demux needs; here the stream stays in a container that
// carries them, and asking for them would be rewriting video that is supposed
// to pass through unaltered.
func RemuxMeta(input string, sel Selection) string {
	var b strings.Builder
	// A Blu-ray's own muxing conventions: the video PID carries no PCR, audio
	// gets fresh PES headers, and the bitrate is variable with a VBV window
	// the size a player expects. Without these a player that is strict about
	// Blu-ray stream structure can refuse the result.
	b.WriteString("MUXOPT --no-pcr-on-video-pid --new-audio-pes --vbr --vbv-len=500\n")
	fmt.Fprintf(&b, "%s, \"%s\", track=%d\n", sel.Base.StreamID, input, sel.Base.ID)
	fmt.Fprintf(&b, "%s, \"%s\", track=%d\n", sel.Dependent.StreamID, input, sel.Dependent.ID)
	for _, t := range append(append([]Track(nil), sel.Audio...), sel.Subtitles...) {
		fmt.Fprintf(&b, "%s, \"%s\", track=%d", t.StreamID, input, t.ID)
		if t.Lang != "" {
			fmt.Fprintf(&b, ", lang=%s", t.Lang)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// DemuxMeta renders the meta file tsMuxeR reads to extract the selected tracks.
//
// insertSEI and contSPS on the base view are what a Blu-ray rip needs: the
// picture timing and the repeated parameter sets are rebuilt, so the extracted
// stream stands on its own rather than depending on what the container carried.
func DemuxMeta(input string, sel Selection) string {
	var b strings.Builder
	b.WriteString("MUXOPT --demux\n")
	fmt.Fprintf(&b, "%s, \"%s\", track=%d, insertSEI, contSPS\n", sel.Base.StreamID, input, sel.Base.ID)
	fmt.Fprintf(&b, "%s, \"%s\", track=%d, insertSEI, contSPS\n", sel.Dependent.StreamID, input, sel.Dependent.ID)
	for _, t := range append(append([]Track(nil), sel.Audio...), sel.Subtitles...) {
		fmt.Fprintf(&b, "%s, \"%s\", track=%d", t.StreamID, input, t.ID)
		if t.Lang != "" {
			fmt.Fprintf(&b, ", lang=%s", t.Lang)
		}
		b.WriteString("\n")
	}
	return b.String()
}
