package convert

import (
	"bytes"
	"sort"
	"time"
)

// A Blu-ray 3D sets the depth of its subtitles and menus with offset
// metadata in the MVC dependent view: on the first access unit of each GOP,
// an SEI message (user_data_unregistered, with the Blu-ray UUID and the type
// "OFMD", nested in an MVC scalable nesting SEI) gives, for up to 32 offset
// sequences, one offset per frame of the GOP in display order. A player
// draws a graphics plane that follows sequence n shifted that many pixels
// right in the left eye and left in the right one (the other way for an
// offset behind the screen). Each PG stream names its sequence in the
// playlist's STN_table_SS.

// ofmdUUID is the user_data_unregistered UUID Blu-ray offset metadata uses.
var ofmdUUID = []byte{0x17, 0xee, 0x8c, 0x60, 0xf8, 0x4d, 0x11, 0xd9, 0x8c, 0xd6, 0x08, 0x00, 0x20, 0x0c, 0x9a, 0x66}

// offsetGOP is one GOP's offset metadata.
type offsetGOP struct {
	pts int64 // 90 kHz, the GOP's first frame in display order
	// offsets[seq][frame], in pixels: positive toward the viewer.
	offsets [][]int8
}

// parseOffsetMetadata finds the offset metadata in an access unit's NAL
// units (Annex B), if it carries any: the dependent view's, or one holding
// both views (as Matroska keeps them), whose dependent view's SEI follows
// the base view's slices and precedes its own.
func parseOffsetMetadata(au []byte) (offsetGOP, bool) {
	for i := 0; i < len(au); {
		j := bytes.Index(au[i:], []byte{0, 0, 1})
		if j < 0 {
			break
		}
		start := i + j + 3
		if start >= len(au) {
			break
		}
		typ := au[start] & 0x1f
		if typ == 20 {
			break // the dependent view's slices: its SEI comes before them
		}
		end := len(au)
		if k := bytes.Index(au[start:], []byte{0, 0, 1}); k >= 0 {
			end = start + k
		}
		if typ == 6 {
			if g, ok := parseSEIOffsets(unescapeRBSP(au[start+1 : end])); ok {
				return g, true
			}
		}
		i = end
	}
	return offsetGOP{}, false
}

