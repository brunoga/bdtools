package mkv

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// TrackType is a Matroska track type.
type TrackType int

const (
	TypeVideo    TrackType = 1
	TypeAudio    TrackType = 2
	TypeSubtitle TrackType = 17
)

// Track describes a track's header.
type Track struct {
	Type         TrackType
	CodecID      string // "V_MPEG4/ISO/AVC", "A_TRUEHD", "S_HDMV/PGS"
	CodecPrivate []byte
	Language     string // ISO 639-2; empty writes "und"
	Name         string
	Default      bool
	Forced       bool
	// DefaultDuration is the duration of a frame, for constant-rate tracks.
	DefaultDuration time.Duration
	// Video.
	Width, Height int
	// StereoMode is the Matroska stereo layout: 1 is side by side, left
	// eye first.
	StereoMode int
	// Audio.
	SampleRate int
	Channels   int
	BitDepth   int
}

// Frame is one block of a track.
type Frame struct {
	// PTS is the presentation time.
	PTS time.Duration
	// Order is when the frame goes into the file, relative to the other
	// tracks: its decode time. Equal to PTS for anything without frame
	// reordering.
	Order    time.Duration
	Keyframe bool
	Data     []byte
}

// Source produces a track's frames in decode order; io.EOF ends it.
type Source interface {
	Track() Track
	Next() (Frame, error)
}

// Chapter is a chapter start, named.
type Chapter struct {
	Start time.Duration
	Name  string
}

// Options for Mux.
type Options struct {
	Chapters   []Chapter
	WritingApp string
	// Progress, when set, is called now and then with the time muxed so far.
	Progress func(time.Duration)
}

// ticks converts a time to the file's timestamp units, rounding to nearest
// as mkvmerge does.
func ticks(t time.Duration) int64 {
	if t < 0 {
		return -int64((-t + timestampScale/2) / timestampScale)
	}
	return int64((t + timestampScale/2) / timestampScale)
}

const (
	timestampScale = time.Millisecond
	// A cluster is closed at a video keyframe once it holds this much, and
	// regardless once relative block times would approach their int16 limit.
	clusterTarget = 2 * time.Second
	clusterMax    = 20 * time.Second
	// seekHeadSpace is reserved at the start of the segment for the seek
	// head, which is written last.
	seekHeadSpace = 160
)

// Mux writes a Matroska file from the sources, interleaving their frames by
// decode time.
func Mux(w io.WriteSeeker, sources []Source, opts Options) error {
	m := &muxer{w: w, sources: sources}
	return m.run(opts)
}

type cuePoint struct {
	t     time.Duration
	track int
	pos   int64 // cluster position, relative to the segment data
}

type head struct {
	f   Frame
	ok  bool
	err error
}

type muxer struct {
	w       io.WriteSeeker
	sources []Source
	pos     int64 // file position
	segData int64 // position of the segment's data
	seeks   map[uint32]int64

	cluster      []byte
	clusterStart time.Duration
	clusterPos   int64
	inCluster    bool
	cues         []cuePoint
	videoTrack   int // 1-based, 0 when none
	end          time.Duration
	durationPos  int64
}

func (m *muxer) write(b []byte) error {
	n, err := m.w.Write(b)
	m.pos += int64(n)
	return err
}

