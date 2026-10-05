package mkv

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// AudioFormat is the content of a demuxed audio file.
type AudioFormat int

const (
	AC3    AudioFormat = iota // AC-3, or E-AC-3: whichever the frames are
	TrueHD                    // TrueHD access units, possibly with an AC-3 core interleaved
	DTS                       // DTS, or DTS-HD: a core frame then its extension substream
	WAV                       // PCM in a WAV file
)

// AudioSource reads a demuxed audio file frame by frame. A Blu-ray stream
// can carry a lossy core next to (or inside) its main coding — the AC-3
// frames between TrueHD's access units, the AC-3 independent substream of a
// 7.1 E-AC-3 track, the DTS core of DTS-HD — and Core selects which of the
// two this source produces.
type AudioSource struct {
	format AudioFormat
	core   bool
	r      *bufio.Reader
	track  Track
	t      time.Duration // presentation time of the next frame
	rate   int
	// For WAV.
	wavFrame int // bytes per sample frame
	wavLeft  int64
	samples  int64
}

// NewAudioSource probes the file. lang is the track's ISO 639-2 language.
func NewAudioSource(r io.Reader, format AudioFormat, core bool, lang string) (*AudioSource, error) {
	a := &AudioSource{format: format, core: core, r: bufio.NewReaderSize(r, 1<<20)}
	a.track = Track{Type: TypeAudio, Language: lang}
	switch format {
	case WAV:
		if err := a.readWAVHeader(); err != nil {
			return nil, err
		}
	default:
		head, _ := a.r.Peek(1 << 16)
		if err := a.describe(head); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// HasCore reports whether the stream carries a separate lossy core: the
// AC-3 frames of a TrueHD stream, the core of DTS-HD. (The AC-3 part of a
// 7.1 E-AC-3 track is not one: it is half of each E-AC-3 frame, and stays.)
func HasCore(r io.Reader, format AudioFormat) bool {
	b := make([]byte, 1<<16)
	n, _ := io.ReadFull(r, b)
	b = b[:n]
	switch format {
	case TrueHD:
		return indexAC3(b) >= 0
	case DTS:
		return len(b) > 4 && be32(b) == dtsSync && extAfterCore(b)
	}
	return false
}

// Track describes the track.
func (a *AudioSource) Track() Track { return a.track }

// SetDefault marks the track as the one a player picks by default.
func (a *AudioSource) SetDefault(d bool) { a.track.Default = d }

func (a *AudioSource) describe(head []byte) error {
	switch a.format {
	case AC3:
		h, ok := ac3Header(head)
		if !ok {
			return errors.New("mkv: no AC-3 frame")
		}
		a.rate = h.rate
		a.track.SampleRate, a.track.Channels = h.rate, h.channels
		a.track.CodecID = "A_AC3"
		if !a.core && (h.eac3 || hasDependent(head)) {
			a.track.CodecID = "A_EAC3"
			a.track.Channels = eac3Channels(head)
		}
	case TrueHD:
		if a.core {
			i := indexAC3(head)
			if i < 0 {
				return errors.New("mkv: no AC-3 core in the TrueHD stream")
			}
			h, _ := ac3Header(head[i:])
			a.rate = h.rate
			a.track.SampleRate, a.track.Channels, a.track.CodecID = h.rate, h.channels, "A_AC3"
			return nil
		}
		i := thdMajor(head)
		if i < 0 {
			return errors.New("mkv: no TrueHD major sync")
		}
		rate, ch := thdInfo(head[i:])
		a.rate = rate
		a.track.SampleRate, a.track.Channels, a.track.CodecID = rate, ch, "A_TRUEHD"
	case DTS:
		if len(head) < 16 || be32(head) != dtsSync {
			return errors.New("mkv: no DTS core frame")
		}
		rate, ch := dtsInfo(head)
		a.rate = rate
		a.track.SampleRate, a.track.Channels, a.track.CodecID = rate, ch, "A_DTS"
	}
	if a.rate == 0 {
		return fmt.Errorf("mkv: unknown sample rate")
	}
	return nil
}

// Next returns the next frame.
func (a *AudioSource) Next() (Frame, error) {
	switch a.format {
	case WAV:
		return a.nextWAV()
	case TrueHD:
		return a.nextTrueHD()
	case DTS:
		return a.nextDTS()
	}
	return a.nextAC3()
}

func (a *AudioSource) emit(data []byte, samples int) Frame {
	f := Frame{PTS: a.t, Order: a.t, Keyframe: true, Data: data}
	a.samples += int64(samples)
	a.t = time.Duration(a.samples * int64(time.Second) / int64(a.rate))
	return f
}

func (a *AudioSource) read(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(a.r, b); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.EOF // a truncated last frame is dropped
		}
		return nil, err
	}
	return b, nil
}

