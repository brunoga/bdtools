package bdmv

import "encoding/binary"

// Writing a disc: the files a player reads off a BDMV, for a film of one
// clip (a 3D one with its dependent view's clip beside it), laid out as
// tsMuxeR (the authoring tool these were checked against, byte for byte)
// lays them out: version 0200, the navigation a single title that plays
// the film.

// EPEntry is an entry point of a clip: an access unit a player may start
// decoding at (an IDR picture), its PTS (90 kHz), the source packet it
// starts in, and IEnd, how far into the clip its I picture ends, coded as
// the clip info codes it (I_end_position_offset: 1 to 7, a coarser count
// of packets the higher).
type EPEntry struct {
	PTS  uint64
	SPN  uint32
	IEnd byte
}

// Clip is what a clip info file says of a clip.
type Clip struct {
	// Dependent is an MVC dependent view's clip: its stream and entry
	// points in the file's 3D extensions, not its program.
	Dependent bool
	// RecordingRate is the clip's TS_recording_rate, in bytes a second;
	// Packets its source packets.
	RecordingRate, Packets uint32
	PCRPID                 uint16
	// Start and End are its presentation times, 45 kHz.
	Start, End uint32
	// Streams are the program's (the dependent's: its MVC stream).
	Streams []ClipStream
	// EPPID is the stream the entry points EP are of.
	EPPID uint16
	EP    []EPEntry
	// Extents are the source packets its SSIF extents start at, for a 3D
	// clip (nil for a 2D one).
	Extents []uint32
}

// wbuf is a big-endian file being written.
type wbuf struct{ b []byte }

func (w *wbuf) u8(v byte)    { w.b = append(w.b, v) }
func (w *wbuf) u16(v uint16) { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *wbuf) u32(v uint32) { w.b = binary.BigEndian.AppendUint32(w.b, v) }
func (w *wbuf) str(s string) { w.b = append(w.b, s...) }
func (w *wbuf) zeros(n int)  { w.b = append(w.b, make([]byte, n)...) }

// align pads to a multiple of four bytes.
func (w *wbuf) align() {
	for len(w.b)%4 != 0 {
		w.u8(0)
	}
}

// mark is where a 32-bit field to fill in later is.
func (w *wbuf) mark32() int { w.u32(0); return len(w.b) - 4 }
func (w *wbuf) mark16() int { w.u16(0); return len(w.b) - 2 }

// set32 fills in the 32-bit field at at.
func (w *wbuf) set32(at int, v int) { binary.BigEndian.PutUint32(w.b[at:], uint32(v)) } //nolint:gosec // a file's offsets

// padInside pads to a multiple of four bytes and fills in the 32-bit
// length at at, the padding counted in it.
func (w *wbuf) padInside(at int) {
	w.align()
	w.length32(at)
}

// length32 fills in the 32-bit length at at: the bytes after it.
func (w *wbuf) length32(at int) { w.set32(at, len(w.b)-at-4) }

func (w *wbuf) length16(at int) { binary.BigEndian.PutUint16(w.b[at:], uint16(len(w.b)-at-2)) } //nolint:gosec // a structure's length

// extData is one entry of an ExtensionData block.
type extData struct {
	id1, id2 uint16
	data     []byte
}

// extension writes an ExtensionData block of entries.
func (w *wbuf) extension(entries []extData) {
	l := w.mark32()
	start := w.mark32()
	w.zeros(3)
	w.u8(byte(len(entries))) //nolint:gosec // a handful
	head := len(w.b)
	for _, e := range entries {
		w.u16(e.id1)
		w.u16(e.id2)
		w.u32(0)                   // its start, below
		w.u32(uint32(len(e.data))) //nolint:gosec // small
	}
	w.set32(start, len(w.b)-l)
	for i, e := range entries {
		w.set32(head+12*i+4, len(w.b)-l)
		w.b = append(w.b, e.data...)
	}
	w.length32(l)
}

// Index is the index.bdmv of a disc of one title, a movie (movie object
// 0) that its first playback and top menu (object 1) start.
func Index(threeD bool) []byte {
	w := &wbuf{}
	w.str("INDX0200")
	indexes := w.mark32()
	w.u32(0) // no ExtensionData
	w.zeros(24)
	// AppInfoBDMV
	l := w.mark32()
	flags := byte(0)
	if threeD {
		flags = 0x60 // initial_output_mode_preference 3D, SS_content_exist_flag
	}
	w.u8(flags)
	w.u8(0) // video_format, frame_rate: unset
	w.zeros(32)
	w.length32(l)
	w.set32(indexes, len(w.b))
	l = w.mark32()
	hdmv := func(playback, object uint16) {
		w.u32(0x40000000) // object_type HDMV
		w.u16(playback << 14)
		w.u16(object)
		w.u32(0)
	}
	hdmv(1, 1) // FirstPlayback: interactive, movie object 1
	hdmv(1, 1) // TopMenu: the same
	w.u16(1)   // number_of_Titles
	hdmv(0, 0) // title 1: a movie, movie object 0
	w.length32(l)
	return w.b
}

