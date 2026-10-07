package mkv

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// Reading Matroska: a streaming reader for the sources mvctools takes in —
// a Blu-ray 3D remuxed to MKV, whose video track carries each MVC access
// unit whole, base and dependent view together. It reads the headers up to
// the first cluster, then returns the blocks in file order, unlaced and
// with any content compression undone. It never seeks, so it reads from a
// pipe or a network share as fast as they deliver.

// Element IDs the reader needs beyond the writer's.
const (
	idBlockGroup        = 0xA0
	idBlock             = 0xA1
	idReferenceBlock    = 0xFB
	idFlagForced        = 0x55AA
	idLanguageBCP47     = 0x22B59D
	idContentEncodings  = 0x6D80
	idContentEncoding   = 0x6240
	idContentEncScope   = 0x5032
	idContentCompr      = 0x5034
	idContentComprAlgo  = 0x4254
	idContentComprSet   = 0x4255
	idEditionFlagDflt   = 0x45DB
	idEditionFlagHidden = 0x45BD
	idChapterFlagHidden = 0x98
	idChapterFlagEnable = 0x4598
)

// ReadTrack is a track of a Matroska file being read.
type ReadTrack struct {
	Number       uint64
	Type         TrackType
	CodecID      string
	CodecPrivate []byte
	// Language is the ISO 639-2 code; Matroska's default, when the file
	// says nothing, is English.
	Language string
	// LanguageBCP47 is the IETF tag, when the file has one.
	LanguageBCP47 string
	Name          string
	Default       bool
	Forced        bool
	// Video.
	Width, Height int
	StereoMode    int
	// DisplayWidth and DisplayHeight are zero when the file does not say.
	DisplayWidth, DisplayHeight int
	// BitsPerChannel is the Colour element's bit depth, zero when the file
	// does not say.
	BitsPerChannel int
	// Audio.
	SampleRate float64
	Channels   int
	BitDepth   int

	// Content compression of the frames: -1 none, 0 zlib, 3 header
	// stripping (comprSettings goes back in front of each frame).
	comprAlgo     int
	comprSettings []byte
}

// Packet is one frame of a track.
type Packet struct {
	Track    uint64
	Time     time.Duration // the block's timestamp: the presentation time
	Keyframe bool
	Data     []byte
}

// Reader reads a Matroska file's blocks in order.
type Reader struct {
	r        *bufio.Reader
	pos      int64
	Tracks   []ReadTrack
	Chapters []time.Duration
	Duration time.Duration
	scale    int64 // ns per timestamp tick

	segEnd     int64 // -1: unknown size
	clusterEnd int64 // -1: unknown size, 0: not in a cluster
	clusterTS  int64
	pending    []Packet
	byNumber   map[uint64]*ReadTrack
	// next holds an element header read ahead while looking for the end of
	// an unknown-size cluster.
	next *elemHeader
	// ended says the input stopped inside the header, after the tracks: a
	// prefix of the file. There are no frames to read.
	ended bool
}

type elemHeader struct {
	id   uint32
	size int64 // -1: unknown
	at   int64 // position of the data
}

// errNotMatroska says the input does not start with an EBML header.
var errNotMatroska = errors.New("mkv: not a Matroska file")