// --- AC-3 / E-AC-3 ----------------------------------------------------------------

type ac3Info struct {
	size, rate, channels, samples int
	eac3, dependent               bool
}

var ac3Rates = [4]int{48000, 44100, 32000, 0}
var ac3Kbps = [19]int{32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 448, 512, 576, 640}
var ac3Chans = [8]int{2, 1, 2, 3, 3, 4, 4, 5}

func ac3Header(b []byte) (ac3Info, bool) {
	if len(b) < 7 || b[0] != 0x0b || b[1] != 0x77 {
		return ac3Info{}, false
	}
	bsid := b[5] >> 3
	if bsid <= 10 {
		fscod := int(b[4] >> 6)
		code := int(b[4] & 0x3f)
		rate := ac3Rates[fscod]
		if rate == 0 || code/2 >= len(ac3Kbps) {
			return ac3Info{}, false
		}
		words := ac3Kbps[code/2] * 1000 * 1536 / (rate * 16)
		if rate == 44100 {
			words += code & 1
		}
		acmod := b[6] >> 5
		r := &bitReader{b: b, pos: 6*8 + 3}
		if acmod&1 != 0 && acmod != 1 {
			r.u(2)
		}
		if acmod&4 != 0 {
			r.u(2)
		}
		if acmod == 2 {
			r.u(2)
		}
		ch := ac3Chans[acmod]
		if r.flag() {
			ch++
		}
		return ac3Info{size: 2 * words, rate: rate, channels: ch, samples: 1536}, true
	}
	if bsid > 16 {
		return ac3Info{}, false
	}
	r := &bitReader{b: b, pos: 16}
	strm := r.u(2)
	r.u(3)
	frmsiz := int(r.u(11))
	fscod := int(r.u(2))
	blkcod := int(r.u(2))
	rate := ac3Rates[fscod]
	blocks := [4]int{1, 2, 3, 6}[blkcod]
	if fscod == 3 {
		rate = [4]int{24000, 22050, 16000, 0}[blkcod]
		blocks = 6
	}
	acmod := r.u(3)
	ch := ac3Chans[acmod]
	if r.flag() {
		ch++
	}
	return ac3Info{size: (frmsiz + 1) * 2, rate: rate, channels: ch, samples: blocks * 256, eac3: true, dependent: strm == 1}, rate > 0
}

// hasDependent reports whether an E-AC-3 dependent substream follows the
// first frame.
func hasDependent(b []byte) bool {
	h, ok := ac3Header(b)
	if !ok || h.size >= len(b) {
		return false
	}
	n, ok := ac3Header(b[h.size:])
	return ok && n.dependent
}

// eac3Channels counts the channels of a frame set: the independent
// substream's plus what its dependent substreams add.
func eac3Channels(b []byte) int {
	h, ok := ac3Header(b)
	if !ok {
		return 2
	}
	ch := h.channels
	for i := h.size; i+7 < len(b); {
		n, ok := ac3Header(b[i:])
		if !ok || !n.dependent {
			break
		}
		r := &bitReader{b: b[i:], pos: 16 + 2 + 3 + 11 + 2 + 2 + 3 + 1 + 5 + 5}
		if r.flag() {
			r.u(8)
		}
		if b[i+5]>>3 > 10 && (b[i+4]>>1)&7 == 0 { // acmod 0 (dual mono)
			r.u(5)
			if r.flag() {
				r.u(8)
			}
		}
		if r.flag() {
			m := r.u(16)
			for bit := 0; bit < 16; bit++ {
				if m&(1<<(15-bit)) == 0 {
					continue
				}
				switch bit {
				case 5, 6, 9, 10, 11, 13:
					ch += 2
				case 7, 8, 12, 14:
					ch++
				}
			}
		}
		i += n.size
	}
	return ch
}

