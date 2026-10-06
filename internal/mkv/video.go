package mkv

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"sort"
	"time"
)

// Codec of an elementary video stream.
type Codec int

const (
	H264 Codec = iota
	HEVC
)

// VideoSource reads an encoder's raw Annex B output — in decode order, with
// no timestamps — and gives each frame its presentation time from its
// picture order count: within a coded video sequence, display order is POC
// order. The frames are passed through in decode order, NAL units
// length-prefixed as Matroska stores them, access unit delimiters dropped and
// the parameter sets kept in-band as well as in the codec private data.
type VideoSource struct {
	codec    Codec
	r        *bufio.Reader
	frameDur time.Duration
	delay    time.Duration
	track    Track

	buf      []byte
	eof      bool
	pending  []*vframe // decoded-order frames awaiting a display index
	ready    []*vframe
	decoded  int64
	window   int
	params   paramSets
	poc      pocState
	hpoc     hevcPOC
	cvsBase  int64 // display index at the start of the coded video sequence
	cvsCount int64
	maxPOC   int64
	err      error
}

type vframe struct {
	nals     [][]byte
	gotPOC   bool
	key      bool
	poc      int64
	decIdx   int64
	dispIdx  int64
	assigned bool
}

// reorderWindow bounds how far display order can run from decode order.
// Encoders use far less (x264's B-pyramid needs 2-3); the H.264 maximum DPB
// is 16.
const reorderWindow = 18

// NewVideoSource reads the first frames to learn the stream's parameters.
// fps is the frame rate as a fraction.
func NewVideoSource(r io.Reader, codec Codec, fpsNum, fpsDen int, stereoMode int) (*VideoSource, error) {
	if fpsNum <= 0 || fpsDen <= 0 {
		return nil, errors.New("mkv: unknown frame rate")
	}
	v := &VideoSource{codec: codec, r: bufio.NewReaderSize(r, 4<<20), window: reorderWindow,
		frameDur: time.Duration(int64(time.Second) * int64(fpsDen) / int64(fpsNum))}
	v.params.fpsNum, v.params.fpsDen = fpsNum, fpsDen
	// Prime: read until the parameter sets are known.
	for !v.params.complete(codec) {
		if err := v.readFrame(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("mkv: no parameter sets in the video stream")
			}
			return nil, err
		}
	}
	priv, err := v.params.codecPrivate(codec)
	if err != nil {
		return nil, err
	}
	v.track = Track{Type: TypeVideo, CodecPrivate: priv, Width: v.params.width, Height: v.params.height,
		StereoMode: stereoMode, DefaultDuration: v.frameDur, Default: true}
	if codec == HEVC {
		v.track.CodecID = "V_MPEGH/ISO/HEVC"
	} else {
		v.track.CodecID = "V_MPEG4/ISO/AVC"
	}
	return v, nil
}

// Track describes the video track.
func (v *VideoSource) Track() Track { return v.track }

// Next returns the next frame in decode order.
func (v *VideoSource) Next() (Frame, error) {
	for len(v.ready) == 0 {
		if v.err != nil {
			return Frame{}, v.err
		}
		if err := v.readFrame(); err != nil {
			if !errors.Is(err, io.EOF) {
				return Frame{}, err
			}
			v.flushCVS()
			v.release(true)
			if len(v.ready) == 0 {
				return Frame{}, io.EOF
			}
		}
	}
	f := v.ready[0]
	v.ready = v.ready[1:]
	nals := f.nals
	if f.key && !v.hasParams(nals) {
		// A keyframe carries the parameter sets, so playback can start there.
		nals = append(v.params.raw(v.codec), nals...)
	}
	var data []byte
	for _, n := range nals {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(n))) //nolint:gosec // a NAL unit is far smaller
		data = append(data, l[:]...)
		data = append(data, n...)
	}
	return Frame{
		PTS:      v.at(f.dispIdx),
		Order:    v.at(f.decIdx),
		Keyframe: f.key,
		Data:     data,
	}, nil
}

// hasParams reports whether an access unit carries a sequence parameter set.
func (v *VideoSource) hasParams(nals [][]byte) bool {
	for _, n := range nals {
		if len(n) == 0 {
			continue
		}
		if v.codec == H264 && n[0]&0x1f == 7 || v.codec == HEVC && n[0]>>1&0x3f == 33 {
			return true
		}
	}
	return false
}

// at is the time of frame n: n frame durations, each truncated to a whole
// nanosecond. That is how mkvmerge counts, so the two give the same
// timestamps; the drift over a feature film is under 0.05 ms.
func (v *VideoSource) at(n int64) time.Duration { return v.delay + time.Duration(n)*v.frameDur }

// SetDelay starts the picture later: for a source whose first picture comes
// after the start of what plays (the audio's), as some discs do.
func (v *VideoSource) SetDelay(d time.Duration) { v.delay = d }

