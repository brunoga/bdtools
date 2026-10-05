// Package esinfo reads the headers of the elementary streams a Blu-ray
// carries — enough to describe a track the way a listing does: codec
// flavour, channels, sample rate, bitrate, picture size and frame rate.
package esinfo

import (
	"fmt"
	"strings"
)

// Audio describes an audio stream from its first frames.
type Audio struct {
	// Codec is the human label: "AC3", "E-AC3 (DD+)", "TRUE-HD", "DTS",
	// "DTS-HD Master Audio", "DTS-HD High Resolution", "LPCM".
	Codec string
	// Channels is the channel count including the LFE; LFE says whether one
	// of them is the LFE, so 6 channels with LFE is "5.1".
	Channels   int
	LFE        bool
	SampleRate int
	// Bitrate is in kbit/s; for TrueHD it is the peak, with the AC-3 core's
	// in CoreBitrate. 0 when the stream does not say (lossless DTS, LPCM).
	Bitrate     int
	CoreBitrate int
	// Atmos is set for a TrueHD stream carrying the Atmos substream.
	Atmos bool
	// Core says which core a TrueHD or DTS-HD stream wraps ("AC3", "DTS").
	Core string
	// Bits is the sample size of LPCM.
	Bits int
}

// Layout renders the channel count as "7.1", "5.1", "2.0".
func (a Audio) Layout() string {
	if a.Channels == 0 {
		return ""
	}
	if a.LFE {
		return fmt.Sprintf("%d.1", a.Channels-1)
	}
	return fmt.Sprintf("%d.0", a.Channels)
}

// Describe renders the stream the way a track listing shows it.
func (a Audio) Describe() string {
	var parts []string
	switch {
	case a.Codec == "TRUE-HD":
		s := "AC3 core + TRUE-HD"
		if a.Atmos {
			s += " + ATMOS"
		}
		parts = append(parts, s+".")
		if a.Bitrate > 0 {
			parts = append(parts, fmt.Sprintf("Peak bitrate: %dKbps (core %dKbps)", a.Bitrate, a.CoreBitrate))
		}
	case strings.HasPrefix(a.Codec, "E-AC3"):
		if a.CoreBitrate > 0 && a.CoreBitrate != a.Bitrate {
			parts = append(parts, fmt.Sprintf("EAC3 Bitrate: %dKbps (core %dKbps)", a.Bitrate, a.CoreBitrate))
		} else {
			parts = append(parts, fmt.Sprintf("EAC3 Bitrate: %dKbps", a.Bitrate))
		}
	case a.Codec == "LPCM":
		parts = append(parts, fmt.Sprintf("Bits per sample: %d", a.Bits))
	default:
		if a.Bitrate > 0 {
			parts = append(parts, fmt.Sprintf("Bitrate: %dKbps", a.Bitrate))
		}
	}
	if a.SampleRate > 0 {
		parts = append(parts, fmt.Sprintf("Sample Rate: %dKHz", a.SampleRate/1000))
	}
	if l := a.Layout(); l != "" {
		parts = append(parts, "Channels: "+l)
	}
	return strings.Join(parts, " ")
}

// --- AC-3 / E-AC-3 ------------------------------------------------------------

var ac3Bitrates = [19]int{32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 448, 512, 576, 640}
var ac3Channels = [8]int{2, 1, 2, 3, 3, 4, 4, 5}
var ac3Rates = [4]int{48000, 44100, 32000, 0}