// nextAC3 returns an access unit: an independent frame and the dependent
// frames that complete it (E-AC-3), or just the independent frame when the
// AC-3 core is wanted.
func (a *AudioSource) nextAC3() (Frame, error) {
	for {
		head, err := a.r.Peek(7)
		if err != nil {
			return Frame{}, io.EOF
		}
		h, ok := ac3Header(head)
		if !ok {
			if _, err := a.r.Discard(1); err != nil {
				return Frame{}, io.EOF
			}
			continue // resynchronise
		}
		data, err := a.read(h.size)
		if err != nil {
			return Frame{}, err
		}
		if h.dependent {
			continue // orphaned; belongs to a dropped frame
		}
		samples := h.samples
		for {
			next, err := a.r.Peek(7)
			if err != nil {
				break
			}
			d, ok := ac3Header(next)
			if !ok || !d.dependent {
				break
			}
			dep, err := a.read(d.size)
			if err != nil {
				break
			}
			if !a.core {
				data = append(data, dep...)
			}
		}
		return a.emit(data, samples), nil
	}
}

// --- TrueHD -------------------------------------------------------------------------

func indexAC3(b []byte) int {
	for i := 0; i+7 < len(b); i++ {
		if b[i] == 0x0b && b[i+1] == 0x77 {
			if h, ok := ac3Header(b[i:]); ok && !h.eac3 {
				return i
			}
		}
	}
	return -1
}

func thdMajor(b []byte) int {
	for i := 0; i+8 < len(b); i++ {
		if b[i+4] == 0xf8 && b[i+5] == 0x72 && b[i+6] == 0x6f && b[i+7] == 0xba {
			return i
		}
	}
	return -1
}

var thdRateCodes = map[byte]int{0: 48000, 1: 96000, 2: 192000, 8: 44100, 9: 88200, 10: 176400}
var thdPairs = [13]int{2, 1, 1, 2, 2, 2, 2, 1, 1, 2, 2, 1, 1}

// thdInfo reads a major sync: sample rate and channel count.
func thdInfo(b []byte) (int, int) {
	r := &bitReader{b: b[8:]}
	rate := thdRateCodes[byte(r.u(4))]
	r.u(4)
	r.u(2)
	r.u(2)
	r.u(5)
	r.u(2)
	assign := r.u(13)
	ch := 0
	for bit, w := range thdPairs {
		if assign&(1<<bit) != 0 {
			ch += w
		}
	}
	if ch == 0 {
		ch = 2
	}
	return rate, ch
}

// nextTrueHD returns one TrueHD access unit (or, for the core, one AC-3
// frame), skipping the other kind.
func (a *AudioSource) nextTrueHD() (Frame, error) {
	for {
		head, err := a.r.Peek(8)
		if err != nil {
			return Frame{}, io.EOF
		}
		if head[0] == 0x0b && head[1] == 0x77 {
			if h, ok := ac3Header(head); ok && a.plausibleNext(h.size) {
				data, err := a.read(h.size)
				if err != nil {
					return Frame{}, err
				}
				if a.core {
					return a.emit(data, h.samples), nil
				}
				continue
			}
		}
		size := int(binary.BigEndian.Uint16(head)&0x0fff) * 2
		if size < 4 {
			if _, err := a.r.Discard(1); err != nil {
				return Frame{}, io.EOF
			}
			continue
		}
		data, err := a.read(size)
		if err != nil {
			return Frame{}, err
		}
		if a.core {
			continue
		}
		// 40 samples at 48 kHz-family base rates, scaled for 96 and 192 kHz.
		samples := 40 * max(a.rate/48000, 1)
		if a.rate%44100 == 0 {
			samples = 40 * max(a.rate/44100, 1)
		}
		return a.emit(data, samples), nil
	}
}

// plausibleNext checks that a candidate frame of size n is followed by
// another frame or the end, which tells an AC-3 sync word from TrueHD data
// that happens to start with one.
func (a *AudioSource) plausibleNext(n int) bool {
	b, err := a.r.Peek(n + 2)
	if err != nil {
		return true // the end of the stream
	}
	nx := b[n:]
	if nx[0] == 0x0b && nx[1] == 0x77 {
		return true
	}
	return int(binary.BigEndian.Uint16(nx)&0x0fff)*2 >= 4
}

// --- DTS ------------------------------------------------------------------------------

const (
	dtsSync    = 0x7ffe8001
	dtsExtSync = 0x64582025
)

func be32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

var dtsRates = [16]int{0, 8000, 16000, 32000, 0, 0, 11025, 22050, 44100, 0, 0, 12000, 24000, 48000, 0, 0}
var dtsChans = [16]int{1, 2, 2, 2, 2, 3, 3, 4, 4, 5, 6, 6, 6, 7, 8, 8}

