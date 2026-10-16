// Package mp4 writes ISO base media files (MP4, QuickTime): a video track
// of HEVC or MV-HEVC with Apple's stereo signalling, and audio tracks of
// AC-3 or E-AC-3, from the same sources the Matroska muxer takes.
package mp4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
)

// Spatial is what a stereo (MV-HEVC) video track says of itself to a
// player: Apple's video extended usage, which visionOS and the Quest read
// to show the two views to the two eyes. Photos marks a file "spatial"
// only with a field of view and a baseline or disparity adjustment.
type Spatial struct {
	// FOV is the horizontal field of view of each eye's picture, in
	// degrees (an iPhone's spatial video says 63.4, Vision Pro's 71.6).
	FOV float64
	// Baseline is the distance between the two cameras, in millimetres
	// (an iPhone's 19.24, Vision Pro's 63.76).
	Baseline float64
	// Disparity is a shift of the two views, -1 to 1 of the picture's
	// width, 0 for none.
	Disparity float64
	// HeroRight makes the right eye the hero (the one shown in 2D); the
	// left is the base layer and the hero otherwise.
	HeroRight bool
}

// Options for Mux.
type Options struct {
	// FPSNum and FPSDen are the video's frame rate, which sets its
	// timescale (24000/1001 for film).
	FPSNum, FPSDen int
	// Spatial, when set, marks the video track as a stereo pair.
	Spatial *Spatial
	// QuickTime writes a QuickTime (.mov) brand instead of MP4's.
	QuickTime bool
}