// NewReader reads the file's headers: the EBML header, then the segment's
// info, tracks and chapters, up to the first cluster.
func NewReader(r io.Reader) (*Reader, error) {
	m := &Reader{r: bufio.NewReaderSize(r, 1<<20), scale: 1000000, byNumber: map[uint64]*ReadTrack{}}
	h, err := m.header()
	if err != nil {
		return nil, err
	}
	if h.id != idEBML {
		return nil, errNotMatroska
	}
	doc, err := m.data(h)
	if err != nil {
		return nil, err
	}
	var docType string
	_ = eachChild(doc, func(id uint32, b []byte) error {
		if id == idDocType {
			docType = string(b)
		}
		return nil
	})
	if docType != "matroska" && docType != "webm" {
		return nil, fmt.Errorf("mkv: document type %q", docType)
	}
	for {
		h, err := m.header()
		if err != nil {
			return nil, fmt.Errorf("mkv: no segment: %w", err)
		}
		if h.id == idSegment {
			m.segEnd = -1
			if h.size >= 0 {
				m.segEnd = h.at + h.size
			}
			break
		}
		if err := m.skip(h); err != nil {
			return nil, err
		}
	}
	// The level-1 elements before the first cluster. Input that ends once
	// the tracks are known — a prefix of the file, cut inside a cover
	// image attachment, say — ends the header: what it states is known,
	// and there are no frames to read.
	for {
		h, err := m.header()
		if err == io.EOF {
			break
		}
		if err != nil {
			if len(m.Tracks) > 0 && errors.Is(err, io.ErrUnexpectedEOF) {
				m.ended = true
				break
			}
			return nil, err
		}
		if h.id == idCluster {
			m.next = &h
			break
		}
		switch h.id {
		case idInfo, idTracks, idChapters:
			b, err := m.data(h)
			if err != nil {
				if len(m.Tracks) > 0 && h.id != idTracks && errors.Is(err, io.ErrUnexpectedEOF) {
					m.ended = true
					break
				}
				return nil, err
			}
			if err := m.level1(h.id, b); err != nil {
				return nil, err
			}
		default:
			if err := m.skip(h); err != nil {
				if len(m.Tracks) > 0 && errors.Is(err, io.ErrUnexpectedEOF) {
					m.ended = true
					break
				}
				return nil, err
			}
		}
		if m.ended {
			break
		}
	}
	if len(m.Tracks) == 0 {
		return nil, errors.New("mkv: the file has no tracks")
	}
	return m, nil
}

func (m *Reader) level1(id uint32, b []byte) error {
	switch id {
	case idInfo:
		var dur float64
		err := eachChild(b, func(id uint32, v []byte) error {
			switch id {
			case idTimestampSc:
				m.scale = int64(readUint(v)) //nolint:gosec // a tick length
			case idDuration:
				dur = readFloat(v)
			}
			return nil
		})
		m.Duration = time.Duration(dur * float64(m.scale))
		return err
	case idTracks:
		return eachChild(b, func(id uint32, v []byte) error {
			if id != idTrackEntry {
				return nil
			}
			t, err := parseTrack(v)
			if err != nil {
				return err
			}
			m.Tracks = append(m.Tracks, t)
			m.byNumber[t.Number] = &m.Tracks[len(m.Tracks)-1]
			return nil
		})
	case idChapters:
		m.Chapters = parseChapters(b)
	}
	return nil
}

func parseTrack(b []byte) (ReadTrack, error) {
	t := ReadTrack{Language: "eng", Default: true, comprAlgo: -1}
	err := eachChild(b, func(id uint32, v []byte) error {
		switch id {
		case idTrackNumber:
			t.Number = readUint(v)
		case idTrackType:
			t.Type = TrackType(readUint(v)) //nolint:gosec // a small enum
		case idCodecID:
			t.CodecID = string(v)
		case idCodecPrivate:
			t.CodecPrivate = append([]byte(nil), v...)
		case idLanguage:
			t.Language = string(v)
		case idLanguageBCP47:
			t.LanguageBCP47 = string(v)
		case idName:
			t.Name = string(v)
		case idFlagDefault:
			t.Default = readUint(v) != 0
		case idFlagForced:
			t.Forced = readUint(v) != 0
		case idVideo:
			return eachChild(v, func(id uint32, v []byte) error {
				switch id {
				case idPixelWidth:
					t.Width = int(readUint(v)) //nolint:gosec // a frame size
				case idPixelHeight:
					t.Height = int(readUint(v)) //nolint:gosec // a frame size
				case idStereoMode:
					t.StereoMode = int(readUint(v)) //nolint:gosec // a small enum
				case idDisplayWidth:
					t.DisplayWidth = int(readUint(v)) //nolint:gosec // a frame size
				case idDisplayHeight:
					t.DisplayHeight = int(readUint(v)) //nolint:gosec // a frame size
				case idColour:
					return eachChild(v, func(id uint32, v []byte) error {
						if id == idBitsPerChannel {
							t.BitsPerChannel = int(readUint(v)) //nolint:gosec // a bit depth
						}
						return nil
					})
				}
				return nil
			})
		case idAudio:
			return eachChild(v, func(id uint32, v []byte) error {
				switch id {
				case idSamplingFreq:
					t.SampleRate = readFloat(v)
				case idChannels:
					t.Channels = int(readUint(v)) //nolint:gosec // a channel count
				case idBitDepth:
					t.BitDepth = int(readUint(v)) //nolint:gosec // a sample size
				}
				return nil
			})
		case idContentEncodings:
			return eachChild(v, func(id uint32, v []byte) error {
				if id != idContentEncoding {
					return nil
				}
				scope := uint64(1)
				algo, settings := -1, []byte(nil)
				err := eachChild(v, func(id uint32, v []byte) error {
					switch id {
					case idContentEncScope:
						scope = readUint(v)
					case idContentCompr:
						algo = 0
						return eachChild(v, func(id uint32, v []byte) error {
							switch id {
							case idContentComprAlgo:
								algo = int(readUint(v)) //nolint:gosec // a small enum
							case idContentComprSet:
								settings = append([]byte(nil), v...)
							}
							return nil
						})
					}
					return nil
				})
				if err != nil {
					return err
				}
				if algo >= 0 && scope&1 != 0 {
					if algo != 0 && algo != 3 {
						return fmt.Errorf("mkv: track compression %d is not supported", algo)
					}
					t.comprAlgo, t.comprSettings = algo, settings
				}
				return nil
			})
		}
		return nil
	})
	if err == nil && t.Number == 0 {
		err = errors.New("mkv: a track has no number")
	}
	return t, err
}

