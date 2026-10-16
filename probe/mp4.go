package probe

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// MP4 probes an ISO base media file -- MP4, or QuickTime .mov, which shares
// the box structure -- reading only its box headers and its movie box. size
// is the file's full length. The caller owns r and MP4 does not close it.
//
// The movie box ("moov") holds every track's description and nothing else
// big, and a writer puts it either before the media data or after it, so it
// lies in the file's first or last piece. Finding it costs one header per
// top-level box; the media data box is skipped by its size, never read.
//
// As with Image, a read the caller's reader could not satisfy comes back as
// a *MissingDataError naming the bytes, so the caller can fetch them and try
// again.
func MP4(r io.ReaderAt, size int64, label string) (res *Result, err error) {
	rr := &watchReader{r: r}
	defer func() {
		if p := recover(); p != nil {
			res, err = nil, fmt.Errorf("probe: %s: malformed MP4 (%v)", label, p)
		}
		if err != nil && rr.failed != nil {
			res, err = nil, &MissingDataError{Offset: rr.off, Length: rr.n, Err: rr.failed, Probe: err}
		}
	}()
	return probeMP4(rr, size, label)
}

// maxMoov bounds the movie box read into memory. A two-hour film's sample
// tables run to a few MiB; a header claiming more than this is not one.
const maxMoov = 64 << 20

func probeMP4(r io.ReaderAt, size int64, label string) (*Result, error) {
	var moov []byte
	for off := int64(0); off < size; {
		typ, hdr, n, err := boxHeader(r, off, size)
		if err != nil {
			return nil, fmt.Errorf("probe: %s: %w", label, err)
		}
		if off == 0 && !leadingBox(typ) {
			return nil, fmt.Errorf("probe: %s: not an ISO base media file (first box %q)", label, typ)
		}
		if typ == "moov" {
			if n-hdr > maxMoov {
				return nil, fmt.Errorf("probe: %s: movie box of %d bytes", label, n-hdr)
			}
			moov = make([]byte, n-hdr)
			if _, err := r.ReadAt(moov, off+hdr); err != nil {
				return nil, fmt.Errorf("probe: %s: reading the movie box: %w", label, err)
			}
			break
		}
		off += n
	}
	if moov == nil {
		return nil, fmt.Errorf("probe: %s: no movie box", label)
	}
	return parseMoov(moov, label), nil
}

// leadingBox is what an ISO base media file may begin with: the file type
// box, or -- in a QuickTime file old enough to predate it -- a movie, media
// data, padding or "wide" box.
func leadingBox(typ string) bool {
	switch typ {
	case "ftyp", "moov", "mdat", "free", "skip", "wide", "pnot":
		return true
	}
	return false
}

// boxHeader reads the box header at off: its type, the header's length and
// the box's whole length. A box of size 0 runs to the end of the file.
func boxHeader(r io.ReaderAt, off, size int64) (typ string, hdr, n int64, err error) {
	var b [16]byte
	if _, err := r.ReadAt(b[:8], off); err != nil {
		return "", 0, 0, fmt.Errorf("box header at %d: %w", off, err)
	}
	n, hdr, typ = int64(binary.BigEndian.Uint32(b[:4])), 8, string(b[4:8])
	switch n {
	case 0:
		n = size - off
	case 1:
		if _, err := r.ReadAt(b[8:16], off+8); err != nil {
			return "", 0, 0, fmt.Errorf("box header at %d: %w", off, err)
		}
		u := binary.BigEndian.Uint64(b[8:16])
		if u > uint64(size) {
			return "", 0, 0, fmt.Errorf("%q box at %d claims %d bytes, past the end", typ, off, u)
		}
		n, hdr = int64(u), 16
	}
	if n < hdr || off+n > size {
		return "", 0, 0, fmt.Errorf("%q box at %d has impossible size %d", typ, off, n)
	}
	return typ, hdr, n, nil
}

// box is one box inside a buffer: its type and its body.
type box struct {
	typ  string
	body []byte
}

