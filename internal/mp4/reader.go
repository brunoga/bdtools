package mp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
)

// Reader reads an MP4 or QuickTime file's samples in file order, each as
// a Matroska-style packet, its tracks described as a Matroska reader
// describes them (codec ids, avcC or hvcC as the codec private data), so
// the conversion takes either alike.
//
// Video: H.264 (avc1, avc3), HEVC (hvc1, hev1; MV-HEVC's second layer
// with it, lhvC), Dolby Vision's configuration. Audio: AC-3, E-AC-3, AAC
// (described, for the caller to leave or code), and PCM (sowt, twos,
// in24, in32, lpcm), given little-endian, a chunk a packet. The edit
// list's start is kept: a track that starts late does.
type Reader struct {
	f        io.ReadSeeker
	Tracks   []mkv.ReadTrack
	Duration time.Duration
	// LayerParams is, for an MV-HEVC track, its second layer's parameter
	// sets (lhvC) as Annex B NAL units.
	LayerParams map[uint64][]byte
	// Stereo is, for a video track Apple's stereo boxes describe, what
	// they say: "both" when vexu's eyes box names both eyes, "one" when it
	// names one.
	Stereo map[uint64]string

	tracks []*inTrack
	order  []sampleRef
	next   int
}

type inSample struct {
	off  int64
	size int
	dts  int64 // in the track's timescale
	cts  int64 // composition offset
	key  bool
}

type inTrack struct {
	rt        *mkv.ReadTrack
	timescale int64
	samples   []inSample
	// shift is subtracted from a sample's composition time (the edit
	// list's media time), delay added after (an empty edit's length).
	shift int64
	delay time.Duration
	// pcm: bytes a sample frame and whether they come big-endian.
	pcmFrame int
	pcmBig   bool
}

// chunkRun is an stsc entry: from chunk first (1-based) on, per samples
// a chunk.
type chunkRun struct{ first, per int }

type sampleRef struct {
	t, i int
	off  int64
}

// box is a parsed box header: its type and body.
type rbox struct {
	typ  string
	body []byte
}

// children splits a box body into boxes.
func children(b []byte) []rbox {
	var out []rbox
	for len(b) >= 8 {
		size := int64(binary.BigEndian.Uint32(b))
		typ := string(b[4:8])
		hdr := int64(8)
		switch size {
		case 1:
			if len(b) < 16 {
				return out
			}
			size, hdr = int64(binary.BigEndian.Uint64(b[8:])), 16 //nolint:gosec // checked below
		case 0:
			size = int64(len(b))
		}
		if size < hdr || size > int64(len(b)) {
			return out
		}
		out = append(out, rbox{typ, b[hdr:size]})
		b = b[size:]
	}
	return out
}

func find(bs []rbox, typ string) []byte {
	for _, b := range bs {
		if b.typ == typ {
			return b.body
		}
	}
	return nil
}

// NewReader reads the file's movie box.
func NewReader(f io.ReadSeeker) (*Reader, error) {
	moov, err := readMoov(f)
	if err != nil {
		return nil, err
	}
	r, err := ParseMovie(moov)
	if err != nil {
		return nil, err
	}
	r.f = f
	return r, nil
}