// ParseAC3 reads the first frame set of an AC-3 or E-AC-3 stream. An
// E-AC-3 7.1 track on a Blu-ray is an AC-3 (or E-AC-3) 5.1 independent
// substream plus an E-AC-3 dependent one carrying the extra channels, so
// every substream of the first frame set is counted. eac3 says the PMT
// announced E-AC-3, which decides the label when the first frame is plain
// AC-3.
func ParseAC3(b []byte, eac3 bool) (Audio, bool) {
	var a Audio
	a.Codec = "AC3"
	if eac3 {
		a.Codec = "E-AC3 (DD+)"
	}
	seen := map[int]bool{}
	i := syncAt(b, 0, 0x0b, 0x77)
	for i >= 0 && i+7 <= len(b) {
		bsid := b[i+5] >> 3
		var (
			size, kbps, rate, chans int
			lfe                     bool
			key                     int
			extra                   int
		)
		if bsid <= 10 {
			fscod := b[i+4] >> 6
			frmsizecod := int(b[i+4] & 0x3f)
			acmod := b[i+6] >> 5
			br := bitReader{b: b[i:], pos: 6*8 + 3}
			if acmod&1 != 0 && acmod != 1 {
				br.read(2)
			}
			if acmod&4 != 0 {
				br.read(2)
			}
			if acmod == 2 {
				br.read(2)
			}
			lfe = br.read(1) == 1
			rate = ac3Rates[fscod]
			if frmsizecod/2 < len(ac3Bitrates) && rate > 0 {
				kbps = ac3Bitrates[frmsizecod/2]
				// 16-bit words per 1536-sample frame (A/52 table 5.18); at
				// 44.1 kHz the odd codes carry one more word.
				words := kbps * 1000 * 1536 / (rate * 16)
				if rate == 44100 {
					words += frmsizecod & 1
				}
				size = 2 * words
			}
			chans = ac3Channels[acmod]
			key = 0 // the independent substream 0
		} else {
			br := bitReader{b: b[i:], pos: 16}
			strmtyp := br.read(2)
			substreamid := br.read(3)
			frmsiz := br.read(11)
			fscod := br.read(2)
			numblkscod := br.read(2)
			rate = ac3Rates[fscod]
			if fscod == 3 {
				rate = [4]int{24000, 22050, 16000, 0}[numblkscod]
				numblkscod = 3
			}
			acmod := br.read(3)
			lfe = br.read(1) == 1
			br.read(5) // bsid
			br.read(5) // dialnorm
			if br.read(1) == 1 {
				br.read(8)
			}
			if acmod == 0 {
				br.read(5)
				if br.read(1) == 1 {
					br.read(8)
				}
			}
			if strmtyp == 1 && br.read(1) == 1 {
				extra = eac3ChanmapChannels(br.read(16))
			}
			size = (int(frmsiz) + 1) * 2
			blocks := [4]int{1, 2, 3, 6}[numblkscod]
			if rate > 0 {
				kbps = size * 8 * rate / (blocks * 256) / 1000
			}
			chans = ac3Channels[acmod]
			key = int(strmtyp)<<3 | int(substreamid)
			if strmtyp == 1 {
				key = 0x100 | int(substreamid)
			}
		}
		if size <= 0 || rate <= 0 {
			break
		}
		if seen[key] {
			break // the frame set repeats: one period has been covered
		}
		seen[key] = true
		switch key {
		case 0: // independent substream 0: the main programme
			a.SampleRate = rate
			a.Channels = chans
			a.LFE = lfe
			if lfe {
				a.Channels++
			}
			a.CoreBitrate = kbps
			a.Bitrate += kbps
		case 0x100: // dependent substream 0: extra channels
			a.Channels += extra
			a.Bitrate += kbps
			a.Codec = "E-AC3 (DD+)"
		}
		i += size
		if i+2 > len(b) || b[i] != 0x0b || b[i+1] != 0x77 {
			break
		}
	}
	return a, a.SampleRate > 0
}

// eac3ChanmapChannels counts the channels a dependent substream's custom
// channel map adds to the independent substream's (A/52 Annex E, table
// E2.5). L, C, R, Ls, Rs and LFE replace channels the independent substream
// already has; the rest are new, and some locations are pairs.
func eac3ChanmapChannels(m uint32) int {
	n := 0
	for bit := 0; bit < 16; bit++ {
		if m&(1<<(15-bit)) == 0 {
			continue
		}
		switch bit {
		case 5, 6, 9, 10, 11, 13: // Lc/Rc, Lrs/Rrs, Lsd/Rsd, Lw/Rw, Lvh/Rvh, Lts/Rts
			n += 2
		case 7, 8, 12, 14: // Cs, Ts, Cvh, LFE2
			n++
		}
	}
	return n
}

// --- TrueHD ---------------------------------------------------------------------

var thdRates = map[uint32]int{0: 48000, 1: 96000, 2: 192000, 8: 44100, 9: 88200, 10: 176400}

// thdPairs marks the bits of the 8-channel assignment that stand for a
// pair of channels (ffmpeg's thd_layout).
var thdPairs = [13]int{2, 1, 1, 2, 2, 2, 2, 1, 1, 2, 2, 1, 1}