// children splits a buffer into the boxes it holds. A truncated or
// malformed trailing box ends the list rather than failing it: what came
// before it was read in full.
func children(b []byte) []box {
	var out []box
	for len(b) >= 8 {
		n, hdr := uint64(binary.BigEndian.Uint32(b[:4])), uint64(8)
		typ := string(b[4:8])
		switch n {
		case 0:
			n = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return out
			}
			n, hdr = binary.BigEndian.Uint64(b[8:16]), 16
		}
		if n < hdr || n > uint64(len(b)) {
			return out
		}
		out = append(out, box{typ, b[hdr:n]})
		b = b[n:]
	}
	return out
}

func child(b []byte, typ string) []byte {
	for _, c := range children(b) {
		if c.typ == typ {
			return c.body
		}
	}
	return nil
}

func parseMoov(moov []byte, label string) *Result {
	res := &Result{Kind: KindMP4, Source: label}
	if mvhd := child(moov, "mvhd"); mvhd != nil {
		res.Duration = headerDuration(mvhd, 12)
	}
	for _, c := range children(moov) {
		if c.typ != "trak" {
			continue
		}
		mdia := child(c.body, "mdia")
		if mdia == nil {
			continue
		}
		lang := mdhdLanguage(child(mdia, "mdhd"))
		handler := hdlrType(child(mdia, "hdlr"))
		stsd := child(child(child(mdia, "minf"), "stbl"), "stsd")
		if len(stsd) < 8 {
			continue
		}
		// One track has one sample description in practice; a second would
		// describe the same stream with different parameters.
		entries := children(stsd[8:])
		if len(entries) == 0 {
			continue
		}
		e := entries[0]
		switch handler {
		case "vide":
			v, layout := videoEntry(e)
			res.Video = append(res.Video, v)
			if res.Layout == Layout2D {
				res.Layout = layout
			}
		case "soun":
			res.Audio = append(res.Audio, audioEntry(e, lang))
		case "sbtl", "text", "subt", "clcp":
			res.Subtitle = append(res.Subtitle, SubtitleTrack{Codec: mp4SubtitleCodec(e.typ), Language: lang})
		}
	}
	res.Is3D = res.Layout != Layout2D
	return res
}

// headerDuration reads a movie or media header's duration. Version 0 puts
// creation and modification times in 32 bits, version 1 in 64; the
// timescale follows them and the duration follows that. v0 is the offset of
// the timescale in a version 0 header, past the full box's 4 bytes.
func headerDuration(b []byte, v0 int) time.Duration {
	if len(b) < 4 {
		return 0
	}
	var scale, dur uint64
	switch b[0] {
	case 0:
		if len(b) < v0+8 {
			return 0
		}
		scale = uint64(binary.BigEndian.Uint32(b[v0:]))
		dur = uint64(binary.BigEndian.Uint32(b[v0+4:]))
		if dur == 0xffffffff {
			return 0 // "unknown"
		}
	case 1:
		o := v0 + 8 // two 64-bit times instead of two 32-bit ones
		if len(b) < o+12 {
			return 0
		}
		scale = uint64(binary.BigEndian.Uint32(b[o:]))
		dur = binary.BigEndian.Uint64(b[o+4:])
	default:
		return 0
	}
	if scale == 0 || dur > uint64(1<<62)/uint64(time.Second) {
		return 0
	}
	return time.Duration(dur * uint64(time.Second) / scale)
}

// mdhdLanguage reads a media header's ISO 639-2/T code, packed as three
// 5-bit letters offset from 0x60. QuickTime may instead store a Macintosh
// language code, below 0x400, which says nothing usable; "und" says nothing
// either. Both read as unstated.
func mdhdLanguage(b []byte) string {
	if len(b) < 4 {
		return ""
	}
	o := 4 + 16 // version 0: four 32-bit fields
	if b[0] == 1 {
		o = 4 + 28 // version 1: three 64-bit fields and the 32-bit timescale
	}
	if len(b) < o+2 {
		return ""
	}
	v := binary.BigEndian.Uint16(b[o:])
	if v < 0x400 {
		return ""
	}
	l := []byte{byte(v>>10&0x1f) + 0x60, byte(v>>5&0x1f) + 0x60, byte(v&0x1f) + 0x60}
	for _, c := range l {
		if c < 'a' || c > 'z' {
			return ""
		}
	}
	if s := string(l); s != "und" {
		return s
	}
	return ""
}

