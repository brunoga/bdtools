package convert

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// reMuxTrack matches a line of `mkvmerge -i` output:
// "Track ID 0: audio (TrueHD Atmos)".
var reMuxTrack = regexp.MustCompile(`^Track ID (\d+): (\S+) \(([^)]*)\)`)

// muxTrack is one track inside a demuxed file, as mkvmerge sees it.
type muxTrack struct {
	ID    int
	Kind  string // mkvmerge's word: "audio", "subtitles", "video"
	Codec string // mkvmerge's description: "TrueHD Atmos", "AC-3 Dolby Surround EX"
}

// identify asks mkvmerge what is inside a demuxed file.
//
// This is needed because a demuxed file is not reliably one track. A Blu-ray
// TrueHD stream carries an embedded AC-3 core as a fallback for players that
// cannot decode TrueHD, and tsMuxeR writes the pair as a single file — named
// ".ac3+thd", which mkvmerge then presents as two tracks. The same is true of
// DTS-HD, whose core is a plain DTS stream.
//
// Guessing is not good enough in either direction. Tagging only track 0 leaves
// the core untagged, so a player sees an audio track of unknown language next
// to the one it was told about. Dropping everything but track 0 without looking
// risks discarding the lossless stream and keeping the core, which is the
// opposite of what was asked for. mkvmerge is the tool that will do the mux, so
// its view is the one that matters, and asking it costs a process start.
func identify(ctx context.Context, bin, path string) ([]muxTrack, error) {
	out, err := runIdentify(ctx, bin, path)
	if err != nil {
		return nil, fmt.Errorf("identifying %s: %w", path, err)
	}
	var tracks []muxTrack
	for _, line := range strings.Split(out, "\n") {
		m := reMuxTrack.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		id, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			continue
		}
		tracks = append(tracks, muxTrack{ID: id, Kind: m[2], Codec: m[3]})
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("mkvmerge found no tracks in %s", path)
	}
	return tracks, nil
}

// primaryTrack picks the track in a demuxed file that corresponds to the disc
// track it was demuxed from, and reports the ones that are merely embedded
// alongside it.
//
// The match is on the codec, because that is what distinguishes a lossless
// stream from the lossy core riding inside it: a file demuxed from a TrueHD
// track holds "TrueHD Atmos" and "AC-3 Dolby Surround EX", and only one of
// those is the track the disc listed. When nothing matches — an unfamiliar
// codec, or a spelling neither tool shares — the first track is taken, which
// is what mkvmerge presents as the primary, and that is also the only track
// when a file holds one.
func primaryTrack(want Track, have []muxTrack) (primary muxTrack, extras []muxTrack) {
	if len(have) == 0 {
		return muxTrack{}, nil
	}
	best := -1
	for i, h := range have {
		if codecsAgree(want, h) {
			best = i
			break
		}
	}
	if best < 0 {
		best = 0
	}
	for i, h := range have {
		if i != best {
			extras = append(extras, h)
		}
	}
	return have[best], extras
}

// codecsAgree reports whether a track mkvmerge found is the one tsMuxeR
// described. The two name codecs differently — tsMuxeR says "TRUE-HD" where
// mkvmerge says "TrueHD Atmos" — so the comparison is on a squashed form with
// the separators removed, which makes those two meet.
func codecsAgree(want Track, have muxTrack) bool {
	w := squashCodec(want.Type)
	if w == "" {
		w = squashCodec(strings.TrimPrefix(want.StreamID, "A_"))
	}
	if w == "" {
		return false
	}
	h := squashCodec(have.Codec)
	return strings.Contains(h, w) || strings.Contains(w, h)
}

// squashCodec reduces a codec name to comparable letters and digits:
// "TRUE-HD" and "TrueHD Atmos" both start "truehd".
func squashCodec(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// runIdentify executes `mkvmerge -i`. Indirected so the track-selection logic
// can be tested without mkvmerge present, which no CI runner is guaranteed to
// have.
var runIdentify = func(ctx context.Context, bin, path string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, "-i", path).CombinedOutput() //nolint:gosec // bin came from LookPath
	return string(out), err
}