// Mux writes the sources (the video first, then audio tracks) to f, in
// decode order interleaved by time; the movie header goes last, so f is
// written once through, then its media data box is sized.
func Mux(f io.WriteSeeker, sources []mkv.Source, o Options) error {
	if len(sources) == 0 || sources[0].Track().Type != mkv.TypeVideo {
		return errors.New("mp4: the first source must be the video")
	}
	if o.FPSNum <= 0 || o.FPSDen <= 0 {
		return errors.New("mp4: the frame rate is needed")
	}
	m := &muxer{f: f, o: o}
	for _, s := range sources {
		m.tracks = append(m.tracks, &track{src: s, t: s.Track()})
	}
	if err := m.writeBox("ftyp", m.ftyp()); err != nil {
		return err
	}
	var err error
	if m.mdatStart, err = f.Seek(0, io.SeekCurrent); err != nil {
		return err
	}
	// a media data box with a 64-bit size, patched at the end
	if _, err := f.Write([]byte{0, 0, 0, 1, 'm', 'd', 'a', 't', 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	m.pos = m.mdatStart + 16
	if err := m.interleave(); err != nil {
		return err
	}
	end := m.pos
	if _, err := f.Seek(m.mdatStart+8, io.SeekStart); err != nil {
		return err
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(end-m.mdatStart)) //nolint:gosec // a file size
	if _, err := f.Write(size[:]); err != nil {
		return err
	}
	if _, err := f.Seek(end, io.SeekStart); err != nil {
		return err
	}
	moov, err := m.moov()
	if err != nil {
		return err
	}
	return m.writeBox("moov", moov)
}

type muxer struct {
	f         io.WriteSeeker
	o         Options
	tracks    []*track
	mdatStart int64
	pos       int64
}

// sample is one written sample's place and timing.
type sample struct {
	offset int64
	size   uint32
	dts    int64 // in the track's timescale
	cts    int64
	key    bool
}

type track struct {
	src     mkv.Source
	t       mkv.Track
	pending *mkv.Frame
	done    bool
	samples []sample
	// video: the parameter sets and SEI the sample entry carries, taken
	// from the samples (an hvc1 track holds none in band)
	layer1 [][]byte // SPS and PPS of layer 1
	sei3D  []byte   // the 3D reference displays SEI
	// audio: the first frame, for the decoder configuration box
	first []byte
	delay time.Duration // the track's first presentation time
}

func (tr *track) video() bool { return tr.t.Type == mkv.TypeVideo }

// timescale is the track's media timescale.
func (tr *track) timescale(o Options) int64 {
	if tr.video() {
		return int64(o.FPSNum)
	}
	return int64(tr.t.SampleRate)
}

// next pulls the track's next frame, keeping it until taken.
func (tr *track) next() error {
	if tr.pending != nil || tr.done {
		return nil
	}
	f, err := tr.src.Next()
	if err == io.EOF {
		tr.done = true
		return nil
	}
	if err != nil {
		return err
	}
	tr.pending = &f
	return nil
}

// interleave writes every track's samples into the media data box in
// decode order across tracks.
func (m *muxer) interleave() error {
	for {
		var pick *track
		for _, tr := range m.tracks {
			if err := tr.next(); err != nil {
				return err
			}
			if tr.pending != nil && (pick == nil || tr.pending.Order < pick.pending.Order) {
				pick = tr
			}
		}
		if pick == nil {
			return nil
		}
		if err := m.write(pick, pick.pending); err != nil {
			return err
		}
		pick.pending = nil
	}
}

// write puts one frame's sample in the media data box.
func (m *muxer) write(tr *track, fr *mkv.Frame) error {
	data := fr.Data
	ts := tr.timescale(m.o)
	var dts, cts int64
	if tr.video() {
		// decode order counts samples; presentation is the frame's time
		dts = int64(len(tr.samples)) * int64(m.o.FPSDen)
		cts = int64(math.Round(float64(fr.PTS) * float64(ts) / 1e9))
		var err error
		if data, err = tr.videoSample(data); err != nil {
			return err
		}
	} else {
		if len(tr.samples) == 0 {
			tr.first = append([]byte(nil), data...)
			tr.delay = fr.PTS
		}
		dts = int64(math.Round(float64(fr.PTS-tr.delay) * float64(ts) / 1e9))
		cts = dts
	}
	n, err := m.f.Write(data)
	if err != nil {
		return err
	}
	tr.samples = append(tr.samples, sample{offset: m.pos, size: uint32(n), dts: dts, cts: cts, key: fr.Keyframe}) //nolint:gosec // a sample size
	m.pos += int64(n)
	return nil
}

// videoSample strips an hvc1 sample of its parameter sets (they go in the
// sample entry), noting layer 1's and the 3D reference displays SEI on
// the way.
// lead is the most a sample's decode time runs ahead of its presentation
// time (B pictures), on the track's own timelines.
func (tr *track) lead() int64 {
	var lead int64
	for _, s := range tr.samples {
		lead = max(lead, s.dts-s.cts)
	}
	return lead
}

func (tr *track) videoSample(data []byte) ([]byte, error) {
	out := make([]byte, 0, len(data))
	for len(data) >= 4 {
		l := int(binary.BigEndian.Uint32(data))
		if l < 2 || 4+l > len(data) {
			return nil, errors.New("mp4: a video frame's NAL unit length is wrong")
		}
		nal := data[4 : 4+l]
		data = data[4+l:]
		typ := nal[0] >> 1 & 0x3f
		layer := int(nal[0]&1)<<5 | int(nal[1]>>3)
		switch {
		case typ >= 32 && typ <= 34:
			if layer > 0 && len(tr.layer1) < 2 && typ != 32 {
				tr.layer1 = append(tr.layer1, append([]byte(nil), nal...))
			}
			continue // out of band
		case typ == 39 && layer == 0 && len(nal) > 2 && nal[2] == 176 && tr.sei3D == nil:
			tr.sei3D = append([]byte(nil), nal...)
		}
		out = binary.BigEndian.AppendUint32(out, uint32(l)) //nolint:gosec // a NAL unit length
		out = append(out, nal...)
	}
	return out, nil
}

// box helpers

func box(typ string, body ...[]byte) []byte {
	n := 8
	for _, b := range body {
		n += len(b)
	}
	out := make([]byte, 0, n)
	out = binary.BigEndian.AppendUint32(out, uint32(n)) //nolint:gosec // a box size
	out = append(out, typ...)
	for _, b := range body {
		out = append(out, b...)
	}
	return out
}

func fullBox(typ string, version byte, flags uint32, body ...[]byte) []byte {
	hdr := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append([][]byte{hdr}, body...)...)
}

func u16(v int) []byte { return []byte{byte(v >> 8), byte(v)} }
func u32(v int64) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}
func u64(v int64) []byte {
	return binary.BigEndian.AppendUint64(nil, uint64(v)) //nolint:gosec // a count or offset
}

func (m *muxer) writeBox(typ string, body []byte) error {
	n, err := m.f.Write(box(typ, body))
	m.pos += int64(n)
	return err
}