// MovieAt finds and reads the movie box of a file of size bytes through
// r, reading only box headers on the way: with the movie box at the end
// (after the media data), the file's first and last pieces are enough.
func MovieAt(r io.ReaderAt, size int64) ([]byte, error) {
	var pos int64
	hdr := make([]byte, 16)
	for pos+8 <= size {
		if _, err := r.ReadAt(hdr[:8], pos); err != nil {
			return nil, err
		}
		bsize := int64(binary.BigEndian.Uint32(hdr))
		typ := string(hdr[4:8])
		h := int64(8)
		if bsize == 1 {
			if _, err := r.ReadAt(hdr[8:16], pos+8); err != nil {
				return nil, err
			}
			bsize, h = int64(binary.BigEndian.Uint64(hdr[8:])), 16 //nolint:gosec // a box size
		}
		if bsize == 0 {
			bsize = size - pos
		}
		if pos == 0 && typ != "ftyp" && typ != "moov" && typ != "wide" && typ != "free" && typ != "mdat" && typ != "skip" {
			return nil, errors.New("mp4: not an MP4 or QuickTime file")
		}
		if bsize < h || pos+bsize > size {
			return nil, errors.New("mp4: a bad box")
		}
		if typ == "moov" {
			if bsize-h > 1<<30 {
				return nil, errors.New("mp4: a bad movie box")
			}
			b := make([]byte, bsize-h)
			if _, err := r.ReadAt(b, pos+h); err != nil && (!errors.Is(err, io.EOF) || pos+bsize != size) {
				return nil, err
			}
			return b, nil
		}
		pos += bsize
	}
	return nil, errors.New("mp4: no movie box")
}

// ParseMovie describes a file from its movie box alone: its tracks, with
// no samples to read (Next fails).
func ParseMovie(moov []byte) (*Reader, error) {
	r := &Reader{LayerParams: map[uint64][]byte{}, Stereo: map[uint64]string{}}
	mbs := children(moov)
	movieScale := int64(1000)
	if mvhd := find(mbs, "mvhd"); len(mvhd) >= 20 {
		if mvhd[0] == 1 && len(mvhd) >= 32 {
			movieScale = int64(binary.BigEndian.Uint32(mvhd[20:]))
			r.Duration = scaled(int64(binary.BigEndian.Uint64(mvhd[24:])), movieScale) //nolint:gosec // a duration
		} else {
			movieScale = int64(binary.BigEndian.Uint32(mvhd[12:]))
			r.Duration = scaled(int64(binary.BigEndian.Uint32(mvhd[16:])), movieScale)
		}
	}
	for _, b := range mbs {
		if b.typ != "trak" {
			continue
		}
		t, err := r.parseTrak(b.body, movieScale)
		if err != nil {
			return nil, err
		}
		if t != nil {
			r.tracks = append(r.tracks, t)
		}
	}
	for ti, t := range r.tracks {
		r.Tracks = append(r.Tracks, *t.rt)
		for i, s := range t.samples {
			r.order = append(r.order, sampleRef{ti, i, s.off})
		}
	}
	sort.SliceStable(r.order, func(i, j int) bool { return r.order[i].off < r.order[j].off })
	for i := range r.tracks {
		r.tracks[i].rt = &r.Tracks[i]
	}
	if len(r.Tracks) == 0 {
		return nil, errors.New("mp4: no track this reads")
	}
	return r, nil
}

func scaled(v, scale int64) time.Duration {
	if scale <= 0 {
		return 0
	}
	return time.Duration(float64(v) * float64(time.Second) / float64(scale))
}

// readMoov finds and reads the movie box, wherever it is.
func readMoov(f io.ReadSeeker) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var pos int64
	hdr := make([]byte, 16)
	for {
		if _, err := io.ReadFull(f, hdr[:8]); err != nil {
			return nil, errors.New("mp4: no movie box")
		}
		size := int64(binary.BigEndian.Uint32(hdr))
		typ := string(hdr[4:8])
		h := int64(8)
		if size == 1 {
			if _, err := io.ReadFull(f, hdr[8:16]); err != nil {
				return nil, err
			}
			size, h = int64(binary.BigEndian.Uint64(hdr[8:])), 16 //nolint:gosec // a box size
		}
		if pos == 0 && typ != "ftyp" && typ != "moov" && typ != "wide" && typ != "free" && typ != "mdat" && typ != "skip" {
			return nil, errors.New("mp4: not an MP4 or QuickTime file")
		}
		if typ == "moov" {
			if size == 0 || size < h || size-h > 1<<30 {
				return nil, errors.New("mp4: a bad movie box")
			}
			b := make([]byte, size-h)
			if _, err := io.ReadFull(f, b); err != nil {
				return nil, err
			}
			return b, nil
		}
		if size == 0 {
			return nil, errors.New("mp4: no movie box")
		}
		if size < h {
			return nil, errors.New("mp4: a bad box")
		}
		pos += size
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
	}
}

