package convert

import (
	"fmt"
	"strings"
)

// Track is one stream of a source.
type Track struct {
	// ID identifies the track in its source: the PID of a Blu-ray stream,
	// the track number of a Matroska one.
	ID int
	// Type is the human label ("H.264", "MVC", "AC3", "PGS").
	Type string
	// StreamID is the codec identifier ("V_MPEG4/ISO/AVC", "V_MPEG4/ISO/MVC",
	// "A_AC3"), in tsMuxeR's spelling, which the track listing and the
	// filters have always used.
	StreamID string
	// Info is the descriptive line, kept for reporting rather than parsed.
	Info string
	// Lang is the ISO-639 code, empty when the source does not say.
	Lang string
	// Name and Forced are the track's title and forced flag, when the source
	// has them (a Matroska file does; a disc does not).
	Name   string
	Forced bool
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