func (m *muxer) ftyp() []byte {
	if m.o.QuickTime {
		return bytes.Join([][]byte{[]byte("qt  "), u32(0x200), []byte("qt  ")}, nil)
	}
	return bytes.Join([][]byte{[]byte("isom"), u32(0x200), []byte("isom"), []byte("iso2"), []byte("mp41"), []byte("hvc1")}, nil)
}

const movieTimescale = 1000

// moov builds the movie box: the header, then a track per source.
func (m *muxer) moov() ([]byte, error) {
	var dur int64 // the longest track, in the movie timescale
	var traks [][]byte
	for i, tr := range m.tracks {
		if len(tr.samples) == 0 {
			continue
		}
		b, d, err := m.trak(i+1, tr)
		if err != nil {
			return nil, err
		}
		traks = append(traks, b)
		dur = max(dur, d)
	}
	mvhd := fullBox("mvhd", 1, 0,
		u64(0), u64(0), // creation, modification
		u32(movieTimescale), u64(dur),
		u32(0x00010000), u16(0x0100), u16(0), u32(0), u32(0), // rate, volume, reserved
		identityMatrix(),
		make([]byte, 24),            // pre_defined
		u32(int64(len(m.tracks)+1)), // next_track_ID
	)
	return bytes.Join(append([][]byte{mvhd}, traks...), nil), nil
}

func identityMatrix() []byte {
	return bytes.Join([][]byte{u32(0x00010000), u32(0), u32(0), u32(0), u32(0x00010000), u32(0), u32(0), u32(0), u32(0x40000000)}, nil)
}

// trak builds a track box and gives its duration in the movie timescale.
func (m *muxer) trak(id int, tr *track) ([]byte, int64, error) {
	ts := tr.timescale(m.o)
	// Presentation: a video's decode times may run ahead of its
	// presentation times (B pictures), so the whole media is shifted
	// later by the largest lead and an edit skips that lead; a track that
	// starts later than time zero gets an empty edit first.
	var mediaDur int64
	if n := len(tr.samples); n > 0 && !tr.video() {
		mediaDur = tr.samples[n-1].dts + m.audioDelta(tr, n-1)
	}
	startMovie := int64(math.Round(float64(tr.delay) / 1e9 * movieTimescale))
	var lead int64
	if tr.video() {
		startMovie = 0 // the video's first presentation time is in its samples
		if len(tr.samples) > 0 {
			first, last := tr.samples[0].cts, tr.samples[0].cts
			for _, s := range tr.samples {
				first, last = min(first, s.cts), max(last, s.cts)
			}
			startMovie = int64(math.Round(float64(first) / float64(ts) * movieTimescale))
			// The media's decode timeline starts at the first sample's
			// decode time and its presentation timeline at the first
			// picture shown; the lead is the most a decode time runs
			// ahead of its presentation time on those.
			dts0 := tr.samples[0].dts
			for i := range tr.samples {
				tr.samples[i].cts -= first
				tr.samples[i].dts -= dts0
			}
			lead = tr.lead()
			// the media runs to the end of the last picture shown, after
			// the shift by the lead
			mediaDur = last - first + lead + int64(m.o.FPSDen)
		}
	}
	shownDur := mediaDur - lead
	durMovie := int64(math.Round(float64(shownDur) / float64(ts) * movieTimescale))
	var edits []byte
	n := 0
	if startMovie > 0 {
		edits = append(edits, u64(startMovie)...)
		edits = append(edits, u64(-1)...) // an empty edit
		edits = append(edits, u32(0x00010000)...)
		n++
	}
	edits = append(edits, u64(durMovie)...)
	edits = append(edits, u64(lead)...)
	edits = append(edits, u32(0x00010000)...)
	n++
	elst := fullBox("elst", 1, 0, u32(int64(n)), edits)
	edts := box("edts", elst)

	var width, height int
	volume := 0
	if tr.video() {
		width, height = tr.t.Width, tr.t.Height
	} else {
		volume = 0x0100
	}
	tkhd := fullBox("tkhd", 1, 7, // enabled, in movie, in preview
		u64(0), u64(0), u32(int64(id)), u32(0), u64(startMovie+durMovie),
		u32(0), u32(0), // reserved
		u16(0), u16(0), u16(volume), u16(0), // layer, alternate group, volume, reserved
		identityMatrix(),
		u32(int64(width)<<16), u32(int64(height)<<16),
	)
	lang := tr.t.Language
	if len(lang) != 3 {
		lang = "und"
	}
	var packed int
	for _, c := range lang {
		packed = packed<<5 | (int(c) - 0x60)
	}
	mdhd := fullBox("mdhd", 1, 0, u64(0), u64(0), u32(ts), u64(mediaDur), u16(packed), u16(0))
	handler, name := "soun", "SoundHandler\x00"
	if tr.video() {
		handler, name = "vide", "VideoHandler\x00"
	}
	hdlr := fullBox("hdlr", 0, 0, u32(0), []byte(handler), make([]byte, 12), []byte(name))
	var mhd []byte
	if tr.video() {
		mhd = fullBox("vmhd", 0, 1, make([]byte, 8))
	} else {
		mhd = fullBox("smhd", 0, 0, make([]byte, 4))
	}
	dinf := box("dinf", fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1)))
	stbl, err := m.stbl(tr)
	if err != nil {
		return nil, 0, err
	}
	minf := box("minf", mhd, dinf, stbl)
	mdia := box("mdia", mdhd, hdlr, minf)
	return box("trak", tkhd, edts, mdia), startMovie + durMovie, nil
}

