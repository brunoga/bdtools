package convert

import (
	"strings"
	"testing"
)

// A disc as tsMuxeR actually describes one: the info line carries the channel
// layout and, for lossy tracks, a bitrate. Lossless tracks often state no
// bitrate at all, which is why bitrate can only ever be a tie-breaker.
func lossless71() Track {
	return Track{ID: 4352, StreamID: "A_TRUEHD", Type: "TrueHD Atmos", Lang: "eng",
		Info: "Bitrate: 0Kbps  Sample Rate: 48KHz  Channels: 7.1"}
}

func dtsma51() Track {
	return Track{ID: 4353, StreamID: "A_DTS", Type: "DTS-HD Master Audio", Lang: "eng",
		Info: "Sample Rate: 48KHz  Channels: 5.1"}
}

func ac3_640() Track {
	return Track{ID: 4354, StreamID: "A_AC3", Type: "AC3", Lang: "eng",
		Info: "Bitrate: 640Kbps  Sample Rate: 48KHz  Channels: 5.1"}
}

// Lossless beats lossy outright: no bitrate of a lossy codec puts back what
// it threw away.
func TestLosslessBeatsLossy(t *testing.T) {
	if !betterAudio(dtsma51(), ac3_640()) {
		t.Error("DTS-HD MA 5.1 should beat AC3 5.1 at 640k")
	}
	// Even when the lossy track has more channels.
	wide := ac3_640()
	wide.Info = "Bitrate: 640Kbps  Channels: 7.1"
	if !betterAudio(dtsma51(), wide) {
		t.Error("lossless 5.1 should still beat lossy 7.1")
	}
}

// Within a tier, channels decide before bitrate: a 7.1 system is what the
// extra channels are for, and a fatter 5.1 mix cannot supply them.
func TestChannelsOutrankBitrate(t *testing.T) {
	narrow := Track{ID: 1, StreamID: "A_AC3", Type: "AC3", Info: "Bitrate: 640Kbps Channels: 5.1"}
	wide := Track{ID: 2, StreamID: "A_AC3", Type: "AC3", Info: "Bitrate: 192Kbps Channels: 7.1"}
	if !betterAudio(wide, narrow) {
		t.Error("7.1 at 192k should beat 5.1 at 640k within the same tier")
	}
}

func TestBitrateBreaksChannelTies(t *testing.T) {
	lo := Track{ID: 1, StreamID: "A_AC3", Type: "AC3", Info: "Bitrate: 192Kbps Channels: 5.1"}
	hi := Track{ID: 2, StreamID: "A_AC3", Type: "AC3", Info: "Bitrate: 640Kbps Channels: 5.1"}
	if !betterAudio(hi, lo) {
		t.Error("640k should beat 192k at equal channels")
	}
}

// An exact tie falls to the lower track number: the disc's own order, which
// conventionally puts the primary mix first, and which makes the choice
// deterministic so one disc always yields one answer.
func TestTiesFallToTheDiscOrder(t *testing.T) {
	a := Track{ID: 7, StreamID: "A_AC3", Type: "AC3", Info: "Channels: 5.1"}
	b := Track{ID: 3, StreamID: "A_AC3", Type: "AC3", Info: "Channels: 5.1"}
	if !betterAudio(b, a) {
		t.Error("an exact tie should fall to the lower track number")
	}
	if betterAudio(a, b) {
		t.Error("the comparison is not antisymmetric")
	}
}

// "dts-hd master" has to be tested before "dts", or a lossless track is filed
// as basic lossy and loses to an AC-3 stream.
func TestSpecificCodecNamesWinOverGeneralOnes(t *testing.T) {
	for _, c := range []struct {
		typ  string
		tier int
	}{
		{"DTS-HD Master Audio", tierLossless},
		{"DTS-HD MA", tierLossless},
		{"DTS-HD High Resolution Audio", tierHiLossy},
		{"DTS-HD HRA", tierHiLossy},
		{"DTS", tierLossy},
		{"TrueHD Atmos", tierLossless},
		{"TRUE-HD", tierLossless},
		{"LPCM", tierLossless},
		{"E-AC3", tierHiLossy},
		{"AC3", tierLossy},
		{"something unheard of", tierUnknown},
	} {
		if got := audioTier(Track{Type: c.typ}); got != c.tier {
			t.Errorf("%q classified as tier %d, want %d", c.typ, got, c.tier)
		}
	}
}

