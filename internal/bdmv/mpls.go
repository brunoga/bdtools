// Package bdmv reads the structure of a Blu-ray disc: the playlists that make
// up its titles, and the stream files they refer to, from a BDMV directory or
// directly out of a UDF disc image.
package bdmv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// ticks converts a playlist time, in 45 kHz ticks, to a duration.
func ticks(t uint32) time.Duration { return time.Duration(t) * time.Second / 45000 }

// Stream kinds in a play item's STN table, in the order the table lists them.
const (
	KindVideo = iota
	KindAudio
	KindPG
	KindIG
	KindSecondaryAudio
	KindSecondaryVideo
	KindPiPPG
)

// Stream coding types (stream_coding_type in the STN table and the PMT).
const (
	CodingMPEG2Video = 0x02
	CodingAVC        = 0x1b
	CodingMVC        = 0x20
	CodingHEVC       = 0x24
	CodingVC1        = 0xea
	CodingLPCM       = 0x80
	CodingAC3        = 0x81
	CodingDTS        = 0x82
	CodingTrueHD     = 0x83
	CodingEAC3       = 0x84
	CodingDTSHDHR    = 0x85
	CodingDTSHDMA    = 0x86
	CodingEAC3Sec    = 0xa1
	CodingDTSSec     = 0xa2
	CodingPGS        = 0x90
	CodingIGS        = 0x91
	CodingText       = 0x92
)

// Stream is one entry of a play item's STN table.
type Stream struct {
	Kind int
	// EntryType says where the stream lives: 1 in the play item's own clip,
	// 2 and 4 in a sub-path's clip, 3 in a sub-path's clip in-mux.
	EntryType int
	PID       uint16
	SubPath   int
	SubClip   int
	Coding    byte
	// Format and Rate are the 4-bit video format / frame rate, or audio
	// presentation / sample rate fields.
	Format, Rate byte
	// Lang is the ISO 639-2 code for audio, PG, IG and text streams.
	Lang string
	// OffsetSequence is, for a PG stream of a 3D play item, the offset
	// sequence of the MVC stream's offset metadata that sets its depth:
	// how far the graphics move apart in the two eyes, frame by frame. -1
	// when the playlist gives none.
	OffsetSequence int
}

// PlayItem is one clip of a playlist.
type PlayItem struct {
	Clip  string // five digits, the clip file name without its extension
	Codec string // "M2TS"
	// ConnectionCondition is 1 for a discontinuity and 5 or 6 for a
	// seamless join with the previous item.
	ConnectionCondition int
	STCID               int
	// InTime and OutTime bound the item on its clip's timeline, in ticks.
	InTime, OutTime uint32
	// Angles lists the other angles' clips of a multi-angle item.
	Angles  []string
	Streams []Stream
	// DependentClip is the clip holding the MVC dependent view of this item,
	// from the playlist's MVC sub-path; empty for a 2D item.
	DependentClip string
	// DependentPID is the dependent view's PID from the STN_table_SS, or 0
	// when the playlist does not say (Blu-ray uses 0x1012).
	DependentPID uint16
}

// Duration is how long the item plays.
func (p PlayItem) Duration() time.Duration {
	if p.OutTime <= p.InTime {
		return 0
	}
	return ticks(p.OutTime - p.InTime)
}

// SubPlayItem is one clip of a sub-path.
type SubPlayItem struct {
	Clip                string
	Codec               string
	ConnectionCondition int
	STCID               int
	InTime, OutTime     uint32
	SyncPlayItem        int
	SyncPTS             uint32
	Clips               []string // further clips of a multi-clip item
}

// SubPath is an auxiliary path of a playlist: secondary audio or video, or
// the MVC dependent view (type 8).
type SubPath struct {
	Type  int
	Items []SubPlayItem
}

// SubPathMVC is the sub-path type carrying the MVC dependent view.
const SubPathMVC = 8

// Mark is a playlist mark: a chapter (type 1) or a link point (type 2).
type Mark struct {
	Type     int
	PlayItem int
	Time     uint32 // ticks on the item's clip timeline
	PID      uint16
	Duration uint32
}

// Playlist is a parsed .mpls file.
type Playlist struct {
	Version  string
	Items    []PlayItem
	SubPaths []SubPath
	Marks    []Mark
	// BaseViewIsRight is the mvc_base_view_R_flag: the disc says its base
	// view is the right eye.
	BaseViewIsRight bool
}

// Duration is the total playing time.
func (p *Playlist) Duration() time.Duration {
	var d time.Duration
	for _, it := range p.Items {
		d += it.Duration()
	}
	return d
}