// audioDelta is the duration of an audio sample (a frame) in the track's
// timescale: the gap to the next, the previous one's for the last.
func (m *muxer) audioDelta(tr *track, i int) int64 {
	if i+1 < len(tr.samples) {
		return tr.samples[i+1].dts - tr.samples[i].dts
	}
	if i > 0 {
		return tr.samples[i].dts - tr.samples[i-1].dts
	}
	return 1536
}

// stbl builds the sample table: the entry, timing, sync samples, and
// where each sample is (one sample per chunk).
func (m *muxer) stbl(tr *track) ([]byte, error) {
	entry, err := m.sampleEntry(tr)
	if err != nil {
		return nil, err
	}
	stsd := fullBox("stsd", 0, 0, u32(1), entry)
	// stts: runs of equal sample durations
	type run struct{ count, delta int64 }
	var runs []run
	for i, s := range tr.samples {
		var d int64
		if tr.video() {
			d = int64(m.o.FPSDen)
		} else {
			d = m.audioDelta(tr, i)
		}
		if len(runs) > 0 && runs[len(runs)-1].delta == d {
			runs[len(runs)-1].count++
		} else {
			runs = append(runs, run{1, d})
		}
		_ = s
	}
	var sttsBody []byte
	for _, r := range runs {
		sttsBody = append(sttsBody, u32(r.count)...)
		sttsBody = append(sttsBody, u32(r.delta)...)
	}
	stts := fullBox("stts", 0, 0, u32(int64(len(runs))), sttsBody)
	parts := [][]byte{stsd, stts}
	if tr.video() {
		// composition offsets, after the media's shift by the lead
		lead := tr.lead()
		type crun struct{ count, offset int64 }
		var cruns []crun
		for _, s := range tr.samples {
			o := s.cts + lead - s.dts
			if len(cruns) > 0 && cruns[len(cruns)-1].offset == o {
				cruns[len(cruns)-1].count++
			} else {
				cruns = append(cruns, crun{1, o})
			}
		}
		var cttsBody []byte
		for _, r := range cruns {
			cttsBody = append(cttsBody, u32(r.count)...)
			cttsBody = append(cttsBody, u32(r.offset)...)
		}
		parts = append(parts, fullBox("ctts", 0, 0, u32(int64(len(cruns))), cttsBody))
		var keys []byte
		nkeys := 0
		for i, s := range tr.samples {
			if s.key {
				keys = append(keys, u32(int64(i+1))...)
				nkeys++
			}
		}
		parts = append(parts, fullBox("stss", 0, 0, u32(int64(nkeys)), keys))
	}
	parts = append(parts, fullBox("stsc", 0, 0, u32(1), u32(1), u32(1), u32(1)))
	var sizes []byte
	for _, s := range tr.samples {
		sizes = append(sizes, u32(int64(s.size))...)
	}
	parts = append(parts, fullBox("stsz", 0, 0, u32(0), u32(int64(len(tr.samples))), sizes))
	var offsets []byte
	for _, s := range tr.samples {
		offsets = append(offsets, u64(s.offset)...)
	}
	parts = append(parts, fullBox("co64", 0, 0, u32(int64(len(tr.samples))), offsets))
	return box("stbl", parts...), nil
}