// HDMV navigation commands: an opcode and two operands.
const (
	cmdPlayPL    = 0x22800000 // PlayPL, the playlist immediate
	cmdJumpTitle = 0x21810000 // JumpTitle, the title immediate
)

// MovieObjects is the MovieObject.bdmv of Index's disc: object 0, the
// title, plays playlist 0; object 1, the first playback and the top
// menu, jumps to title 1.
func MovieObjects() []byte {
	objects := [][][3]uint32{
		{{cmdPlayPL, 0, 0}},
		{{cmdJumpTitle, 1, 0}},
	}
	w := &wbuf{}
	w.str("MOBJ0200")
	w.u32(0) // no ExtensionData
	w.zeros(28)
	l := w.mark32()
	w.u32(0)
	w.u16(uint16(len(objects))) //nolint:gosec // two
	for _, cmds := range objects {
		w.u16(0x8000)            // resume_intention_flag
		w.u16(uint16(len(cmds))) //nolint:gosec // one
		for _, c := range cmds {
			w.u32(c[0])
			w.u32(c[1])
			w.u32(c[2])
		}
	}
	w.length32(l)
	return w.b
}

// CLPI is the clip's clip info file.
func (c *Clip) CLPI() []byte {
	w := &wbuf{}
	w.str("HDMV0200")
	seq, prog, cpi, mark, ext := w.mark32(), w.mark32(), w.mark32(), w.mark32(), w.mark32()
	w.zeros(12)
	// ClipInfo
	l := w.mark32()
	w.u16(0)
	w.u8(1) // clip_stream_type
	if c.Dependent {
		w.u8(8) // application_type: a sub-TS of a 3D sub-path
	} else {
		w.u8(1) // application_type: a main TS of a movie
	}
	w.u32(0) // is_ATC_delta
	w.u32(c.RecordingRate)
	w.u32(c.Packets)
	w.zeros(128)
	w.u16(30) // TS_type_info_block
	w.u8(0x80)
	w.str("HDMV")
	w.zeros(9 + 16)
	w.length32(l)
	// SequenceInfo: one ATC and one STC sequence
	w.set32(seq, len(w.b))
	l = w.mark32()
	w.u8(0)
	w.u8(1)
	w.u32(0)
	w.u8(1)
	w.u8(0)
	w.u16(c.PCRPID)
	w.u32(0)
	w.u32(c.Start)
	w.u32(c.End)
	w.length32(l)
	// ProgramInfo (a dependent view's stream is in its ProgramInfo_SS),
	// each section on four bytes
	w.align()
	w.set32(prog, len(w.b))
	if c.Dependent {
		w.programInfo(nil)
	} else {
		w.programInfo(c.Streams)
	}
	w.align()
	w.set32(cpi, len(w.b))
	if c.Dependent {
		w.u32(0) // its entry points in its CPI_SS
	} else {
		w.cpi(c.EPPID, c.EP)
	}
	w.align()
	w.set32(mark, len(w.b))
	w.u32(0) // ClipMark
	if c.Extents == nil {
		return w.b
	}
	w.align()
	w.set32(ext, len(w.b))
	starts := &wbuf{}
	sl := starts.mark32()
	starts.u16(0)
	starts.u16(uint16(len(c.Extents))) //nolint:gosec // a film's extents fit
	for _, s := range c.Extents {
		starts.u32(s)
	}
	starts.length32(sl)
	entries := []extData{{2, 4, starts.b}} // ExtentStartPoint
	if c.Dependent {
		pi, cp := &wbuf{}, &wbuf{}
		pi.programInfo(c.Streams)
		pi.padInside(0)
		cp.cpi(c.EPPID, c.EP)
		entries = append(entries, extData{2, 5, pi.b}, extData{2, 6, cp.b}) // ProgramInfo_SS, CPI_SS
	}
	w.extension(entries)
	return w.b
}