// UniqueDuration is the playing time counting each stretch of a clip once.
// A disc can carry playlists that loop one short clip a hundred times —
// menus, demo loops, and decoys meant to confuse rippers — which last
// longer than the feature without holding more of it.
func (p *Playlist) UniqueDuration() time.Duration {
	type span struct {
		clip    string
		in, out uint32
	}
	seen := map[span]bool{}
	var d time.Duration
	for _, it := range p.Items {
		k := span{it.Clip, it.InTime, it.OutTime}
		if !seen[k] {
			seen[k] = true
			d += it.Duration()
		}
	}
	return d
}

// ThreeD reports whether the playlist carries an MVC dependent view.
func (p *Playlist) ThreeD() bool {
	for _, it := range p.Items {
		if it.DependentClip != "" {
			return true
		}
	}
	return false
}

// Chapters returns the chapter marks as offsets from the start of the
// playlist, in order.
func (p *Playlist) Chapters() []time.Duration {
	var starts []time.Duration
	var acc time.Duration
	for _, it := range p.Items {
		starts = append(starts, acc)
		acc += it.Duration()
	}
	var out []time.Duration
	for _, m := range p.Marks {
		if m.Type != 1 || m.PlayItem < 0 || m.PlayItem >= len(p.Items) {
			continue
		}
		it := p.Items[m.PlayItem]
		if m.Time < it.InTime {
			continue
		}
		out = append(out, starts[m.PlayItem]+ticks(m.Time-it.InTime))
	}
	return out
}

var errShort = errors.New("mpls: truncated")

type cursor struct {
	b   []byte
	pos int
	err error
}

func (c *cursor) need(n int) bool {
	if c.err != nil {
		return false
	}
	if c.pos+n > len(c.b) {
		c.err = errShort
		return false
	}
	return true
}

func (c *cursor) u8() byte {
	if !c.need(1) {
		return 0
	}
	v := c.b[c.pos]
	c.pos++
	return v
}

func (c *cursor) u16() uint16 {
	if !c.need(2) {
		return 0
	}
	v := binary.BigEndian.Uint16(c.b[c.pos:])
	c.pos += 2
	return v
}

func (c *cursor) u32() uint32 {
	if !c.need(4) {
		return 0
	}
	v := binary.BigEndian.Uint32(c.b[c.pos:])
	c.pos += 4
	return v
}

func (c *cursor) str(n int) string {
	if !c.need(n) {
		return ""
	}
	v := string(c.b[c.pos : c.pos+n])
	c.pos += n
	return v
}

func (c *cursor) skip(n int) {
	if c.need(n) {
		c.pos += n
	}
}

// ParseMPLS parses a playlist file.
func ParseMPLS(b []byte) (*Playlist, error) {
	if len(b) < 40 || string(b[:4]) != "MPLS" {
		return nil, errors.New("mpls: not a playlist file")
	}
	p := &Playlist{Version: string(b[4:8])}
	listPos := int(binary.BigEndian.Uint32(b[8:]))
	markPos := int(binary.BigEndian.Uint32(b[12:]))
	extPos := int(binary.BigEndian.Uint32(b[16:]))

	// AppInfoPlayList
	c := &cursor{b: b, pos: 40}
	appLen := int(c.u32())
	c.skip(1)
	c.skip(1) // playback_type
	c.skip(2) // playback_count
	c.skip(8) // uo_mask
	flags := c.u16()
	// random_access_flag, audio_mix_app_flag, lossless_may_bypass_mixer_flag,
	// mvc_base_view_R_flag, from the top bit down.
	p.BaseViewIsRight = flags&0x1000 != 0
	_ = appLen
	if c.err != nil {
		return nil, c.err
	}

	if err := p.parseList(b, listPos); err != nil {
		return nil, err
	}
	if markPos > 0 {
		if err := p.parseMarks(b, markPos); err != nil {
			return nil, err
		}
	}
	if extPos > 0 && extPos < len(b) {
		// Extension data is where 3D lives; a playlist without it is 2D,
		// and a damaged extension should not take the whole playlist down.
		p.parseExtensions(b, extPos)
	}
	p.resolveDependentClips()
	return p, nil
}

func (p *Playlist) parseList(b []byte, pos int) error {
	if pos <= 0 || pos >= len(b) {
		return errors.New("mpls: no PlayList")
	}
	c := &cursor{b: b, pos: pos}
	c.u32() // length
	c.skip(2)
	nItems := int(c.u16())
	nSub := int(c.u16())
	for i := 0; i < nItems && c.err == nil; i++ {
		it, err := parsePlayItem(c)
		if err != nil {
			return err
		}
		p.Items = append(p.Items, it)
	}
	for i := 0; i < nSub && c.err == nil; i++ {
		sp, err := parseSubPath(c)
		if err != nil {
			return err
		}
		p.SubPaths = append(p.SubPaths, sp)
	}
	return c.err
}