// parseChapters takes the chapter starts of the default edition (else the
// first that is not hidden), in order, leaving out hidden and disabled ones.
func parseChapters(b []byte) []time.Duration {
	var editions [][]time.Duration
	def := -1
	_ = eachChild(b, func(id uint32, v []byte) error {
		if id != idEditionEntry {
			return nil
		}
		var starts []time.Duration
		hidden := false
		_ = eachChild(v, func(id uint32, v []byte) error {
			switch id {
			case idEditionFlagHidden:
				hidden = readUint(v) != 0
			case idEditionFlagDflt:
				if readUint(v) != 0 && def < 0 {
					def = len(editions)
				}
			case idChapterAtom:
				var start time.Duration
				show := true
				_ = eachChild(v, func(id uint32, v []byte) error {
					switch id {
					case idChapterTimeStart:
						start = time.Duration(readUint(v)) //nolint:gosec // nanoseconds
					case idChapterFlagHidden:
						show = show && readUint(v) == 0
					case idChapterFlagEnable:
						show = show && readUint(v) != 0
					}
					return nil
				})
				if show {
					starts = append(starts, start)
				}
			}
			return nil
		})
		if hidden {
			starts = nil
		}
		editions = append(editions, starts)
		return nil
	})
	if def >= 0 && len(editions[def]) > 0 {
		return editions[def]
	}
	for _, e := range editions {
		if len(e) > 0 {
			return e
		}
	}
	return nil
}

// Track returns the track with a number, or nil.
func (m *Reader) Track(n uint64) *ReadTrack { return m.byNumber[n] }

// Next returns the next frame in file order; io.EOF ends the file.
func (m *Reader) Next() (Packet, error) {
	if m.ended {
		return Packet{}, io.ErrUnexpectedEOF
	}
	for len(m.pending) == 0 {
		if err := m.advance(); err != nil {
			return Packet{}, err
		}
	}
	p := m.pending[0]
	m.pending = m.pending[1:]
	return p, nil
}

// isLevel1 reports whether an ID is a segment child, which ends an
// unknown-size cluster.
func isLevel1(id uint32) bool {
	switch id {
	case idCluster, idCues, idChapters, idTracks, idInfo, idSeekHead, 0x1941A469 /* Attachments */, 0x1254C367 /* Tags */ :
		return true
	}
	return false
}