// language decodes mdhd's packed ISO 639-2/T code ("und" for none).
func language(v uint16) string {
	if v < 0x400 || v == 0x7fff { // QuickTime's Macintosh codes, or none
		return "und"
	}
	return string([]byte{byte(v>>10&31) + 0x60, byte(v>>5&31) + 0x60, byte(v&31) + 0x60})
}

func (r *Reader) parseTrak(body []byte, movieScale int64) (*inTrack, error) {
	tbs := children(body)
	tkhd := find(tbs, "tkhd")
	mdia := children(find(tbs, "mdia"))
	mdhd := find(mdia, "mdhd")
	hdlr := find(mdia, "hdlr")
	stbl := children(find(children(find(mdia, "minf")), "stbl"))
	if len(tkhd) < 20 || len(mdhd) < 20 || len(hdlr) < 12 || stbl == nil {
		return nil, nil
	}
	t := &inTrack{rt: &mkv.ReadTrack{}}
	rt := t.rt
	if tkhd[0] == 1 {
		rt.Number = uint64(binary.BigEndian.Uint32(tkhd[20:]))
	} else {
		rt.Number = uint64(binary.BigEndian.Uint32(tkhd[12:]))
	}
	if n := len(tkhd); n >= 8 {
		rt.DisplayWidth = int(binary.BigEndian.Uint32(tkhd[n-8:]) >> 16)
		rt.DisplayHeight = int(binary.BigEndian.Uint32(tkhd[n-4:]) >> 16)
	}
	var lang uint16
	if mdhd[0] == 1 {
		if len(mdhd) < 34 {
			return nil, nil
		}
		t.timescale = int64(binary.BigEndian.Uint32(mdhd[20:]))
		lang = binary.BigEndian.Uint16(mdhd[32:])
	} else {
		t.timescale = int64(binary.BigEndian.Uint32(mdhd[12:]))
		lang = binary.BigEndian.Uint16(mdhd[20:])
	}
	rt.Language = language(lang)
	if t.timescale <= 0 {
		return nil, nil
	}
	handler := string(hdlr[8:12])
	stsd := find(stbl, "stsd")
	if len(stsd) < 16 {
		return nil, nil
	}
	entries := children(stsd[8:])
	if len(entries) == 0 {
		return nil, nil
	}
	e := entries[0]
	switch handler {
	case "vide":
		if !r.videoEntry(t, e) {
			return nil, nil
		}
	case "soun":
		if !audioEntry(t, e) {
			return nil, nil
		}
	default:
		return nil, nil
	}
	if err := t.sampleTable(stbl); err != nil {
		return nil, fmt.Errorf("mp4: track %d: %w", rt.Number, err)
	}
	// The edit list: an empty edit delays the track, a media time skips
	// into it.
	if edts := children(find(tbs, "edts")); edts != nil {
		elst := find(edts, "elst")
		if len(elst) >= 8 {
			n := int(binary.BigEndian.Uint32(elst[4:]))
			e := elst[8:]
			for range n {
				var dur, mt int64
				if elst[0] == 1 {
					if len(e) < 20 {
						break
					}
					dur, mt = int64(binary.BigEndian.Uint64(e)), int64(binary.BigEndian.Uint64(e[8:])) //nolint:gosec // edit values
					e = e[20:]
				} else {
					if len(e) < 12 {
						break
					}
					dur, mt = int64(binary.BigEndian.Uint32(e)), int64(int32(binary.BigEndian.Uint32(e[4:]))) //nolint:gosec // signed media time
					e = e[12:]
				}
				if mt == -1 {
					t.delay += scaled(dur, movieScale)
					continue
				}
				t.shift = mt
				break
			}
		}
	}
	return t, nil
}