// sampleEntry builds the track's sample description.
func (m *muxer) sampleEntry(tr *track) ([]byte, error) {
	if tr.video() {
		return m.hvc1(tr)
	}
	switch tr.t.CodecID {
	case "A_AC3":
		return m.ac3(tr, false)
	case "A_EAC3":
		return m.ac3(tr, true)
	}
	return nil, fmt.Errorf("mp4: audio %s is not written (AC-3 and E-AC-3 are)", tr.t.CodecID)
}

func (m *muxer) hvc1(tr *track) ([]byte, error) {
	if tr.t.CodecID != "V_MPEGH/ISO/HEVC" {
		return nil, fmt.Errorf("mp4: video %s is not written (HEVC is)", tr.t.CodecID)
	}
	hvcC := append([]byte(nil), tr.t.CodecPrivate...)
	if len(hvcC) < 23 {
		return nil, errors.New("mp4: no HEVC decoder configuration")
	}
	stereo := m.o.Spatial != nil
	if stereo {
		if len(tr.layer1) < 2 {
			return nil, errors.New("mp4: a stereo video without a second layer's parameter sets")
		}
		if tr.sei3D != nil {
			// the 3D reference displays SEI goes with the base layer's
			// parameter sets (Apple's profile)
			hvcC[22]++
			hvcC = append(hvcC, 0x80|39, 0, 1, byte(len(tr.sei3D)>>8), byte(len(tr.sei3D)))
			hvcC = append(hvcC, tr.sei3D...)
		}
	}
	entry := bytes.Join([][]byte{
		make([]byte, 6), u16(1), // reserved, data_reference_index
		u16(0), u16(0), make([]byte, 12),
		u16(tr.t.Width), u16(tr.t.Height),
		u32(0x00480000), u32(0x00480000), u32(0), u16(1), // resolution, reserved, frame_count
		make([]byte, 32),         // compressorname
		u16(0x0018), u16(0xffff), // depth, pre_defined
		box("hvcC", hvcC),
	}, nil)
	if stereo {
		entry = append(entry, box("lhvC", m.lhvC(hvcC, tr.layer1))...)
	}
	if c := tr.t.Colour; c != nil {
		rng := byte(0)
		if c.FullRange {
			rng = 0x80
		}
		entry = append(entry, box("colr", []byte("nclx"), u16(c.Primaries), u16(c.Transfer), u16(c.Matrix), []byte{rng})...)
	}
	entry = append(entry, box("pasp", u32(1), u32(1))...)
	if stereo {
		entry = append(entry, m.vexu()...)
	}
	return box("hvc1", entry), nil
}

// lhvC is the layered HEVC decoder configuration record (ISO/IEC
// 14496-15 9.6): the second layer's SPS and PPS, with the temporal
// layering of the base.
func (m *muxer) lhvC(hvcC []byte, layer1 [][]byte) []byte {
	b := []byte{1, 0xf0, 0, 0xfc, hvcC[21]&^0xc0 | 0xc0} // version, min_spatial_segmentation, parallelism, numTemporalLayers/nesting/length
	b[4] = 0xc0 | hvcC[21]&0x3f
	arrays := map[byte][][]byte{}
	var order []byte
	for _, nal := range layer1 {
		t := nal[0] >> 1 & 0x3f
		if _, ok := arrays[t]; !ok {
			order = append(order, t)
		}
		arrays[t] = append(arrays[t], nal)
	}
	b = append(b, byte(len(order)))
	for _, t := range order {
		b = append(b, 0x80|t, byte(len(arrays[t])>>8), byte(len(arrays[t])))
		for _, nal := range arrays[t] {
			b = append(b, byte(len(nal)>>8), byte(len(nal)))
			b = append(b, nal...)
		}
	}
	return b
}