func dtsCore(b []byte) (size, samples int) {
	r := &bitReader{b: b[4:]}
	r.u(1)
	r.u(5)
	r.u(1)
	samples = (int(r.u(7)) + 1) * 32
	size = int(r.u(14)) + 1
	return
}

func dtsInfo(b []byte) (int, int) {
	r := &bitReader{b: b[4:]}
	r.u(1 + 5 + 1 + 7 + 14)
	amode := r.u(6)
	rate := dtsRates[r.u(4)]
	r.u(5 + 1 + 1 + 1 + 1 + 1 + 3 + 1 + 1)
	lfe := r.u(2)
	ch := 2
	if amode < 16 {
		ch = dtsChans[amode]
	}
	if lfe != 0 {
		ch++
	}
	return rate, ch
}

func extSize(b []byte) int {
	if len(b) < 10 || be32(b) != dtsExtSync {
		return 0
	}
	r := &bitReader{b: b[4:]}
	r.u(8)
	r.u(2)
	if r.u(1) == 0 {
		r.u(8)
		return int(r.u(16)) + 1
	}
	r.u(12)
	return int(r.u(20)) + 1
}

func extAfterCore(b []byte) bool {
	size, _ := dtsCore(b)
	return size+4 <= len(b) && be32(b[size:]) == dtsExtSync
}

// nextDTS returns a core frame with its extension substream, or the core
// alone when the core is wanted.
func (a *AudioSource) nextDTS() (Frame, error) {
	for {
		head, err := a.r.Peek(16)
		if err != nil {
			return Frame{}, io.EOF
		}
		if be32(head) != dtsSync {
			if n := extSize(head); n > 0 {
				if _, err := a.read(n); err != nil {
					return Frame{}, err
				}
				continue // an orphaned extension
			}
			if _, err := a.r.Discard(1); err != nil {
				return Frame{}, io.EOF
			}
			continue
		}
		size, samples := dtsCore(head)
		data, err := a.read(size)
		if err != nil {
			return Frame{}, err
		}
		if next, err := a.r.Peek(16); err == nil {
			if n := extSize(next); n > 0 {
				ext, err := a.read(n)
				if err == nil && !a.core {
					data = append(data, ext...)
				}
			}
		}
		return a.emit(data, samples), nil
	}
}

// --- WAV --------------------------------------------------------------------------------

func (a *AudioSource) readWAVHeader() error {
	hdr, err := a.read(12)
	if err != nil || string(hdr[:4]) != "RIFF" || string(hdr[8:]) != "WAVE" {
		return errors.New("mkv: not a WAV file")
	}
	var bits, channels, rate int
	for {
		ch, err := a.read(8)
		if err != nil {
			return errors.New("mkv: WAV file with no data")
		}
		size := int64(binary.LittleEndian.Uint32(ch[4:]))
		switch string(ch[:4]) {
		case "fmt ":
			f, err := a.read(int(size + size&1))
			if err != nil || len(f) < 16 {
				return errors.New("mkv: bad WAV format chunk")
			}
			channels = int(binary.LittleEndian.Uint16(f[2:]))
			rate = int(binary.LittleEndian.Uint32(f[4:]))
			bits = int(binary.LittleEndian.Uint16(f[14:]))
		case "data":
			if channels == 0 || bits == 0 || rate == 0 {
				return errors.New("mkv: WAV data before its format")
			}
			a.wavFrame = channels * bits / 8
			a.wavLeft = size
			if size == 0xffffffff || size == 0 {
				a.wavLeft = 1 << 62 // streamed: to the end of the file
			}
			a.rate = rate
			a.track.CodecID = "A_PCM/INT/LIT"
			a.track.SampleRate, a.track.Channels, a.track.BitDepth = rate, channels, bits
			return nil
		default:
			if _, err := a.r.Discard(int(size + size&1)); err != nil {
				return errors.New("mkv: WAV file with no data")
			}
		}
	}
}

// nextWAV returns 40 ms of samples.
func (a *AudioSource) nextWAV() (Frame, error) {
	frames := a.rate / 25
	n := int64(frames * a.wavFrame)
	if n > a.wavLeft {
		n = a.wavLeft - a.wavLeft%int64(a.wavFrame)
	}
	if n <= 0 {
		return Frame{}, io.EOF
	}
	b := make([]byte, n)
	m, _ := io.ReadFull(a.r, b)
	m -= m % a.wavFrame
	if m == 0 {
		return Frame{}, io.EOF
	}
	a.wavLeft -= int64(m)
	return a.emit(b[:m], m/a.wavFrame), nil
}