// programInfo writes a ProgramInfo of one program of streams.
func (w *wbuf) programInfo(streams []ClipStream) {
	l := w.mark32()
	w.u8(0)
	w.u8(1)                  // number_of_programs
	w.u32(0)                 // SPN_program_sequence_start
	w.u16(0x100)             // program_map_PID
	w.u8(byte(len(streams))) //nolint:gosec // a handful
	w.u8(0)                  // num_groups
	for _, s := range streams {
		w.u16(s.PID)
		w.u8(21)
		start := len(w.b)
		w.u8(s.Coding)
		switch {
		case isVideo(s.Coding):
			w.u8(s.Format<<4 | s.Rate)
			w.u8(s.Aspect << 4)
			w.zeros(2)
		case s.Coding == CodingPGS || s.Coding == CodingIGS:
			w.str(lang(s.Lang))
			w.u8(0)
		default: // audio
			w.u8(s.Format<<4 | s.Rate)
			w.str(lang(s.Lang))
		}
		w.str("000000000000") // ISRC, unset
		w.b = append(w.b, make([]byte, 21-(len(w.b)-start))...)
	}
	w.length32(l)
}

func isVideo(coding byte) bool {
	switch coding {
	case 0x01, CodingMPEG2Video, CodingAVC, CodingMVC, CodingHEVC, CodingVC1:
		return true
	}
	return false
}

// lang is a language code as three bytes ("und" when there is none).
func lang(l string) string {
	if len(l) != 3 {
		return "und"
	}
	return l
}

// cpi writes a CPI: the EP_map of one stream's entry points, each split
// into a coarse entry (the PTS's bits 32 to 19, the whole SPN), made when
// those change, and a fine one (the PTS's bits 19 to 9, the SPN's low 17).
func (w *wbuf) cpi(pid uint16, ep []EPEntry) {
	type coarse struct {
		fine uint32
		pts  uint32
		spn  uint32
	}
	var cs []coarse
	for i, e := range ep {
		pts := uint32(e.PTS>>19) & 0x3fff //nolint:gosec // 14 bits of a PTS
		if n := len(cs); n == 0 || cs[n-1].pts != pts || cs[n-1].spn>>17 != e.SPN>>17 {
			cs = append(cs, coarse{uint32(i), pts, e.SPN}) //nolint:gosec // entry counts fit
		}
	}
	l := w.mark32()
	w.u16(1) // CPI_type: EP_map
	w.u8(0)
	w.u8(1) // number_of_stream_PID_entries
	w.u16(pid)
	// reserved 10, EP_stream_type 4 (1, video), coarse entries 16, fine 18
	v := uint64(1)<<34 | uint64(len(cs))<<18 | uint64(len(ep)) //nolint:gosec // counts
	w.u16(uint16(v >> 32))
	w.u32(uint32(v))             //nolint:gosec // its low half
	w.u32(14)                    // EP_map_for_one_stream_PID_start_address
	w.u32(uint32(4 + 8*len(cs))) //nolint:gosec // EP_fine_table_start_address
	for _, c := range cs {
		w.u32(c.fine<<14 | c.pts)
		w.u32(c.spn)
	}
	for _, e := range ep {
		w.u32(uint32(e.IEnd&7)<<28 | (uint32(e.PTS>>9)&0x7ff)<<17 | e.SPN&0x1ffff) //nolint:gosec // bit fields
	}
	w.length32(l)
}

// MPLS is the playlist file of p: its items' stream tables and marks, and
// for a 3D playlist its dependent view's sub-path and STN_table_SS.
func (p *Playlist) MPLS() []byte {
	w := &wbuf{}
	w.str("MPLS0200")
	list, marks, ext := w.mark32(), w.mark32(), w.mark32()
	w.zeros(20)
	// AppInfoPlayList
	l := w.mark32()
	w.u8(0)
	w.u8(1)  // playback_type: sequential
	w.u16(0) // playback_count
	w.zeros(8)
	flags := uint16(0x4000) // audio_mix_app_flag
	if p.BaseViewIsRight {
		flags |= 0x1000
	}
	w.u16(flags)
	w.length32(l)
	w.set32(list, len(w.b))
	var main, mvc []SubPath
	for _, sp := range p.SubPaths {
		if sp.Type == SubPathMVC {
			mvc = append(mvc, sp)
		} else {
			main = append(main, sp)
		}
	}
	l = w.mark32()
	w.u16(0)
	w.u16(uint16(len(p.Items))) //nolint:gosec // a playlist's items fit
	w.u16(uint16(len(main)))    //nolint:gosec // and its sub-paths
	for _, it := range p.Items {
		w.playItem(it)
	}
	for _, sp := range main {
		w.subPath(sp)
	}
	w.length32(l)
	w.set32(marks, len(w.b))
	l = w.mark32()
	w.u16(uint16(len(p.Marks))) //nolint:gosec // a playlist's marks fit
	for _, m := range p.Marks {
		w.u8(0)
		w.u8(byte(m.Type))        //nolint:gosec // 1 or 2
		w.u16(uint16(m.PlayItem)) //nolint:gosec // an item index
		w.u32(m.Time)
		w.u16(m.PID)
		w.u32(m.Duration)
	}
	w.length32(l)
	if len(mvc) == 0 {
		return w.b
	}
	w.set32(ext, len(w.b))
	ss := &wbuf{}
	for _, it := range p.Items {
		ss.stnSS(it)
	}
	sub := &wbuf{}
	sl := sub.mark32()
	sub.u16(uint16(len(mvc))) //nolint:gosec // one
	for _, sp := range mvc {
		sub.subPath(sp)
	}
	sub.padInside(sl)
	w.extension([]extData{{2, 1, ss.b}, {2, 2, sub.b}}) // STN_table_SS, SubPath entries
	return w.b
}