// advance reads until it has queued frames or the file ends.
func (m *Reader) advance() error {
	var h elemHeader
	if m.next != nil {
		h, m.next = *m.next, nil
	} else {
		if m.segEnd >= 0 && m.pos >= m.segEnd {
			return io.EOF
		}
		var err error
		if h, err = m.header(); err != nil {
			return err
		}
	}
	// A known-size cluster ends at its size, an unknown-size one at the next
	// level-1 element.
	inCluster := m.clusterEnd != 0
	if m.clusterEnd > 0 && h.at > m.clusterEnd || m.clusterEnd < 0 && isLevel1(h.id) {
		inCluster = false
	}
	if !inCluster {
		m.clusterEnd = 0
	}
	switch {
	case h.id == idCluster:
		m.clusterEnd = -1
		if h.size >= 0 {
			m.clusterEnd = h.at + h.size
		}
		m.clusterTS = 0
		return nil
	case inCluster && h.id == idTimestamp:
		b, err := m.data(h)
		if err != nil {
			return err
		}
		m.clusterTS = int64(readUint(b)) //nolint:gosec // a timestamp
		return nil
	case inCluster && h.id == idSimpleBlock:
		b, err := m.data(h)
		if err != nil {
			return err
		}
		return m.block(b, blockFlags(b)&0x80 != 0)
	case inCluster && h.id == idBlockGroup:
		b, err := m.data(h)
		if err != nil {
			return err
		}
		var blk []byte
		key := true
		err = eachChild(b, func(id uint32, v []byte) error {
			switch id {
			case idBlock:
				blk = v
			case idReferenceBlock:
				key = false
			}
			return nil
		})
		if err != nil || blk == nil {
			return err
		}
		return m.block(blk, key)
	case h.id == idChapters && len(m.Chapters) == 0:
		b, err := m.data(h)
		if err != nil {
			return err
		}
		m.Chapters = parseChapters(b)
		return nil
	}
	return m.skip(h)
}

// blockFlags returns a block's flag byte (after its track number and
// timestamp).
func blockFlags(b []byte) byte {
	_, n := vint(b)
	if n <= 0 || n+2 >= len(b) {
		return 0
	}
	return b[n+2]
}

// block queues the frames of a block.
func (m *Reader) block(b []byte, key bool) error {
	num, n := vint(b)
	if n <= 0 || len(b) < n+3 {
		return errors.New("mkv: truncated block")
	}
	t := m.byNumber[num]
	rel := int64(int16(binary.BigEndian.Uint16(b[n:]))) //nolint:gosec // the field is signed
	flags := b[n+2]
	data := b[n+3:]
	if t == nil {
		return nil
	}
	at := time.Duration((m.clusterTS + rel) * m.scale)
	frames, err := unlace(data, flags>>1&3)
	if err != nil {
		return err
	}
	for _, f := range frames {
		switch t.comprAlgo {
		case 3:
			f = append(append(make([]byte, 0, len(t.comprSettings)+len(f)), t.comprSettings...), f...)
		case 0:
			zr, err := zlib.NewReader(bytes.NewReader(f))
			if err != nil {
				return fmt.Errorf("mkv: track %d: %w", num, err)
			}
			if f, err = io.ReadAll(zr); err != nil {
				return fmt.Errorf("mkv: track %d: %w", num, err)
			}
		default:
			f = append([]byte(nil), f...)
		}
		m.pending = append(m.pending, Packet{Track: num, Time: at, Keyframe: key, Data: f})
	}
	return nil
}

// unlace splits a block's data into its frames.
func unlace(b []byte, lacing byte) ([][]byte, error) {
	if lacing == 0 {
		return [][]byte{b}, nil
	}
	if len(b) < 1 {
		return nil, errors.New("mkv: truncated lace")
	}
	count := int(b[0]) + 1
	b = b[1:]
	sizes := make([]int, count)
	switch lacing {
	case 1: // Xiph
		for i := 0; i < count-1; i++ {
			for {
				if len(b) == 0 {
					return nil, errors.New("mkv: truncated lace")
				}
				v := int(b[0])
				b = b[1:]
				sizes[i] += v
				if v != 255 {
					break
				}
			}
		}
	case 3: // EBML
		first, n := vint(b)
		if n <= 0 {
			return nil, errors.New("mkv: bad EBML lace")
		}
		b = b[n:]
		sizes[0] = int(first) //nolint:gosec // a frame size
		for i := 1; i < count-1; i++ {
			raw, n := vint(b)
			if n <= 0 {
				return nil, errors.New("mkv: bad EBML lace")
			}
			b = b[n:]
			// A signed difference: the raw value less half its range.
			diff := int64(raw) - (int64(1)<<(7*n-1) - 1) //nolint:gosec // at most 56 bits
			sizes[i] = sizes[i-1] + int(diff)
		}
	case 2: // fixed
		if len(b)%count != 0 {
			return nil, errors.New("mkv: uneven fixed lace")
		}
		for i := range sizes {
			sizes[i] = len(b) / count
		}
		count = -count // all sizes known
	}
	if count > 0 {
		used := 0
		for _, s := range sizes[:count-1] {
			used += s
		}
		sizes[count-1] = len(b) - used
	} else {
		count = -count
	}
	frames := make([][]byte, 0, count)
	for _, s := range sizes {
		if s < 0 || s > len(b) {
			return nil, errors.New("mkv: lace sizes run past the block")
		}
		frames = append(frames, b[:s])
		b = b[s:]
	}
	return frames, nil
}