// ParseTrueHD reads a Blu-ray TrueHD stream: MLP access units with an
// AC-3 core interleaved. The major sync gives the channel assignment and
// the peak rate; the AC-3 frames give the core's.
func ParseTrueHD(b []byte) (Audio, bool) {
	a := Audio{Codec: "TRUE-HD", Core: "AC3"}
	if core, ok := ParseAC3(b, false); ok {
		a.CoreBitrate = core.Bitrate
	}
	// Find a major sync: 4-byte AU header then F8 72 6F BA.
	for i := 0; i+32 <= len(b); i++ {
		if b[i+4] != 0xf8 || b[i+5] != 0x72 || b[i+6] != 0x6f || b[i+7] != 0xba {
			continue
		}
		br := bitReader{b: b[i+8:], pos: 0}
		fs := br.read(4)
		br.read(4) // 6ch/8ch multichannel types, reserved
		br.read(2) // 2ch modifier
		br.read(2) // 6ch modifier
		br.read(5) // 6ch assignment
		br.read(2) // 8ch modifier
		assign := br.read(13)
		br.read(16) // signature
		br.read(16) // flags
		br.read(16) // reserved
		br.read(1)  // is_vbr
		peak := br.read(15)
		substreams := br.read(4)
		a.SampleRate = thdRates[fs]
		for bit, width := range thdPairs {
			if assign&(1<<bit) != 0 {
				a.Channels += width
				if bit == 2 {
					a.LFE = true
				}
			}
		}
		if a.SampleRate > 0 {
			a.Bitrate = int(peak) * a.SampleRate / 16 / 1000
		}
		a.Atmos = substreams >= 4
		return a, a.SampleRate > 0
	}
	return a, false
}

// --- DTS ------------------------------------------------------------------------

var dtsChannels = [16]int{1, 2, 2, 2, 2, 3, 3, 4, 4, 5, 6, 6, 6, 7, 8, 8}
var dtsRates = [16]int{0, 8000, 16000, 32000, 0, 0, 11025, 22050, 44100, 0, 0, 12000, 24000, 48000, 0, 0}
var dtsBitrates = [32]int{32, 56, 64, 96, 112, 128, 192, 224, 256, 320, 384, 448, 512, 576, 640, 768, 896, 1024, 1152, 1280, 1344, 1408, 1411, 1472, 1536, 1920, 2048, 3072, 3840, 0, 0, 0}
var dtsHDRates = [16]int{8000, 16000, 32000, 64000, 128000, 22050, 44100, 88200, 176400, 352800, 12000, 24000, 48000, 96000, 192000, 384000}