func (w *wbuf) playItem(it PlayItem) {
	l := w.mark16()
	w.str(it.Clip)
	w.str(codecOr(it.Codec))
	w.u16(uint16(it.ConnectionCondition & 0xf)) //nolint:gosec // 4 bits
	w.u8(byte(it.STCID))                        //nolint:gosec // an STC id
	w.u32(it.InTime)
	w.u32(it.OutTime)
	w.zeros(8) // UO_mask_table
	w.u8(0)    // PlayItem_random_access_flag
	w.u8(0)    // still_mode
	w.u16(0)   // still_time
	// STN_table: the streams by kind, in the kinds' order
	sl := w.mark16()
	w.u16(0)
	var counts [7]byte
	for _, s := range it.Streams {
		if s.Kind < len(counts) {
			counts[s.Kind]++
		}
	}
	w.b = append(w.b, counts[:]...)
	w.zeros(5)
	for k := range counts {
		for _, s := range it.Streams {
			if s.Kind != k {
				continue
			}
			w.u8(9)
			w.u8(1) // in the play item's clip
			w.u16(s.PID)
			w.zeros(6)
			w.streamAttributes(s)
		}
	}
	w.length16(sl)
	w.length16(l)
}

// streamAttributes writes an STN table stream's attributes.
func (w *wbuf) streamAttributes(s Stream) {
	w.u8(5)
	w.u8(s.Coding)
	switch {
	case isVideo(s.Coding):
		w.u8(s.Format<<4 | s.Rate)
		w.zeros(3)
	case s.Coding == CodingPGS || s.Coding == CodingIGS:
		w.str(lang(s.Lang))
		w.u8(0)
	default:
		w.u8(s.Format<<4 | s.Rate)
		w.str(lang(s.Lang))
	}
}

func codecOr(c string) string {
	if len(c) != 4 {
		return "M2TS"
	}
	return c
}

func (w *wbuf) subPath(sp SubPath) {
	l := w.mark32()
	w.u8(0)
	w.u8(byte(sp.Type)) //nolint:gosec // a sub-path type
	w.u16(0)
	w.u8(0)
	w.u8(byte(len(sp.Items))) //nolint:gosec // a few
	for _, si := range sp.Items {
		il := w.mark16()
		w.str(si.Clip)
		w.str(codecOr(si.Codec))
		w.u32(uint32(si.ConnectionCondition&0xf) << 1) //nolint:gosec // SP_connection_condition
		w.u8(byte(si.STCID))                           //nolint:gosec // an STC id
		w.u32(si.InTime)
		w.u32(si.OutTime)
		w.u16(uint16(si.SyncPlayItem)) //nolint:gosec // an item index
		w.u32(si.SyncPTS)
		w.length16(il)
	}
	w.length32(l)
}

// stnSS writes a play item's STN_table_SS: its dependent view's stream
// (in the MVC sub-path's clip) and each PG stream's offset sequence.
func (w *wbuf) stnSS(it PlayItem) {
	l := w.mark16()
	w.u16(0) // Fixed_offset_during_PopUp_flag
	w.u8(9)
	w.u8(2) // in a sub-path's clip
	w.u8(0) // ref_to_SubPath_id
	w.u8(0) // ref_to_subClip_entry_id
	pid := it.DependentPID
	if pid == 0 {
		pid = 0x1012
	}
	w.u16(pid)
	w.zeros(4)
	video := Stream{Coding: CodingMVC}
	for _, s := range it.Streams {
		if s.Kind == KindVideo {
			video.Format, video.Rate = s.Format, s.Rate
			break
		}
	}
	w.streamAttributes(video)
	w.u8(0)
	w.u8(byte(it.OffsetSequences)) //nolint:gosec // up to 32
	for _, s := range it.Streams {
		if s.Kind != KindPG {
			continue
		}
		id := byte(0xff)
		if s.OffsetSequence >= 0 {
			id = byte(s.OffsetSequence) //nolint:gosec // up to 31
		}
		w.u8(id)
		w.u8(0)
	}
	w.length16(l)
}