// unescapeRBSP removes emulation prevention bytes.
func unescapeRBSP(b []byte) []byte {
	if !bytes.Contains(b, []byte{0, 0, 3}) {
		return b
	}
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// seiMessages splits SEI RBSP into its messages' types and payloads.
func seiMessages(b []byte, each func(typ int, payload []byte) bool) {
	for len(b) > 1 && b[0] != 0x80 { // the rbsp trailing bits end it
		typ, size := 0, 0
		for len(b) > 0 && b[0] == 0xff {
			typ += 255
			b = b[1:]
		}
		if len(b) == 0 {
			return
		}
		typ += int(b[0])
		b = b[1:]
		for len(b) > 0 && b[0] == 0xff {
			size += 255
			b = b[1:]
		}
		if len(b) == 0 {
			return
		}
		size += int(b[0])
		b = b[1:]
		if size > len(b) {
			return
		}
		if !each(typ, b[:size]) {
			return
		}
		b = b[size:]
	}
}

// parseSEIOffsets reads offset metadata from one SEI NAL unit's messages,
// directly or inside an MVC scalable nesting message.
func parseSEIOffsets(rbsp []byte) (g offsetGOP, ok bool) {
	seiMessages(rbsp, func(typ int, p []byte) bool {
		switch typ {
		case 5:
			g, ok = parseOFMD(p)
		case 37:
			if nested, valid := skipMVCNesting(p); valid {
				seiMessages(nested, func(typ int, p []byte) bool {
					if typ == 5 {
						g, ok = parseOFMD(p)
					}
					return !ok
				})
			}
		}
		return !ok
	})
	return g, ok
}

// skipMVCNesting returns the messages an MVC scalable nesting SEI holds
// (H.264 H.13.1.1), after its header.
func skipMVCNesting(p []byte) ([]byte, bool) {
	r := &bitReader{b: p}
	if r.bit() == 1 { // operation_point_flag
		n := r.ue() // num_view_components_op_minus1
		for range n + 1 {
			r.bits(10) // sei_op_view_id
		}
		r.bits(3) // sei_op_temporal_id
	} else if r.bit() == 0 { // all_view_components_in_au_flag
		n := r.ue() // num_view_components_minus1
		for range n + 1 {
			r.bits(10) // sei_view_id
		}
	}
	if r.err {
		return nil, false
	}
	at := (r.pos + 7) / 8 // sei_nesting_zero_bits to the byte
	if at > len(p) {
		return nil, false
	}
	return p[at:], true
}

// parseOFMD reads a user_data_unregistered payload holding offset
// metadata: the UUID and "OFMD", then frame_rate, the PTS of the GOP's
// first frame (33 bits with marker bits), number_of_offset_sequences,
// number_of_displayed_frames_in_GOP, two reserved bytes, and an offset a
// frame per sequence (a direction bit, set for behind the screen, and 7
// bits of pixels).
func parseOFMD(p []byte) (offsetGOP, bool) {
	if len(p) < 16+4+10 || !bytes.Equal(p[:16], ofmdUUID) || string(p[16:20]) != "OFMD" {
		return offsetGOP{}, false
	}
	h := p[20:]
	// PTS[32..30] ends byte 1; then a marker bit and PTS[29..15], another
	// and PTS[14..0], and a last marker opening byte 6.
	pts := int64(h[1]&7)<<30 | int64(h[2]&0x7f)<<23 | int64(h[3])<<15 | int64(h[4]&0x7f)<<8 | int64(h[5])
	seqs, frames := int(h[6]&0x3f), int(h[7])
	vals := h[10:]
	if seqs == 0 || frames == 0 || len(vals) < seqs*frames {
		return offsetGOP{}, false
	}
	g := offsetGOP{pts: pts, offsets: make([][]int8, seqs)}
	for s := range seqs {
		row := make([]int8, frames)
		for f, v := range vals[s*frames : (s+1)*frames] {
			row[f] = int8(v & 0x7f) //nolint:gosec // 7 bits
			if v&0x80 != 0 {
				row[f] = -row[f]
			}
		}
		g.offsets[s] = row
	}
	return g, true
}

// depthMap is a title's offset metadata on the output's timeline.
type depthMap struct {
	frame time.Duration // one frame
	gops  []depthGOP
}

type depthGOP struct {
	at      time.Duration // the first frame, on the output's timeline
	offsets [][]int8
}

// add records a GOP's offsets, its first frame at at.
func (m *depthMap) add(at time.Duration, g offsetGOP) {
	m.gops = append(m.gops, depthGOP{at, g.offsets})
}

// frontMost, as a sequence, is the one nearest the viewer at each frame:
// for a subtitle track whose sequence the source does not say.
const frontMost = -2

// offset is sequence seq's offset at t, in pixels, positive toward the
// viewer: the frame showing at t, or the GOP's last when t is past it (a
// gap in the metadata). Zero where nothing says.
func (m *depthMap) offset(seq int, t time.Duration) int {
	if m == nil || seq < 0 && seq != frontMost || len(m.gops) == 0 || m.frame <= 0 {
		return 0
	}
	i := sort.Search(len(m.gops), func(i int) bool { return m.gops[i].at > t }) - 1
	if i < 0 {
		return 0
	}
	g := m.gops[i]
	at := func(row []int8) int {
		f := int((t - g.at + m.frame/2) / m.frame)
		return int(row[min(max(f, 0), len(row)-1)])
	}
	if seq == frontMost {
		front := 0
		for k, row := range g.offsets {
			if v := at(row); k == 0 || v > front {
				front = v
			}
		}
		return front
	}
	if seq >= len(g.offsets) {
		return 0
	}
	return at(g.offsets[seq])
}

// sort orders the GOPs by time, as they may arrive out of order.
func (m *depthMap) sort() {
	sort.SliceStable(m.gops, func(i, j int) bool { return m.gops[i].at < m.gops[j].at })
}

// bitReader reads bits most significant first.
type bitReader struct {
	b   []byte
	pos int
	err bool
}

func (r *bitReader) bit() int {
	if r.pos >= 8*len(r.b) {
		r.err = true
		return 0
	}
	v := int(r.b[r.pos/8]>>(7-r.pos%8)) & 1
	r.pos++
	return v
}

func (r *bitReader) bits(n int) int {
	v := 0
	for range n {
		v = v<<1 | r.bit()
	}
	return v
}

func (r *bitReader) ue() int {
	zeros := 0
	for r.bit() == 0 && !r.err {
		zeros++
		if zeros > 31 {
			r.err = true
			return 0
		}
	}
	return 1<<zeros - 1 + r.bits(zeros)
}