// --- EBML ---------------------------------------------------------------------

// header reads an element's ID and size.
func (m *Reader) header() (elemHeader, error) {
	first, err := m.r.ReadByte()
	if err != nil {
		return elemHeader{}, err
	}
	m.pos++
	n := idLen(first)
	if n == 0 {
		return elemHeader{}, fmt.Errorf("mkv: bad element ID at %d", m.pos-1)
	}
	id := uint32(first)
	for i := 1; i < n; i++ {
		c, err := m.r.ReadByte()
		if err != nil {
			return elemHeader{}, io.ErrUnexpectedEOF
		}
		m.pos++
		id = id<<8 | uint32(c)
	}
	c, err := m.r.ReadByte()
	if err != nil {
		return elemHeader{}, io.ErrUnexpectedEOF
	}
	m.pos++
	n = idLen(c) // the same length marker as an ID's, up to 8 bytes
	if n == 0 {
		return elemHeader{}, fmt.Errorf("mkv: bad element size at %d", m.pos-1)
	}
	size := int64(c) & (1<<(8-n) - 1)
	allOnes := size == 1<<(8-n)-1
	for i := 1; i < n; i++ {
		c, err := m.r.ReadByte()
		if err != nil {
			return elemHeader{}, io.ErrUnexpectedEOF
		}
		m.pos++
		size = size<<8 | int64(c)
		allOnes = allOnes && c == 0xff
	}
	if allOnes {
		size = -1
	}
	return elemHeader{id: id, size: size, at: m.pos}, nil
}

// idLen is the length of a vint from its first byte, up to 4 (an ID) or 8.
func idLen(b byte) int {
	for i := 0; i < 8; i++ {
		if b&(0x80>>i) != 0 {
			return i + 1
		}
	}
	return 0
}

// maxElement bounds what is read into memory at once: a block or a header
// element, never a whole cluster.
const maxElement = 256 << 20

func (m *Reader) data(h elemHeader) ([]byte, error) {
	if h.size < 0 || h.size > maxElement {
		return nil, fmt.Errorf("mkv: element %x of size %d at %d", h.id, h.size, h.at)
	}
	b := make([]byte, h.size)
	n, err := io.ReadFull(m.r, b)
	m.pos += int64(n)
	if err != nil {
		return nil, io.ErrUnexpectedEOF
	}
	return b, nil
}

func (m *Reader) skip(h elemHeader) error {
	if h.size < 0 {
		return fmt.Errorf("mkv: element %x of unknown size at %d", h.id, h.at)
	}
	n, err := m.r.Discard(int(h.size))
	m.pos += int64(n)
	if err != nil {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// vint reads a size-style vint (marker stripped); n is 0 when malformed.
func vint(b []byte) (uint64, int) {
	if len(b) == 0 {
		return 0, 0
	}
	n := idLen(b[0])
	if n == 0 || n > len(b) {
		return 0, 0
	}
	v := uint64(b[0]) & (1<<(8-n) - 1)
	for _, c := range b[1:n] {
		v = v<<8 | uint64(c)
	}
	return v, n
}

// eachChild calls fn for each element in a master element's data.
func eachChild(b []byte, fn func(id uint32, v []byte) error) error {
	for len(b) > 0 {
		n := idLen(b[0])
		if n == 0 || n > 4 || n > len(b) {
			return errors.New("mkv: bad element ID")
		}
		var id uint32
		for _, c := range b[:n] {
			id = id<<8 | uint32(c)
		}
		b = b[n:]
		size, sn := vint(b)
		if sn == 0 || size > uint64(len(b)-sn) {
			return errors.New("mkv: element runs past its parent")
		}
		b = b[sn:]
		if err := fn(id, b[:size]); err != nil {
			return err
		}
		b = b[size:]
	}
	return nil
}

func readUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func readFloat(b []byte) float64 {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	}
	return 0
}
