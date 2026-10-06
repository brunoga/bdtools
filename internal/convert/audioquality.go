package convert

import (
	"regexp"
	"strconv"
	"strings"
)

// Audio quality tiers, best first. The distinction that matters on a Blu-ray
// is lossless versus not: a TrueHD or DTS-HD Master Audio track reconstructs
// the studio master exactly, while everything else has thrown information
// away, and no bitrate of a lossy codec makes up for that.
const (
	tierUnknown = iota
	tierLossy   // AC-3, DTS, AAC, MP2
	tierHiLossy // E-AC-3, DTS-HD High Resolution — lossy but above the basic tier
	tierLossless
)

// audioTiers maps a substring to the tier it implies, most specific first.
// Order is what makes it work: "dts-hd master" has to be tested before "dts",
// or a lossless track would be filed as basic lossy.
//
// Matching is by substring over the stream ID and the human type together,
// because the two spell codecs differently and neither spelling is promised:
// a disc's TrueHD track may read A_TRUEHD, "TRUE-HD" or "TrueHD Atmos"
// depending on the source.
var audioTiers = []struct {
	match string
	tier  int
}{
	{"dts-hd master", tierLossless},
	{"dts-hd ma", tierLossless},
	{"dts-hd high", tierHiLossy},
	{"dts-hd hr", tierHiLossy},
	{"dts-hd", tierHiLossy}, // unqualified DTS-HD: not lossless unless it says so
	{"truehd", tierLossless},
	{"true-hd", tierLossless},
	{"mlp", tierLossless},
	{"lpcm", tierLossless},
	{"flac", tierLossless},
	{"pcm", tierLossless},
	{"eac3", tierHiLossy},
	{"e-ac3", tierHiLossy},
	{"ac3", tierLossy},
	{"dts", tierLossy},
	{"aac", tierLossy},
	{"mp3", tierLossy},
	{"mp2", tierLossy},
}

// audioTier classifies a track's codec.
func audioTier(t Track) int {
	hay := strings.ToLower(t.StreamID + " " + t.Type)
	for _, e := range audioTiers {
		if strings.Contains(hay, e.match) {
			return e.tier
		}
	}
	return tierUnknown
}

var (
	// The listing writes "Channels: 7.1", "Channels: 5.1" or "Channels: 2" — a
	// layout, not a count.
	reChannels = regexp.MustCompile(`(?i)Channels:\s*(\d+)(?:\.(\d+))?`)
	reBitrate  = regexp.MustCompile(`(?i)Bitrate:\s*(\d+)\s*Kbps`)
)

// channelCount is how many channels a track carries, from the listing's layout
// string: "7.1" is eight, "5.1" is six, "2" is two. Zero when not stated.
func channelCount(t Track) int {
	m := reChannels.FindStringSubmatch(t.Info)
	if m == nil {
		return 0
	}
	main, _ := strconv.Atoi(m[1])
	lfe := 0
	if m[2] != "" {
		lfe, _ = strconv.Atoi(m[2])
	}
	return main + lfe
}

// bitrateKbps is the stated bitrate, or zero. A lossless track often states
// none, which is why it is only ever a tie-breaker within a tier.
func bitrateKbps(t Track) int {
	m := reBitrate.FindStringSubmatch(t.Info)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// betterAudio reports whether a is a better track than b.
//
// Lossless beats lossy outright; within a tier more channels win, and only
// then does bitrate decide. Channels are ranked above bitrate deliberately: a
// 7.1 track is what a 7.1 system is for, and a higher-bitrate 5.1 mix cannot
// put back the two channels it does not have.
//
// Ties fall to the lower track number, which is the order the disc listed
// them and conventionally puts the primary mix first. That also makes the
// choice deterministic, so the same disc always yields the same output.
func betterAudio(a, b Track) bool {
	if ta, tb := audioTier(a), audioTier(b); ta != tb {
		return ta > tb
	}
	if ca, cb := channelCount(a), channelCount(b); ca != cb {
		return ca > cb
	}
	if ba, bb := bitrateKbps(a), bitrateKbps(b); ba != bb {
		return ba > bb
	}
	return a.ID < b.ID
}

// BestAudio returns the best track of those given, and whether there was one.
func BestAudio(tracks []Track) (Track, bool) {
	if len(tracks) == 0 {
		return Track{}, false
	}
	best := tracks[0]
	for _, t := range tracks[1:] {
		if betterAudio(t, best) {
			best = t
		}
	}
	return best, true
}

// DescribeAudio names a track the way a log line should, so the choice the
// ranking made is visible rather than implicit.
func DescribeAudio(t Track) string {
	var b strings.Builder
	b.WriteString(t.Type)
	if c := channelCount(t); c > 0 {
		b.WriteString(" " + strconv.Itoa(c) + "ch")
	}
	lang := strings.TrimSpace(t.Lang)
	if lang == "" {
		lang = "und"
	}
	b.WriteString(" (" + lang + ")")
	if br := bitrateKbps(t); br > 0 {
		b.WriteString(" " + strconv.Itoa(br) + "kbps")
	}
	switch audioTier(t) {
	case tierLossless:
		b.WriteString(" lossless")
	case tierHiLossy:
		b.WriteString(" lossy/hi-res")
	}
	return b.String()
}

// CodecSlug is a short, filesystem-safe name for a track's codec, for putting
// in an output filename: "TrueHD Atmos" becomes "TrueHD-Atmos".
func CodecSlug(t Track) string {
	s := strings.TrimSpace(t.Type)
	if s == "" {
		s = strings.TrimPrefix(strings.TrimSpace(t.StreamID), "A_")
	}
	if s == "" {
		return "audio"
	}
	var out strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out.WriteRune(r)
			prevDash = false
		case r == '.' || r == '+':
			// Keep the shape of "5.1" and "DD+" readable without punctuation
			// a shell or a filesystem would rather not see.
			out.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && out.Len() > 0 {
				out.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(out.String(), "-")
}