// The codec may be spelled in the stream ID, the human type, or both, and
// neither spelling is promised across sources or tsMuxeR builds.
func TestTierReadsEitherSpelling(t *testing.T) {
	if audioTier(Track{StreamID: "A_TRUEHD"}) != tierLossless {
		t.Error("stream ID alone should classify a TrueHD track")
	}
	if audioTier(Track{Type: "TrueHD Atmos"}) != tierLossless {
		t.Error("human type alone should classify a TrueHD track")
	}
}

// tsMuxeR states a layout, not a count: "7.1" is eight channels.
func TestChannelCountParsesTheLayout(t *testing.T) {
	for info, want := range map[string]int{
		"Channels: 7.1":                   8,
		"Bitrate: 640Kbps  Channels: 5.1": 6,
		"Channels: 2":                     2,
		"Channels: 1":                     1,
		"Sample Rate: 48KHz":              0,
		"channels: 7.1":                   8,
	} {
		if got := channelCount(Track{Info: info}); got != want {
			t.Errorf("channelCount(%q) = %d, want %d", info, got, want)
		}
	}
}

func TestBitrateParsing(t *testing.T) {
	for info, want := range map[string]int{
		"Bitrate: 640Kbps":  640,
		"Bitrate: 1411Kbps": 1411,
		"Bitrate: 0Kbps":    0,
		"Channels: 5.1":     0,
	} {
		if got := bitrateKbps(Track{Info: info}); got != want {
			t.Errorf("bitrateKbps(%q) = %d, want %d", info, got, want)
		}
	}
}

// The whole point: "the best English track" on a real disc.
func TestBestAudioPicksTheLosslessWidestTrack(t *testing.T) {
	got, ok := BestAudio([]Track{ac3_640(), lossless71(), dtsma51()})
	if !ok {
		t.Fatal("BestAudio found nothing in a non-empty list")
	}
	if got.ID != 4352 {
		t.Errorf("picked track %d (%s), want the TrueHD 7.1", got.ID, got.Type)
	}
	if _, ok := BestAudio(nil); ok {
		t.Error("BestAudio reported a track for an empty list")
	}
}

// The ranking's decision has to be legible in a log, since it is made on the
// operator's behalf.
func TestDescribeAudioNamesWhatWasChosen(t *testing.T) {
	got := DescribeAudio(lossless71())
	for _, want := range []string{"TrueHD Atmos", "8ch", "eng", "lossless"} {
		if !strings.Contains(got, want) {
			t.Errorf("describe = %q, want it to mention %q", got, want)
		}
	}
	// An untagged track is named "und" rather than left blank.
	if got := DescribeAudio(Track{Type: "AC3", Info: "Channels: 2"}); !strings.Contains(got, "und") {
		t.Errorf("describe = %q, want und for an untagged track", got)
	}
}

func TestCodecSlugIsFilesystemSafe(t *testing.T) {
	for _, c := range []struct{ typ, want string }{
		{"TrueHD Atmos", "TrueHD-Atmos"},
		{"DTS-HD Master Audio", "DTS-HD-Master-Audio"},
		{"AC3", "AC3"},
		{"AC-3 Dolby Surround EX", "AC-3-Dolby-Surround-EX"},
		{"E-AC3", "E-AC3"},
	} {
		if got := CodecSlug(Track{Type: c.typ}); got != c.want {
			t.Errorf("CodecSlug(%q) = %q, want %q", c.typ, got, c.want)
		}
	}
	// Falls back to the stream ID, then to something printable.
	if got := CodecSlug(Track{StreamID: "A_TRUEHD"}); got != "TRUEHD" {
		t.Errorf("slug from stream ID = %q, want TRUEHD", got)
	}
	if got := CodecSlug(Track{}); got != "audio" {
		t.Errorf("slug for an unnamed track = %q, want audio", got)
	}
	// Nothing that would need quoting in a shell or upset a filesystem.
	for _, bad := range []string{"/", "\\", " ", ":", "*", "?", "\""} {
		if strings.Contains(CodecSlug(Track{Type: "a/b c:d*e?f\"g\\h"}), bad) {
			t.Errorf("slug kept %q", bad)
		}
	}
}