// readFrame reads one access unit and queues it.
func (v *VideoSource) readFrame() error {
	nals, err := v.nextAU()
	if err != nil {
		return err
	}
	f := &vframe{decIdx: v.decoded}
	v.decoded++
	isNewCVS := false
	for _, n := range nals {
		// Too short to carry a header worth parsing: passed through.
		if len(n) < 3 {
			if len(n) > 0 {
				f.nals = append(f.nals, n)
			}
			continue
		}
		if v.codec == H264 {
			t := n[0] & 0x1f
			switch t {
			case 9: // access unit delimiter
				continue
			case 7:
				v.params.addH264SPS(n)
			case 8:
				v.params.addH264PPS(n)
			case 1, 5:
				if !f.gotPOC {
					f.gotPOC = true
					poc, idr, err := v.poc.h264(n, &v.params)
					if err != nil {
						return err
					}
					f.poc = int64(poc)
					if idr {
						f.key, isNewCVS = true, true
					}
				}
			}
		} else {
			t := n[0] >> 1 & 0x3f
			switch {
			case t == 35: // access unit delimiter
				continue
			case t == 32:
				v.params.addHEVC(0, n)
			case t == 33:
				v.params.addHEVCSPS(n)
			case t == 34:
				v.params.addHEVCPPS(n)
			case t < 32 && len(n) > 2 && !f.gotPOC:
				f.gotPOC = true
				poc, irap, reset, err := v.hpoc.poc(n, &v.params)
				if err != nil {
					return err
				}
				f.poc = poc
				f.key = irap
				if reset {
					isNewCVS = true
				}
			}
		}
		f.nals = append(f.nals, n)
	}
	if isNewCVS {
		// Everything before an IDR is shown before it.
		v.flushCVS()
	}
	v.pending = append(v.pending, f)
	if f.poc > v.maxPOC || len(v.pending) == 1 {
		v.maxPOC = f.poc
	}
	v.release(false)
	return nil
}

// flushCVS gives display indexes to every pending frame: the coded video
// sequence they belong to has ended.
func (v *VideoSource) flushCVS() {
	v.assign(len(v.pending))
	v.cvsBase += v.cvsCount
	v.cvsCount = 0
}

// assign gives the n lowest-POC pending frames the next display indexes.
func (v *VideoSource) assign(n int) {
	var open []*vframe
	for _, f := range v.pending {
		if !f.assigned {
			open = append(open, f)
		}
	}
	sort.SliceStable(open, func(i, j int) bool { return open[i].poc < open[j].poc })
	for i := 0; i < n && i < len(open); i++ {
		open[i].dispIdx = v.cvsBase + v.cvsCount
		open[i].assigned = true
		v.cvsCount++
	}
}

// release moves frames whose display index is known to the ready queue,
// in decode order. With more than the reorder window pending, the
// lowest-POC frame can come no later, so it gets the next index.
func (v *VideoSource) release(final bool) {
	for {
		open := 0
		for _, f := range v.pending {
			if !f.assigned {
				open++
			}
		}
		if open > v.window {
			v.assign(1)
			continue
		}
		break
	}
	for len(v.pending) > 0 && (v.pending[0].assigned || final) {
		if !v.pending[0].assigned {
			v.flushCVS()
		}
		v.ready = append(v.ready, v.pending[0])
		v.pending = v.pending[1:]
	}
}

// nextAU reads NAL units up to the next access unit boundary.
func (v *VideoSource) nextAU() ([][]byte, error) {
	for {
		nals := splitNALs(v.buf)
		// The last NAL may be incomplete until more is read or the input ends.
		if cut, ok := v.boundary(nals, v.eof); ok {
			au := nals[:cut]
			// Keep the rest in the buffer.
			rest := nals[cut:]
			var nb []byte
			for _, n := range rest {
				nb = append(nb, 0, 0, 0, 1)
				nb = append(nb, n...)
			}
			out := make([][]byte, len(au))
			for i, n := range au {
				out[i] = append([]byte(nil), n...)
			}
			v.buf = nb
			return out, nil
		}
		if v.eof {
			if len(nals) == 0 {
				return nil, io.EOF
			}
			v.buf = nil
			out := make([][]byte, len(nals))
			for i, n := range nals {
				out[i] = append([]byte(nil), n...)
			}
			return out, nil
		}
		chunk := make([]byte, 1<<20)
		n, err := io.ReadFull(v.r, chunk)
		v.buf = append(v.buf, chunk[:n]...)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			v.eof = true
		} else if err != nil {
			return nil, err
		}
	}
}

// boundary finds where the second access unit starts among complete NAL
// units (all but the last, unless the input ended).
func (v *VideoSource) boundary(nals [][]byte, eof bool) (int, bool) {
	complete := len(nals) - 1
	if eof {
		complete = len(nals)
	}
	seenVCL := false
	for i := 0; i < complete; i++ {
		n := nals[i]
		if len(n) == 0 {
			continue
		}
		var vcl, first, starter bool
		if v.codec == H264 {
			t := n[0] & 0x1f
			vcl = t == 1 || t == 5
			first = vcl && len(n) > 1 && n[1]&0x80 != 0 // first_mb_in_slice == 0
			starter = t == 9 || t == 7 || t == 8 || t == 6 || (t >= 14 && t <= 18)
		} else {
			if len(n) < 3 {
				continue
			}
			t := n[0] >> 1 & 0x3f
			vcl = t < 32
			first = vcl && n[2]&0x80 != 0
			starter = t == 35 || t == 32 || t == 33 || t == 34 || t == 39 || (t >= 41 && t <= 44) || (t >= 48 && t <= 55)
		}
		if seenVCL && (first || starter) {
			return i, true
		}
		if vcl {
			seenVCL = true
		}
	}
	return 0, false
}

// String names the codec.
func (c Codec) String() string {
	if c == HEVC {
		return "HEVC"
	}
	return "H.264"
}