func (r *Reader) videoEntry(t *inTrack, e rbox) bool {
	rt := t.rt
	rt.Type = mkv.TypeVideo
	b := e.body
	if len(b) < 78 {
		return false
	}
	rt.Width, rt.Height = int(binary.BigEndian.Uint16(b[24:])), int(binary.BigEndian.Uint16(b[26:]))
	kids := children(b[78:])
	switch e.typ {
	case "avc1", "avc3":
		rt.CodecID, rt.CodecPrivate = "V_MPEG4/ISO/AVC", find(kids, "avcC")
	case "hvc1", "hev1", "dvh1", "dvhe":
		rt.CodecID, rt.CodecPrivate = "V_MPEGH/ISO/HEVC", find(kids, "hvcC")
		if l := find(kids, "lhvC"); l != nil {
			r.LayerParams[rt.Number] = lhvcParams(l)
		}
	case "av01":
		rt.CodecID, rt.CodecPrivate = "V_AV1", find(kids, "av1C")
	default:
		// Described, for a probe, but not read: its codec id is its four
		// characters.
		rt.CodecID = "V_FOURCC/" + e.typ
		return true
	}
	if rt.CodecPrivate == nil {
		return false
	}
	for _, k := range kids {
		switch k.typ {
		case "dvcC", "dvvC":
			rt.BlockAdditions = append(rt.BlockAdditions, mkv.BlockAddition{Type: mkv.FourCC(k.typ), ExtraData: k.body})
		case "vexu":
			r.Stereo[rt.Number] = vexuEyes(k.body)
		case "st3d": // Google's spherical metadata: 1 over-under, 2 side by side
			if len(k.body) >= 5 {
				rt.StereoMode = map[byte]int{1: 3, 2: 1}[k.body[4]]
			}
		}
	}
	return true
}

// vexuEyes reads Apple's video extended usage box: "both" when its eyes
// box's stereo view information names both eyes, "one" otherwise.
func vexuEyes(b []byte) string {
	eyes := children(find(children(b), "eyes"))
	stri := find(eyes, "stri")
	if len(stri) >= 5 && stri[4]&3 == 3 { // has left and right eye
		return "both"
	}
	return "one"
}

// lhvcParams gives an lhvC record's parameter sets as Annex B NAL units.
func lhvcParams(b []byte) []byte {
	if len(b) < 6 {
		return nil
	}
	var out []byte
	n := int(b[5])
	p := b[6:]
	for range n {
		if len(p) < 3 {
			break
		}
		cnt := int(binary.BigEndian.Uint16(p[1:]))
		p = p[3:]
		for range cnt {
			if len(p) < 2 {
				return out
			}
			l := int(binary.BigEndian.Uint16(p))
			if len(p) < 2+l {
				return out
			}
			out = append(out, 0, 0, 0, 1)
			out = append(out, p[2:2+l]...)
			p = p[2+l:]
		}
	}
	return out
}