// ParseDTS reads a DTS core frame and, when one follows, the DTS-HD
// substream header: the extension's channel count and sample rate are what
// a Master Audio track really has, the core being a 5.1 downmix.
func ParseDTS(b []byte, hd string) (Audio, bool) {
	a := Audio{Codec: "DTS"}
	i := syncAt4(b, 0x7f, 0xfe, 0x80, 0x01)
	coreEnd := 0
	if i >= 0 && i+12 <= len(b) {
		br := bitReader{b: b[i+4:], pos: 0}
		br.read(1) // FTYPE
		br.read(5) // SHORT
		br.read(1) // CPF
		br.read(7) // NBLKS
		fsize := br.read(14) + 1
		amode := br.read(6)
		sfreq := br.read(4)
		rate := br.read(5)
		br.read(1) // fixed bit
		br.read(1) // DYNF
		br.read(1) // TIMEF
		br.read(1) // AUXF
		br.read(1) // HDCD
		br.read(3) // EXT_AUDIO_ID
		br.read(1) // EXT_AUDIO
		br.read(1) // ASPF
		lff := br.read(2)
		if amode < 16 {
			a.Channels = dtsChannels[amode]
		}
		a.SampleRate = dtsRates[sfreq&15]
		a.Bitrate = dtsBitrates[rate&31]
		if lff != 0 {
			a.LFE = true
			a.Channels++
		}
		a.Core = "DTS"
		coreEnd = i + int(fsize)
	}
	if hd == "" {
		return a, a.SampleRate > 0
	}
	a.Codec = hd
	// The substream header follows the core frame (or stands alone).
	j := syncAt4(b[min(coreEnd, len(b)):], 0x64, 0x58, 0x20, 0x25)
	if j < 0 {
		return a, a.SampleRate > 0
	}
	j += min(coreEnd, len(b))
	br := bitReader{b: b[j+4:], pos: 0}
	br.read(8) // UserDefinedBits
	ssIndex := br.read(2)
	headerSizeType := br.read(1)
	if headerSizeType == 0 {
		br.read(8)  // nuExtSSHeaderSize
		br.read(16) // nuExtSSFsize
	} else {
		br.read(12)
		br.read(20)
	}
	static := br.read(1) == 1
	numAssets := uint32(1)
	if static {
		br.read(2) // nuRefClockCode
		br.read(3) // nuExtSSFrameDurationCode
		if br.read(1) == 1 {
			br.read(32)
			br.read(4)
		}
		numPresent := br.read(3) + 1
		numAssets = br.read(3) + 1
		var masks []uint32
		for p := uint32(0); p < numPresent; p++ {
			masks = append(masks, br.read(int(ssIndex)+1))
		}
		for p := uint32(0); p < numPresent; p++ {
			for ss := uint32(0); ss <= ssIndex; ss++ {
				if masks[p]&(1<<ss) != 0 {
					br.read(8)
				}
			}
		}
		if br.read(1) == 1 { // bMixMetadataEnbl
			br.read(2)
			bits := br.read(2) + 4
			n := br.read(2) + 1
			for k := uint32(0); k < n; k++ {
				br.read(int(bits))
			}
		}
	}
	for k := uint32(0); k < numAssets; k++ {
		br.read(16) // nuAssetFsize
	}
	// First asset descriptor.
	br.read(9) // nuAssetDescriptFsize
	br.read(3) // nuAssetIndex
	if static {
		if br.read(1) == 1 {
			br.read(4)
		}
		if br.read(1) == 1 {
			br.read(24)
		}
		if br.read(1) == 1 {
			n := br.read(10) + 1
			for k := uint32(0); k < n; k++ {
				br.read(8)
			}
		}
		br.read(5) // nuBitResolution
		rate := br.read(4)
		chans := int(br.read(8)) + 1
		if !br.overrun() {
			a.SampleRate = dtsHDRates[rate]
			if chans > a.Channels {
				a.Channels = chans
			}
		}
	}
	a.Bitrate = 0 // variable; the core's rate would misdescribe it
	return a, a.SampleRate > 0
}

// --- LPCM -----------------------------------------------------------------------

var lpcmChannels = [16]int{0, 1, 0, 2, 3, 3, 4, 4, 5, 6, 7, 8, 0, 0, 0, 0}
var lpcmLFE = [16]bool{9: true, 11: true}
var lpcmRates = [16]int{1: 48000, 4: 96000, 5: 192000}

// ParseLPCM reads the 4-byte header of a Blu-ray LPCM PES payload.
func ParseLPCM(payload []byte) (Audio, bool) {
	if len(payload) < 4 {
		return Audio{}, false
	}
	ch := payload[2] >> 4
	rate := payload[2] & 15
	bits := payload[3] >> 6
	a := Audio{Codec: "LPCM", Channels: lpcmChannels[ch], LFE: lpcmLFE[ch], SampleRate: lpcmRates[rate]}
	a.Bits = [4]int{0, 16, 20, 24}[bits]
	return a, a.Channels > 0 && a.SampleRate > 0
}

// --- helpers --------------------------------------------------------------------

type bitReader struct {
	b   []byte
	pos int
	bad bool
}

func (r *bitReader) read(n int) uint32 {
	var v uint32
	for ; n > 0; n-- {
		if r.pos>>3 >= len(r.b) {
			r.bad = true
			return v << n
		}
		v = v<<1 | uint32(r.b[r.pos>>3]>>(7-r.pos&7)&1)
		r.pos++
	}
	return v
}

func (r *bitReader) overrun() bool { return r.bad }

func syncAt(b []byte, from int, a, c byte) int {
	for i := from; i+1 < len(b); i++ {
		if b[i] == a && b[i+1] == c {
			return i
		}
	}
	return -1
}

func syncAt4(b []byte, a, c, d, e byte) int {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == a && b[i+1] == c && b[i+2] == d && b[i+3] == e {
			return i
		}
	}
	return -1
}