func hdlrType(b []byte) string {
	// full box header, then pre_defined, then the handler type
	if len(b) < 12 {
		return ""
	}
	return string(b[8:12])
}

// visualEntryLen is the fixed part of a visual sample entry: reserved and
// data reference index (8), pre-defined and reserved (16), width and height
// (4), resolutions (8), reserved (4), frame count (2), compressor name (32),
// depth (2) and pre-defined (2). The entry's own boxes follow it.
const visualEntryLen = 78

// videoEntry reads a visual sample entry: the codec, the picture size, and
// whether the stream is stereoscopic.
//
// MV-HEVC is told by its layered configuration. A single-layer HEVC track
// carries only "hvcC"; a multiview one adds "lhvC", the second layer's
// decoder configuration (ISO/IEC 14496-15 clause 9). Apple's video extended
// usage box, "vexu", says which eyes are present when there is one, and a
// layered track whose "stri" names fewer than both is not a stereo pair. A
// frame-packed track is told by the spherical video "st3d" box instead.
func videoEntry(e box) (VideoTrack, Layout) {
	v := VideoTrack{Codec: mp4VideoCodec(e.typ)}
	if len(e.body) < visualEntryLen {
		return v, Layout2D
	}
	v.Width = int(binary.BigEndian.Uint16(e.body[24:]))
	v.Height = int(binary.BigEndian.Uint16(e.body[26:]))
	ext := e.body[visualEntryLen:]
	if c := child(ext, "hvcC"); len(c) > 18 {
		// bitDepthLumaMinus8 in the low three bits of byte 17
		v.BitDepth = int(c[17]&0x07) + 8
	} else if c := child(ext, "avcC"); len(c) > 0 {
		v.BitDepth = 8
	}

	layout := Layout2D
	if child(ext, "lhvC") != nil {
		layout = LayoutMVHEVC
		if eyes := child(child(ext, "vexu"), "eyes"); eyes != nil {
			if stri := child(eyes, "stri"); len(stri) >= 5 && stri[4]&0x03 != 0x03 {
				layout = Layout2D
			}
		}
		if layout == LayoutMVHEVC {
			v.Codec = "MV-HEVC"
		}
	}
	if st3d := child(ext, "st3d"); layout == Layout2D && len(st3d) >= 5 {
		switch st3d[4] {
		case 1:
			layout = LayoutTopBottom
		case 2:
			layout = LayoutSideBySide
		}
	}
	return v, layout
}

// soundEntryLen is the fixed part of a sound sample entry (version 0):
// reserved and data reference index (8), version and reserved (8), channel
// count (2), sample size (2), pre-defined and reserved (4), and the sample
// rate as 16.16 fixed point (4).
const soundEntryLen = 28

func audioEntry(e box, lang string) AudioTrack {
	a := AudioTrack{Codec: mp4AudioCodec(e.typ), Language: lang}
	if len(e.body) >= soundEntryLen {
		a.Channels = int(binary.BigEndian.Uint16(e.body[16:]))
		a.SampleRate = float64(binary.BigEndian.Uint32(e.body[24:])) / 65536
	}
	return a
}

// The codec names follow the Matroska and disc probes' vocabulary, so a
// caller compares one set of strings whatever the container.

func mp4VideoCodec(typ string) string {
	switch typ {
	case "hvc1", "hev1", "dvh1", "dvhe":
		return "HEVC"
	case "avc1", "avc3":
		return "H.264"
	case "av01":
		return "AV1"
	case "vp09":
		return "VP9"
	case "mp4v":
		return "MPEG-4"
	}
	return typ
}

func mp4AudioCodec(typ string) string {
	switch typ {
	case "mp4a":
		return "AAC"
	case "ac-3":
		return "AC3"
	case "ec-3":
		return "E-AC3 (DD+)"
	case "Opus":
		return "Opus"
	case "fLaC":
		return "FLAC"
	case "alac":
		return "ALAC"
	case "lpcm", "sowt", "twos", "ipcm":
		return "LPCM"
	}
	return typ
}

func mp4SubtitleCodec(typ string) string {
	switch typ {
	case "tx3g":
		return "tx3g"
	case "wvtt":
		return "WebVTT"
	case "stpp":
		return "TTML"
	case "c608":
		return "CEA-608"
	}
	return typ
}