func audioEntry(t *inTrack, e rbox) bool {
	rt := t.rt
	rt.Type = mkv.TypeAudio
	b := e.body
	if len(b) < 28 {
		return false
	}
	version := binary.BigEndian.Uint16(b[8:])
	rt.Channels = int(binary.BigEndian.Uint16(b[16:]))
	rt.BitDepth = int(binary.BigEndian.Uint16(b[18:]))
	rt.SampleRate = float64(binary.BigEndian.Uint32(b[24:]) >> 16)
	kidsAt := 28
	lpcmFlags := uint32(0)
	switch version {
	case 1:
		kidsAt = 44
	case 2:
		if len(b) < 64 {
			return false
		}
		rt.SampleRate = math.Float64frombits(binary.BigEndian.Uint64(b[32:]))
		rt.Channels = int(binary.BigEndian.Uint32(b[40:]))
		rt.BitDepth = int(binary.BigEndian.Uint32(b[48:]))
		lpcmFlags = binary.BigEndian.Uint32(b[52:])
		kidsAt = 64
	}
	if kidsAt > len(b) {
		kidsAt = len(b)
	}
	kids := children(b[kidsAt:])
	switch e.typ {
	case "ac-3":
		rt.CodecID = "A_AC3"
	case "ec-3":
		rt.CodecID = "A_EAC3"
	case "mp4a":
		rt.CodecID = "A_AAC"
		rt.CodecPrivate = find(kids, "esds")
	case "sowt", "twos", "in24", "in32", "lpcm":
		rt.CodecID = "A_PCM/INT/LIT"
		switch e.typ {
		case "in24":
			rt.BitDepth = 24
		case "in32":
			rt.BitDepth = 32
		}
		big := e.typ == "twos" || e.typ == "in24" || e.typ == "in32"
		if e.typ == "lpcm" {
			if lpcmFlags&1 != 0 { // float
				return false
			}
			big = lpcmFlags&2 != 0
		}
		// in24 and in32 may say little-endian in an enda box.
		if enda := find(kids, "enda"); len(enda) >= 2 && binary.BigEndian.Uint16(enda) != 0 {
			big = false
		}
		if rt.BitDepth == 0 || rt.Channels == 0 {
			return false
		}
		t.pcmFrame, t.pcmBig = rt.Channels*rt.BitDepth/8, big
	case "fLaC":
		rt.CodecID = "A_FLAC"
	case "Opus":
		rt.CodecID = "A_OPUS"
	default:
		rt.CodecID = "A_FOURCC/" + e.typ
	}
	return true
}

// sampleTable builds the track's samples from its sample table.
func (t *inTrack) sampleTable(stbl []rbox) error {
	stts := find(stbl, "stts")
	stsc := find(stbl, "stsc")
	stsz := find(stbl, "stsz")
	stco := find(stbl, "stco")
	co64 := find(stbl, "co64")
	if stts == nil || stsc == nil || stsz == nil || stco == nil && co64 == nil {
		return errors.New("an incomplete sample table")
	}
	u32 := func(b []byte, i int) int64 { return int64(binary.BigEndian.Uint32(b[i:])) }
	// Sizes.
	if len(stsz) < 12 {
		return errors.New("a short stsz")
	}
	fixed := int(u32(stsz, 4))
	count := int(u32(stsz, 8))
	if count > 50_000_000 || fixed == 0 && len(stsz) < 12+4*count {
		return errors.New("a bad stsz")
	}
	size := func(i int) int {
		if fixed != 0 {
			return fixed
		}
		return int(u32(stsz, 12+4*i))
	}
	// Chunk offsets.
	var chunks []int64
	if co64 != nil {
		n := int(u32(co64, 4))
		if len(co64) < 8+8*n {
			return errors.New("a short co64")
		}
		for i := range n {
			chunks = append(chunks, int64(binary.BigEndian.Uint64(co64[8+8*i:]))) //nolint:gosec // a file offset
		}
	} else {
		n := int(u32(stco, 4))
		if len(stco) < 8+4*n {
			return errors.New("a short stco")
		}
		for i := range n {
			chunks = append(chunks, u32(stco, 8+4*i))
		}
	}
	// Samples per chunk.
	n := int(u32(stsc, 4))
	if len(stsc) < 8+12*n {
		return errors.New("a short stsc")
	}
	var runs []chunkRun
	for i := range n {
		runs = append(runs, chunkRun{int(u32(stsc, 8+12*i)), int(u32(stsc, 12+12*i))})
	}
	if t.pcmFrame > 0 {
		return t.pcmChunks(chunks, runs, stts)
	}
	t.samples = make([]inSample, 0, count)
	s := 0
	for c := range chunks {
		per := 0
		for _, r := range runs {
			if r.first-1 <= c {
				per = r.per
			}
		}
		off := chunks[c]
		for range per {
			if s >= count {
				break
			}
			t.samples = append(t.samples, inSample{off: off, size: size(s), key: true})
			off += int64(size(s))
			s++
		}
	}
	// Decoding times.
	n = int(u32(stts, 4))
	if len(stts) < 8+8*n {
		return errors.New("a short stts")
	}
	var dts int64
	i := 0
	for k := range n {
		cnt, delta := int(u32(stts, 8+8*k)), u32(stts, 12+8*k)
		for range cnt {
			if i < len(t.samples) {
				t.samples[i].dts = dts
			}
			dts += delta
			i++
		}
	}
	// Composition offsets.
	if ctts := find(stbl, "ctts"); len(ctts) >= 8 {
		n := int(u32(ctts, 4))
		i := 0
		for k := range n {
			if len(ctts) < 16+8*k {
				break
			}
			cnt := int(u32(ctts, 8+8*k))
			off := int64(int32(binary.BigEndian.Uint32(ctts[12+8*k:]))) //nolint:gosec // signed in version 1, small in 0
			for range cnt {
				if i < len(t.samples) {
					t.samples[i].cts = off
				}
				i++
			}
		}
	}
	// Sync samples: all when there is no table.
	if stss := find(stbl, "stss"); len(stss) >= 8 {
		for i := range t.samples {
			t.samples[i].key = false
		}
		n := int(u32(stss, 4))
		for k := range n {
			if len(stss) < 12+4*k {
				break
			}
			if i := int(u32(stss, 8+4*k)) - 1; i >= 0 && i < len(t.samples) {
				t.samples[i].key = true
			}
		}
	}
	return nil
}

