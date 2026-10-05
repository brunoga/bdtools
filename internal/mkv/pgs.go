package mkv

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"time"
)

// PGSSource reads a .sup file — each presentation graphics segment behind
// "PG" and its PTS and DTS — and gives one frame per display set: the
// segments from a presentation composition to its end segment, at the
// composition's time.
type PGSSource struct {
	r     *bufio.Reader
	track Track
}

// NewPGSSource reads a .sup file.
func NewPGSSource(r io.Reader, lang string) *PGSSource {
	return &PGSSource{r: bufio.NewReaderSize(r, 1<<18),
		track: Track{Type: TypeSubtitle, CodecID: "S_HDMV/PGS", Language: lang}}
}

// Track describes the track.
func (p *PGSSource) Track() Track { return p.track }

// Next returns the next display set.
func (p *PGSSource) Next() (Frame, error) {
	var (
		data  []byte
		pts   int64
		start bool
	)
	for {
		var h [13]byte
		if _, err := io.ReadFull(p.r, h[:]); err != nil {
			if start {
				return p.frame(pts, data), nil
			}
			return Frame{}, io.EOF
		}
		if h[0] != 'P' || h[1] != 'G' {
			return Frame{}, errors.New("mkv: not a .sup file")
		}
		size := int(binary.BigEndian.Uint16(h[11:]))
		seg := make([]byte, 3+size)
		copy(seg, h[10:])
		if _, err := io.ReadFull(p.r, seg[3:]); err != nil {
			return Frame{}, io.EOF
		}
		if !start {
			pts = int64(binary.BigEndian.Uint32(h[2:]))
			start = true
		}
		data = append(data, seg...)
		if h[10] == 0x80 { // end of display set
			return p.frame(pts, data), nil
		}
	}
}

func (p *PGSSource) frame(pts int64, data []byte) Frame {
	t := time.Duration(pts) * time.Second / 90000
	return Frame{PTS: t, Order: t, Keyframe: true, Data: data}
}