func (m *muxer) run(opts Options) error {
	m.seeks = map[uint32]int64{}
	if opts.WritingApp == "" {
		opts.WritingApp = "mvctools"
	}
	// EBML header.
	var h []byte
	h = elemUint(h, idEBMLVersion, 1)
	h = elemUint(h, idEBMLReadVersion, 1)
	h = elemUint(h, idEBMLMaxIDLength, 4)
	h = elemUint(h, idEBMLMaxSizeLength, 8)
	h = elemString(h, idDocType, "matroska")
	h = elemUint(h, idDocTypeVersion, 4)
	h = elemUint(h, idDocTypeReadVersion, 2)
	if err := m.write(elem(nil, idEBML, h)); err != nil {
		return err
	}
	// Segment with a size patched at the end.
	segSizePos := m.pos + 4
	seg := putID(nil, idSegment)
	seg = putSizeLen(seg, 0, 8)
	if err := m.write(seg); err != nil {
		return err
	}
	m.segData = m.pos
	// Room for the seek head.
	if err := m.write(void(seekHeadSpace)); err != nil {
		return err
	}
	// Info, with the duration patched at the end.
	m.seeks[idInfo] = m.pos - m.segData
	var uid [16]byte
	_, _ = rand.Read(uid[:])
	var info []byte
	info = elemUint(info, idTimestampSc, uint64(timestampScale))
	info = elem(info, idSegmentUID, uid[:])
	info = elemString(info, idMuxingApp, opts.WritingApp)
	info = elemString(info, idWritingApp, opts.WritingApp)
	durOff := len(info)
	info = elemFloat(info, idDuration, 0)
	infoEl := elem(nil, idInfo, info)
	m.durationPos = m.pos + int64(len(infoEl)-len(info)) + int64(durOff) + 3 // past the Duration ID and size
	if err := m.write(infoEl); err != nil {
		return err
	}
	// Tracks.
	m.seeks[idTracks] = m.pos - m.segData
	var tracks []byte
	for i, s := range m.sources {
		t := s.Track()
		if t.Type == TypeVideo && m.videoTrack == 0 {
			m.videoTrack = i + 1
		}
		tracks = elem(tracks, idTrackEntry, trackEntry(i+1, t))
	}
	if err := m.write(elem(nil, idTracks, tracks)); err != nil {
		return err
	}
	if len(opts.Chapters) > 0 {
		m.seeks[idChapters] = m.pos - m.segData
		if err := m.write(chapters(opts.Chapters)); err != nil {
			return err
		}
	}
	// The frames, merged by decode order.
	heads := make([]head, len(m.sources))
	fill := func(i int) {
		f, err := m.sources[i].Next()
		heads[i] = head{f: f, ok: err == nil, err: err}
	}
	for i := range heads {
		fill(i)
	}
	lastProgress := time.Duration(0)
	for {
		best := -1
		for i, hd := range heads {
			if hd.err != nil && !errors.Is(hd.err, io.EOF) {
				return fmt.Errorf("track %d: %w", i+1, hd.err)
			}
			if hd.ok && (best < 0 || hd.f.Order < heads[best].f.Order) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		if err := m.block(best+1, heads[best].f); err != nil {
			return err
		}
		if opts.Progress != nil && heads[best].f.Order-lastProgress >= time.Minute {
			lastProgress = heads[best].f.Order
			opts.Progress(lastProgress)
		}
		fill(best)
	}
	if err := m.closeCluster(); err != nil {
		return err
	}
	// Cues.
	if len(m.cues) > 0 {
		m.seeks[idCues] = m.pos - m.segData
		var cues []byte
		for _, c := range m.cues {
			var tp []byte
			tp = elemUint(tp, idCueTrack, uint64(c.track))
			tp = elemUint(tp, idCueClusterPosition, uint64(c.pos))
			var cp []byte
			cp = elemUint(cp, idCueTime, uint64(max(ticks(c.t), 0)))
			cp = elem(cp, idCueTrackPositions, tp)
			cues = elem(cues, idCuePoint, cp)
		}
		if err := m.write(elem(nil, idCues, cues)); err != nil {
			return err
		}
	}
	end := m.pos
	// Seek head, into the reserved space.
	var sh []byte
	for _, id := range []uint32{idInfo, idTracks, idChapters, idCues} {
		p, ok := m.seeks[id]
		if !ok {
			continue
		}
		var s []byte
		s = elem(s, idSeekID, putID(nil, id))
		s = elemUint(s, idSeekPos, uint64(p))
		sh = elem(sh, idSeek, s)
	}
	shEl := elem(nil, idSeekHead, sh)
	if len(shEl)+2 > seekHeadSpace {
		return errors.New("mkv: seek head does not fit")
	}
	shEl = append(shEl, void(seekHeadSpace-len(shEl))...)
	if err := m.patch(m.segData, shEl); err != nil {
		return err
	}
	var d [8]byte
	binary.BigEndian.PutUint64(d[:], math.Float64bits(float64(ticks(m.end))))
	if err := m.patch(m.durationPos, d[:]); err != nil {
		return err
	}
	if err := m.patch(segSizePos, putSizeLen(nil, uint64(end-m.segData), 8)); err != nil {
		return err
	}
	_, err := m.w.Seek(end, io.SeekStart)
	return err
}

func (m *muxer) patch(at int64, b []byte) error {
	if _, err := m.w.Seek(at, io.SeekStart); err != nil {
		return err
	}
	_, err := m.w.Write(b)
	return err
}

// block adds a frame to the current cluster, starting a new one when due.
func (m *muxer) block(track int, f Frame) error {
	videoKey := track == m.videoTrack && f.Keyframe
	if m.inCluster {
		age := f.PTS - m.clusterStart
		if age >= clusterMax || age <= -30*time.Second || (videoKey && age >= clusterTarget) ||
			(m.videoTrack == 0 && age >= clusterTarget) {
			if err := m.closeCluster(); err != nil {
				return err
			}
		}
	}
	if !m.inCluster {
		m.inCluster = true
		m.clusterStart = max(time.Duration(ticks(f.PTS))*timestampScale, 0)
		m.clusterPos = m.pos - m.segData
		m.cluster = elemUint(m.cluster[:0], idTimestamp, uint64(ticks(m.clusterStart)))
	}
	if videoKey || (m.videoTrack == 0 && len(m.cues) == 0) {
		m.cues = append(m.cues, cuePoint{t: f.PTS, track: track, pos: m.clusterPos})
	}
	rel := ticks(f.PTS) - ticks(m.clusterStart)
	var hdr [4]byte
	hdr[0] = 0x80 | byte(track) // track number as a 1-byte vint (tracks < 127)
	binary.BigEndian.PutUint16(hdr[1:], uint16(int16(rel)))
	flags := byte(0)
	if f.Keyframe {
		flags |= 0x80
	}
	hdr[3] = flags
	m.cluster = putID(m.cluster, idSimpleBlock)
	m.cluster = putSize(m.cluster, uint64(len(f.Data)+4))
	m.cluster = append(m.cluster, hdr[:]...)
	m.cluster = append(m.cluster, f.Data...)
	if f.PTS > m.end {
		m.end = f.PTS
	}
	return nil
}

func (m *muxer) closeCluster() error {
	if !m.inCluster {
		return nil
	}
	m.inCluster = false
	return m.write(elem(nil, idCluster, m.cluster))
}

func trackEntry(n int, t Track) []byte {
	var b []byte
	b = elemUint(b, idTrackNumber, uint64(n))
	var uid [8]byte
	_, _ = rand.Read(uid[:])
	b = elemUint(b, idTrackUID, binary.BigEndian.Uint64(uid[:])>>1|1)
	b = elemUint(b, idTrackType, uint64(t.Type))
	if !t.Default {
		b = elemUint(b, idFlagDefault, 0)
	}
	if t.Forced {
		b = elemUint(b, idFlagForced, 1)
	}
	b = elemUint(b, idFlagLacing, 0)
	if t.DefaultDuration > 0 {
		b = elemUint(b, idDefaultDuration, uint64(t.DefaultDuration))
	}
	if t.Name != "" {
		b = elemString(b, idName, t.Name)
	}
	lang := t.Language
	if lang == "" {
		lang = "und" // Matroska's default is English, which would be a claim
	}
	b = elemString(b, idLanguage, lang)
	b = elemString(b, idCodecID, t.CodecID)
	if len(t.CodecPrivate) > 0 {
		b = elem(b, idCodecPrivate, t.CodecPrivate)
	}
	switch t.Type {
	case TypeVideo:
		var v []byte
		v = elemUint(v, idPixelWidth, uint64(t.Width))
		v = elemUint(v, idPixelHeight, uint64(t.Height))
		if t.StereoMode != 0 {
			v = elemUint(v, idStereoMode, uint64(t.StereoMode))
		}
		b = elem(b, idVideo, v)
	case TypeAudio:
		var a []byte
		a = elemFloat(a, idSamplingFreq, float64(t.SampleRate))
		a = elemUint(a, idChannels, uint64(max(t.Channels, 1)))
		if t.BitDepth > 0 {
			a = elemUint(a, idBitDepth, uint64(t.BitDepth))
		}
		b = elem(b, idAudio, a)
	}
	return b
}

func chapters(chs []Chapter) []byte {
	var ed []byte
	ed = elemUint(ed, idEditionUID, 1)
	for i, c := range chs {
		var disp []byte
		disp = elemString(disp, idChapString, c.Name)
		disp = elemString(disp, idChapLanguage, "eng")
		var atom []byte
		atom = elemUint(atom, idChapterUID, uint64(i+1))
		atom = elemUint(atom, idChapterTimeStart, uint64(max(c.Start, 0)))
		atom = elem(atom, idChapterDisplay, disp)
		ed = elem(ed, idChapterAtom, atom)
	}
	return elem(nil, idChapters, elem(nil, idEditionEntry, ed))
}

// void returns a Void element of exactly n bytes (n >= 2).
func void(n int) []byte {
	if n < 9 {
		b := putID(nil, idVoid)
		b = putSizeLen(b, uint64(n-2), 1)
		return append(b, make([]byte, n-2)...)
	}
	b := putID(nil, idVoid)
	b = putSizeLen(b, uint64(n-9), 8)
	return append(b, make([]byte, n-9)...)
}
