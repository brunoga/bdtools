// Package m2ts demultiplexes the video elementary streams of an MPEG-2
// transport stream (including Blu-ray .m2ts with 192-byte packets) and
// pairs the base view (AVC) and dependent view (MVC) access units.
package m2ts

import (
	"bufio"
	"errors"
	"io"
)

// Blu-ray PIDs of the primary video and its MVC dependent view.
const (
	PIDBaseView = 0x1011
	PIDDepView  = 0x1012
)

// AccessUnit is one paired access unit in Annex B format.
type AccessUnit struct {
	Base []byte // base view NAL units (AVC)
	Dep  []byte // dependent view NAL units (MVC), may be empty
	PTS  int64  // presentation time stamp (90 kHz), -1 if absent
	DTS  int64
}

type pes struct {
	data []byte
	pts  int64
	dts  int64
	open bool
}

// Demuxer extracts paired access units from a transport stream.
type Demuxer struct {
	r         *bufio.Reader
	pktSize   int
	pkt       [192]byte
	basePID   uint16
	depPID    uint16
	cur       [2]pes
	queue     [2][]pes
	eof       bool
	depActive bool
	// free buffers for reuse
	free [][]byte
}

// NewDemuxer returns a demuxer for the Blu-ray video PIDs.
func NewDemuxer(r io.Reader) *Demuxer {
	return NewDemuxerPIDs(r, PIDBaseView, PIDDepView)
}

// NewDemuxerPIDs returns a demuxer for the given base and dependent PIDs.
func NewDemuxerPIDs(r io.Reader, base, dep uint16) *Demuxer {
	return &Demuxer{r: bufio.NewReaderSize(r, 1<<20), basePID: base, depPID: dep}
}

var errSync = errors.New("m2ts: lost sync")

func (d *Demuxer) detect() error {
	b, err := d.r.Peek(192 * 3)
	if err != nil && len(b) < 192*2 {
		return err
	}
	switch {
	case b[4] == 0x47 && b[196] == 0x47:
		d.pktSize = 192
	case b[0] == 0x47 && b[188] == 0x47:
		d.pktSize = 188
	default:
		return errSync
	}
	return nil
}

// readPacket reads one TS packet and returns its 188-byte body.
func (d *Demuxer) readPacket() ([]byte, error) {
	if d.pktSize == 0 {
		if err := d.detect(); err != nil {
			return nil, err
		}
	}
	buf := d.pkt[:d.pktSize]
	if _, err := io.ReadFull(d.r, buf); err != nil {
		return nil, err
	}
	p := buf[d.pktSize-188:]
	if p[0] != 0x47 {
		// resync: search for the sync byte
		for {
			c, err := d.r.ReadByte()
			if err != nil {
				return nil, err
			}
			if c == 0x47 {
				n := d.pktSize - 188
				copy(d.pkt[:], make([]byte, n))
				d.pkt[n] = 0x47
				if _, err := io.ReadFull(d.r, d.pkt[n+1:d.pktSize]); err != nil {
					return nil, err
				}
				return d.pkt[n:d.pktSize], nil
			}
		}
	}
	return p, nil
}

func (d *Demuxer) getBuf() []byte {
	if n := len(d.free); n > 0 {
		b := d.free[n-1]
		d.free = d.free[:n-1]
		return b[:0]
	}
	return make([]byte, 0, 1<<18)
}

// Recycle returns the buffers of an access unit for reuse.
func (d *Demuxer) Recycle(au *AccessUnit) {
	if au.Base != nil {
		d.free = append(d.free, au.Base)
	}
	if au.Dep != nil {
		d.free = append(d.free, au.Dep)
	}
	au.Base, au.Dep = nil, nil
}

func parseTS(b []byte) int64 {
	return int64(b[0]>>1&7)<<30 | int64(b[1])<<22 | int64(b[2]>>1)<<15 | int64(b[3])<<7 | int64(b[4]>>1)
}

// finish completes the PES of stream i and queues its ES payload.
func (d *Demuxer) finish(i int) {
	c := &d.cur[i]
	if !c.open {
		return
	}
	c.open = false
	b := c.data
	if len(b) < 9 || b[0] != 0 || b[1] != 0 || b[2] != 1 {
		d.free = append(d.free, b)
		return
	}
	hl := int(b[8])
	flags := b[7]
	pts, dts := int64(-1), int64(-1)
	if flags&0x80 != 0 && len(b) >= 14 {
		pts = parseTS(b[9:])
		dts = pts
	}
	if flags&0x40 != 0 && len(b) >= 19 {
		dts = parseTS(b[14:])
	}
	if 9+hl > len(b) {
		d.free = append(d.free, b)
		return
	}
	es := b[9+hl:]
	copy(b, es)
	d.queue[i] = append(d.queue[i], pes{data: b[:len(es)], pts: pts, dts: dts})
}

// step reads and processes one TS packet.
func (d *Demuxer) step() error {
	if d.eof {
		return io.EOF
	}
	p, err := d.readPacket()
	if err != nil {
		d.eof = true
		d.finish(0)
		d.finish(1)
		return err
	}
	pid := uint16(p[1]&0x1f)<<8 | uint16(p[2])
	var i int
	switch pid {
	case d.basePID:
		i = 0
	case d.depPID:
		i = 1
		d.depActive = true
	default:
		return nil
	}
	pusi := p[1]&0x40 != 0
	afc := p[3] >> 4 & 3
	payload := p[4:]
	if afc&2 != 0 {
		al := int(p[4])
		if 5+al > len(p) {
			return nil
		}
		payload = p[5+al:]
	}
	if afc&1 == 0 {
		return nil
	}
	if pusi {
		d.finish(i)
		d.cur[i] = pes{data: d.getBuf(), open: true}
	}
	if d.cur[i].open {
		d.cur[i].data = append(d.cur[i].data, payload...)
	}
	return nil
}

// Next returns the next access unit. The returned buffers may be passed to
// Recycle once consumed.
func (d *Demuxer) Next() (AccessUnit, error) {
	for len(d.queue[0]) == 0 {
		if err := d.step(); err != nil && len(d.queue[0]) == 0 {
			return AccessUnit{}, err
		}
	}
	b := d.queue[0][0]
	// find the dependent view PES with the same DTS, dropping older ones
	for {
		for len(d.queue[1]) > 0 {
			q := d.queue[1][0]
			if b.dts < 0 || q.dts < 0 || q.dts >= b.dts {
				break
			}
			d.queue[1] = d.queue[1][1:]
			d.free = append(d.free, q.data)
		}
		if len(d.queue[1]) > 0 || d.eof || (!d.depActive && len(d.queue[0]) >= 2) {
			break
		}
		// the dependent PES is complete once the next one starts; stop
		// waiting if the base view runs far ahead (missing dependent view)
		if len(d.queue[0]) > 8 {
			break
		}
		if d.step() != nil {
			break // the input ended or lost sync: deliver what is queued
		}
	}
	d.queue[0] = d.queue[0][1:]
	au := AccessUnit{Base: b.data, PTS: b.pts, DTS: b.dts}
	if len(d.queue[1]) > 0 {
		q := d.queue[1][0]
		if b.dts < 0 || q.dts < 0 || q.dts == b.dts {
			au.Dep = q.data
			d.queue[1] = d.queue[1][1:]
		}
	}
	return au, nil
}