func parsePlayItem(c *cursor) (PlayItem, error) {
	var it PlayItem
	length := int(c.u16())
	start := c.pos
	it.Clip = c.str(5)
	it.Codec = c.str(4)
	flags := c.u16()
	multiAngle := flags&0x10 != 0
	it.ConnectionCondition = int(flags & 0x0f)
	it.STCID = int(c.u8())
	it.InTime = c.u32()
	it.OutTime = c.u32()
	c.skip(8) // uo_mask
	c.skip(1) // random_access_flag
	c.skip(1) // still_mode
	c.skip(2) // still_time
	if multiAngle {
		n := int(c.u8())
		c.skip(1) // is_different_audios, is_seamless_angle_change
		for i := 1; i < n && c.err == nil; i++ {
			it.Angles = append(it.Angles, c.str(5))
			c.skip(4) // codec_id
			c.skip(1) // stc_id
		}
	}
	streams, err := parseSTN(c)
	if err != nil {
		return it, err
	}
	it.Streams = streams
	if c.err != nil {
		return it, c.err
	}
	c.pos = start + length
	return it, c.err
}

func parseSTN(c *cursor) ([]Stream, error) {
	length := int(c.u16())
	start := c.pos
	c.skip(2)
	counts := [7]int{}
	for i := range counts {
		counts[i] = int(c.u8())
	}
	c.skip(5)
	var out []Stream
	for kind, n := range counts {
		for i := 0; i < n && c.err == nil; i++ {
			s := Stream{Kind: kind, OffsetSequence: -1}
			// stream_entry
			el := int(c.u8())
			es := c.pos
			s.EntryType = int(c.u8())
			switch s.EntryType {
			case 1:
				s.PID = c.u16()
			case 2, 4:
				s.SubPath = int(c.u8())
				s.SubClip = int(c.u8())
				s.PID = c.u16()
			case 3:
				s.SubPath = int(c.u8())
				s.PID = c.u16()
			}
			c.pos = es + el
			// stream_attributes
			al := int(c.u8())
			as := c.pos
			s.Coding = c.u8()
			switch s.Coding {
			case CodingMPEG2Video, CodingAVC, CodingMVC, CodingHEVC, CodingVC1, 0x01:
				v := c.u8()
				s.Format, s.Rate = v>>4, v&15
			case CodingLPCM, CodingAC3, CodingDTS, CodingTrueHD, CodingEAC3, CodingDTSHDHR, CodingDTSHDMA,
				CodingEAC3Sec, CodingDTSSec, 0x03, 0x04:
				v := c.u8()
				s.Format, s.Rate = v>>4, v&15
				s.Lang = c.str(3)
			case CodingPGS, CodingIGS:
				s.Lang = c.str(3)
			case CodingText:
				c.skip(1) // character code
				s.Lang = c.str(3)
			}
			c.pos = as + al
			if c.err != nil {
				return out, c.err
			}
			out = append(out, s)
		}
	}
	if c.err != nil {
		return out, c.err
	}
	c.pos = start + length
	return out, c.err
}

func parseSubPath(c *cursor) (SubPath, error) {
	var sp SubPath
	length := int(c.u32())
	start := c.pos
	c.skip(1)
	sp.Type = int(c.u8())
	c.skip(2) // reserved, is_repeat
	c.skip(1)
	n := int(c.u8())
	for i := 0; i < n && c.err == nil; i++ {
		var si SubPlayItem
		l := int(c.u16())
		s := c.pos
		si.Clip = c.str(5)
		si.Codec = c.str(4)
		// 19 reserved bits, connection_condition, is_multi_Clip_entries
		f := uint32(c.u16())<<8 | uint32(c.u8())
		si.ConnectionCondition = int(f >> 1 & 0x0f)
		multi := f&1 != 0
		si.STCID = int(c.u8())
		si.InTime = c.u32()
		si.OutTime = c.u32()
		si.SyncPlayItem = int(c.u16())
		si.SyncPTS = c.u32()
		if multi {
			k := int(c.u8())
			for j := 1; j < k && c.err == nil; j++ {
				si.Clips = append(si.Clips, c.str(5))
				c.skip(4)
				c.skip(1)
			}
		}
		if c.err != nil {
			return sp, c.err
		}
		c.pos = s + l
		sp.Items = append(sp.Items, si)
	}
	if c.err != nil {
		return sp, c.err
	}
	c.pos = start + length
	return sp, c.err
}