// vexu is Apple's video extended usage box with the horizontal field of
// view beside it: a stereo pair with both eyes, the hero eye, the camera
// baseline, the disparity adjustment, a rectilinear projection.
func (m *muxer) vexu() []byte {
	s := m.o.Spatial
	hero := byte(1)
	if s.HeroRight {
		hero = 2
	}
	// The stereo view box holds the eyes' description and, as Apple's
	// hierarchy has them, the camera system and comfort boxes; the
	// projection is the usage box's own.
	eyes := [][]byte{
		fullBox("stri", 0, 0, []byte{0x03}), // has_left_eye_view, has_right_eye_view
		fullBox("hero", 0, 0, []byte{hero}),
	}
	if s.Baseline > 0 {
		eyes = append(eyes, box("cams", fullBox("blin", 0, 0, u32(int64(math.Round(s.Baseline*1000))))))
	}
	if s.Disparity != 0 {
		eyes = append(eyes, box("cmfy", fullBox("dadj", 0, 0, u32(int64(math.Round(max(-1, min(1, s.Disparity))*10000))))))
	}
	out := box("vexu", box("eyes", eyes...), box("proj", fullBox("prji", 0, 0, []byte("rect"))))
	if s.FOV > 0 {
		out = append(out, box("hfov", u32(int64(math.Round(s.FOV*1000))))...)
	}
	return out
}

// ac3 builds an AC-3 ('ac-3' with dac3) or E-AC-3 ('ec-3' with dec3)
// sample entry from the track and its first frame (ETSI TS 102 366 F.4
// and F.6).
func (m *muxer) ac3(tr *track, eac3 bool) ([]byte, error) {
	f := tr.first
	if len(f) < 8 || f[0] != 0x0b || f[1] != 0x77 {
		return nil, errors.New("mp4: the audio does not start with an AC-3 frame")
	}
	var spec []byte
	typ := "ac-3"
	if !eac3 {
		fscod, frmsizecod := f[4]>>6, f[4]&0x3f
		bsid, bsmod := f[5]>>3, f[5]&7
		acmod := f[6] >> 5
		// lfeon follows acmod and the fields acmod brings (cmixlev, surmixlev, dsurmod)
		r := bitReader{b: f[6:]}
		r.u(3)
		if acmod&1 != 0 && acmod != 1 {
			r.u(2)
		}
		if acmod&4 != 0 {
			r.u(2)
		}
		if acmod == 2 {
			r.u(2)
		}
		lfeon := r.u(1)
		v := uint32(fscod)<<22 | uint32(bsid)<<17 | uint32(bsmod)<<14 | uint32(acmod)<<11 | lfeon<<10 | uint32(frmsizecod>>1)<<5
		spec = box("dac3", []byte{byte(v >> 16), byte(v >> 8), byte(v)})
	} else {
		typ = "ec-3"
		r := bitReader{b: f[2:]}
		r.u(2) // strmtyp
		r.u(3) // substreamid
		frmsiz := r.u(11)
		fscod := r.u(2)
		numblks := uint32(6)
		if fscod == 3 {
			r.u(2) // fscod2
		} else {
			numblks = [4]uint32{1, 2, 3, 6}[r.u(2)]
		}
		acmod := r.u(3)
		lfeon := r.u(1)
		bsid := r.u(5)
		rate := int64(tr.t.SampleRate)
		if rate == 0 {
			rate = 48000
		}
		dataRate := (int64(frmsiz+1) * 2 * 8 * rate / (int64(numblks) * 256)) / 1000
		v := uint32(dataRate) << 3 //nolint:gosec // kbit/s; num_ind_sub 0
		b := []byte{byte(v >> 8), byte(v)}
		w := fscod<<30 | bsid<<25 | acmod<<17 | lfeon<<16 // reserved, asvc and bsmod 0, no dependent substreams
		b = append(b, byte(w>>24), byte(w>>16), byte(w>>8))
		spec = box("dec3", b)
	}
	entry := bytes.Join([][]byte{
		make([]byte, 6), u16(1), // reserved, data_reference_index
		make([]byte, 8), // reserved
		u16(tr.t.Channels), u16(16), u16(0), u16(0),
		u32(int64(tr.t.SampleRate) << 16),
		spec,
	}, nil)
	return box(typ, entry), nil
}

// bitReader reads bits most significant first.
type bitReader struct {
	b   []byte
	pos int
}

func (r *bitReader) u(n int) uint32 {
	var v uint32
	for ; n > 0; n-- {
		var bit uint32
		if i := r.pos >> 3; i < len(r.b) {
			bit = uint32(r.b[i]>>(7-r.pos&7)) & 1
		}
		v = v<<1 | bit
		r.pos++
	}
	return v
}