// pcmChunks makes a PCM track's samples: a chunk each (its sample frames
// one after another), timed by the first frame's decoding time.
func (t *inTrack) pcmChunks(chunks []int64, runs []chunkRun, stts []byte) error {
	var delta int64 = 1
	if len(stts) >= 16 {
		delta = int64(binary.BigEndian.Uint32(stts[12:]))
	}
	var dts int64
	for c := range chunks {
		per := 0
		for _, r := range runs {
			if r.first-1 <= c {
				per = r.per
			}
		}
		t.samples = append(t.samples, inSample{off: chunks[c], size: per * t.pcmFrame, dts: dts, key: true})
		dts += int64(per) * delta
	}
	return nil
}

// Track is the track numbered n, nil if there is none.
func (r *Reader) Track(n uint64) *mkv.ReadTrack {
	for i := range r.Tracks {
		if r.Tracks[i].Number == n {
			return &r.Tracks[i]
		}
	}
	return nil
}

// Next returns the next sample in file order, its time the presentation
// time (the edit list applied).
func (r *Reader) Next() (mkv.Packet, error) {
	if r.next >= len(r.order) || r.f == nil {
		return mkv.Packet{}, io.EOF
	}
	ref := r.order[r.next]
	r.next++
	t := r.tracks[ref.t]
	s := t.samples[ref.i]
	if _, err := r.f.Seek(s.off, io.SeekStart); err != nil {
		return mkv.Packet{}, err
	}
	data := make([]byte, s.size)
	if _, err := io.ReadFull(r.f, data); err != nil {
		return mkv.Packet{}, err
	}
	if t.pcmBig {
		bps := t.rt.BitDepth / 8
		for i := 0; i+bps <= len(data); i += bps {
			for a, b := i, i+bps-1; a < b; a, b = a+1, b-1 {
				data[a], data[b] = data[b], data[a]
			}
		}
	}
	at := scaled(s.dts+s.cts-t.shift, t.timescale) + t.delay
	return mkv.Packet{Track: t.rt.Number, Time: at, Keyframe: s.key, Data: data}, nil
}