func (p *Playlist) parseMarks(b []byte, pos int) error {
	if pos >= len(b) {
		return nil
	}
	c := &cursor{b: b, pos: pos}
	c.u32() // length
	n := int(c.u16())
	for i := 0; i < n && c.err == nil; i++ {
		var m Mark
		c.skip(1) // mark_id
		m.Type = int(c.u8())
		m.PlayItem = int(c.u16())
		m.Time = c.u32()
		m.PID = c.u16()
		m.Duration = c.u32()
		if c.err == nil {
			p.Marks = append(p.Marks, m)
		}
	}
	return c.err
}

// parseExtensions reads the extension data block: the MVC sub-paths of a
// 3D playlist are stored there (ID 2/1) rather than in the main list.
func (p *Playlist) parseExtensions(b []byte, pos int) {
	c := &cursor{b: b, pos: pos}
	c.u32() // length
	dataStart := int(c.u32())
	c.skip(3)
	n := int(c.u8())
	_ = dataStart
	for i := 0; i < n && c.err == nil; i++ {
		id1 := c.u16()
		id2 := c.u16()
		start := int(c.u32())
		length := int(c.u32())
		if start <= 0 || length <= 0 || pos+start+length > len(b) {
			continue
		}
		switch {
		case id1 == 2 && id2 == 2:
			// SubPath entries extension: the MVC dependent view's sub-path.
			sc := &cursor{b: b, pos: pos + start}
			sc.u32() // length
			k := int(sc.u16())
			for j := 0; j < k && sc.err == nil; j++ {
				sp, err := parseSubPath(sc)
				if err != nil {
					break
				}
				p.SubPaths = append(p.SubPaths, sp)
			}
		case id1 == 2 && id2 == 1:
			p.parseSTNSS(&cursor{b: b, pos: pos + start})
		}
	}
}

// parseSTNSS reads the STN_table_SS, one per play item: the dependent
// view's stream entry and attributes, then for each PG stream of the
// item's STN_table the offset sequence that sets its depth.
func (p *Playlist) parseSTNSS(c *cursor) {
	for j := range p.Items {
		it := &p.Items[j]
		l := int(c.u16())
		end := c.pos + l
		if c.err != nil || l <= 0 {
			return
		}
		c.skip(2) // Fixed_offset_during_PopUp_flag, reserved
		// stream_entry of the dependent view
		el := int(c.u8())
		es := c.pos
		if c.u8() == 2 {
			c.skip(2)
			it.DependentPID = c.u16()
		}
		c.pos = es + el
		c.skip(int(c.u8())) // stream_attributes
		c.skip(2)           // reserved, number_of_offset_sequences
		for k := range it.Streams {
			s := &it.Streams[k]
			if s.Kind != 2 || c.pos+2 > end {
				continue
			}
			if id := int(c.u8()); id != 0xff {
				s.OffsetSequence = id
			}
			flags := c.u8()
			// A stereoscopic PG stream (left and right streams of its own)
			// or a top or bottom variant for a letterboxed frame: their
			// entries follow, and are skipped.
			entry := func() {
				n := int(c.u8())
				c.skip(n)
			}
			if flags&0x08 != 0 { // is_SS_PG
				entry()
				entry()
				c.skip(2)
			}
			if flags&0x04 != 0 { // is_top_AS_PG
				entry()
				c.skip(1)
			}
			if flags&0x02 != 0 { // is_bottom_AS_PG
				entry()
				c.skip(1)
			}
		}
		if c.err != nil {
			return
		}
		c.pos = end
	}
}

// resolveDependentClips attaches the MVC sub-path's clips to the play items
// they accompany.
func (p *Playlist) resolveDependentClips() {
	for _, sp := range p.SubPaths {
		if sp.Type != SubPathMVC {
			continue
		}
		for i, si := range sp.Items {
			idx := si.SyncPlayItem
			if idx < 0 || idx >= len(p.Items) {
				idx = i
			}
			if idx < len(p.Items) && p.Items[idx].DependentClip == "" {
				p.Items[idx].DependentClip = si.Clip
			}
		}
	}
}

// String summarises a playlist for a listing.
func (p *Playlist) String() string {
	threeD := ""
	if p.ThreeD() {
		threeD = ", 3D"
		if p.BaseViewIsRight {
			threeD += " (base view is the right eye)"
		}
	}
	return fmt.Sprintf("%d item(s), %s%s, %d chapter(s)", len(p.Items), p.Duration().Round(time.Second), threeD, len(p.Chapters()))
}
