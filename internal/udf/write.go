// Package udf writes UDF 2.50 file system images, a Blu-ray disc's:
// read-only, its files' entries and directories in a metadata partition
// (with its mirror), their contents in the physical partition laid out in
// an order the caller chooses, so that one file (a 3D film's SSIF) can be
// made of pieces of others, interleaved.
//
// The layout is tsMuxeR's: the volume recognition sequence at sector 16,
// the volume descriptors at 32 (their reserve copy after the partition),
// the integrity descriptor at 64, anchors at 256 and at the last sector,
// the partition from 288 (its metadata file's entry first, the metadata
// from block 32, the files' contents, the metadata's mirror).
package udf

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"time"
)

// Sector is the image's sector and logical block size.
const Sector = 2048

// File is a file or directory of the image.
type File struct {
	Name     string
	Dir      bool
	Children []*File
	// Size is a file's length; Content gives its bytes (from the start,
	// read once, in the order the image needs them: Write reads the
	// pieces of each file in order).
	Size    int64
	Content func() (io.Reader, error)
	// RealTime marks a stream file (UDF's real-time file, as Blu-ray's
	// m2ts and ssif are).
	RealTime bool
	// Parts, when set, make the file of pieces of other files (each in
	// Write's order), not of content of its own.
	Parts []Piece

	// the layout's
	icb    uint32  // its entry's block in the metadata partition
	extra  uint32  // its allocation extent descriptors' blocks there
	nExtra int     // how many
	fids   uint32  // a directory's identifiers' first block there
	fidLen int     // their bytes
	exts   []piece // a file's extents in the physical partition
	id     uint64  // its unique id
	parent *File
}

// Piece is a stretch of a file, [Off, Off+Len), in the image's data
// order.
type Piece struct {
	F        *File
	Off, Len int64
}

// piece is a laid out stretch: its block in the physical partition.
type piece struct {
	Piece
	block uint32
}

// Options are the image's names and time.
type Options struct {
	// Label is the volume's name.
	Label string
	// Time is when it was recorded (the files' times too).
	Time time.Time
}

const (
	vrsSector    = 16
	mainVDS      = 32
	lvidSector   = 64
	anchorSector = 256
	partStart    = 288
	metaStart    = 32                             // the metadata's first block in the partition
	alignment    = 32                             // UDF 2.50's allocation and alignment unit, blocks
	maxExtent    = (1<<30 - Sector) &^ (6144 - 1) // a long_ad's longest, whole aligned units
	adsInEntry   = (Sector - efeHeader) / 16
	adsInAED     = (Sector - 24) / 16
	efeHeader    = 216
	implementor  = "*bdtools"
	fileTypeDir  = 4
	fileTypeFile = 5
	fileTypeRT   = 249
	fileTypeMeta = 250
	fileTypeMirr = 251
	permFile     = 0x1084 // read, for all
	permDir      = 0x14a5 // read and search, for all
)

// Write writes the image of root's tree to w: the pieces in order first,
// then every file not in them whole.
func Write(w io.Writer, root *File, order []Piece, o Options) error {
	l, err := layout(root, order)
	if err != nil {
		return err
	}
	return l.write(w, o)
}

type img struct {
	root    *File
	files   []*File // every file and directory, depth first
	data    []piece // the physical partition's contents in order
	metaLen uint32  // the metadata's blocks
	mirror  uint32  // the mirror's first block
	partLen uint32
	total   uint32 // sectors
	nextID  uint64
	open    map[*File]*reading
}

func layout(root *File, order []Piece) (*img, error) {
	l := &img{root: root}
	var walk func(f, parent *File)
	walk = func(f, parent *File) {
		f.parent = parent
		l.files = append(l.files, f)
		for _, c := range f.Children {
			walk(c, f)
		}
	}
	walk(root, root)
	// unique ids: the root's 0, 1 to 15 reserved
	l.nextID = 16
	for _, f := range l.files {
		f.exts = nil
		if f != root {
			f.id = l.nextID
			l.nextID++
		}
	}
	// the data: the pieces in order, then the files not in them
	block := uint32(metaStart) // placed after the metadata, below
	var data []piece
	placed := map[*File]int64{} // how much of each file is placed
	add := func(p Piece) error {
		if p.Len <= 0 {
			return nil
		}
		if placed[p.F] != p.Off {
			return fmt.Errorf("udf: %s's pieces out of order (%d placed, a piece at %d)", p.F.Name, placed[p.F], p.Off)
		}
		placed[p.F] += p.Len
		for p.Len > 0 { // a long_ad's extent is shorter than 1 GiB
			n := min(p.Len, maxExtent)
			data = append(data, piece{Piece{p.F, p.Off, n}, block})
			block += uint32((n + Sector - 1) / Sector) //nolint:gosec // an extent's blocks
			p.Off += n
			p.Len -= n
		}
		return nil
	}
	for _, p := range order {
		if err := add(p); err != nil {
			return nil, err
		}
	}
	for _, f := range l.files {
		if !f.Dir && f.Parts == nil && placed[f] < f.Size {
			if err := add(Piece{f, placed[f], f.Size - placed[f]}); err != nil {
				return nil, err
			}
		}
	}
	for _, f := range l.files {
		if !f.Dir && f.Parts == nil && placed[f] != f.Size {
			return nil, fmt.Errorf("udf: %s is %d bytes, its pieces %d", f.Name, f.Size, placed[f])
		}
	}
	// each file's extents: its own, or its parts' as placed
	for i := range data {
		data[i].F.exts = append(data[i].F.exts, data[i])
	}
	for _, f := range l.files {
		if f.Parts == nil {
			continue
		}
		f.Size = 0
		for _, p := range f.Parts {
			found := false
			for _, e := range p.F.exts {
				if e.Off >= p.Off && e.Off+e.Len <= p.Off+p.Len {
					f.exts = append(f.exts, piece{Piece{f, f.Size, e.Len}, e.block})
					f.Size += e.Len
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("udf: %s's part %s [%d, %d) is not a piece of the image", f.Name, p.F.Name, p.Off, p.Off+p.Len)
			}
		}
	}
	// extents that follow on (an SSIF's, its parts placed side by side)
	// as one, as a disc has its SSIF
	for _, f := range l.files {
		f.exts = merged(f.exts)
	}
	// the metadata partition: the file set descriptor and its
	// terminator, then each file's entry, a directory's identifiers, a
	// file's allocation extent descriptors
	mb := uint32(2)
	for _, f := range l.files {
		f.icb = mb
		mb++
		if f.Dir {
			f.fidLen = 0
			for _, c := range append([]*File{nil}, f.Children...) {
				f.fidLen += fidSize(c)
			}
			f.fids = mb
			mb += uint32((f.fidLen + Sector - 1) / Sector) //nolint:gosec // a directory's blocks
			continue
		}
		f.nExtra = 0
		if n := len(f.exts); n > adsInEntry {
			// the entry holds adsInEntry-1 and a continuation, each AED
			// adsInAED-1 and the next
			f.nExtra = (n - (adsInEntry - 1) + adsInAED - 2) / (adsInAED - 1)
			f.extra = mb
			mb += uint32(f.nExtra) //nolint:gosec // a few
		}
	}
	l.metaLen = roundUp(mb, alignment)
	// the physical partition: the metadata file's and its mirror's
	// entries, the metadata, the data, the mirror
	shift := l.metaLen
	for i := range data {
		data[i].block += shift
	}
	for _, f := range l.files {
		for i := range f.exts {
			f.exts[i].block += shift
		}
	}
	l.data = data
	l.mirror = roundUp(block+shift, alignment)
	l.partLen = l.mirror + l.metaLen
	// after the partition: the reserve descriptors, the last anchor
	l.total = partStart + l.partLen + 16 + 1
	return l, nil
}

func roundUp(v, unit uint32) uint32 { return (v + unit - 1) / unit * unit }

// merged is extents with those that follow on from the one before in
// whole blocks joined, up to a long_ad's longest.
func merged(exts []piece) []piece {
	var out []piece
	for _, e := range exts {
		if n := len(out); n > 0 {
			p := &out[n-1]
			if p.Len%Sector == 0 && p.block+uint32(p.Len/Sector) == e.block && p.Len+e.Len <= maxExtent { //nolint:gosec // an extent's blocks
				p.Len += e.Len
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// fidSize is the bytes of a directory's identifier of f (nil: its
// parent's).
func fidSize(f *File) int {
	n := 38
	if f != nil {
		n += 1 + len(f.Name)
	}
	return (n + 3) &^ 3
}

// sectorWriter writes the image a sector at a time, checking where.
type sectorWriter struct {
	w   *bufio.Writer
	at  uint32
	err error
}

func (s *sectorWriter) put(sector uint32, b []byte) {
	if s.err != nil {
		return
	}
	if sector < s.at {
		s.err = fmt.Errorf("udf: sector %d written after %d", sector, s.at)
		return
	}
	s.zeros(sector - s.at)
	if _, err := s.w.Write(b); err != nil {
		s.err = err
		return
	}
	n := (len(b) + Sector - 1) / Sector
	if pad := n*Sector - len(b); pad > 0 {
		_, s.err = s.w.Write(make([]byte, pad))
	}
	s.at = sector + uint32(n) //nolint:gosec // an image's sectors
}

func (s *sectorWriter) zeros(n uint32) {
	var z [Sector]byte
	for ; n > 0 && s.err == nil; n-- {
		_, s.err = s.w.Write(z[:])
	}
}

func (l *img) write(out io.Writer, o Options) error {
	l.open = map[*File]*reading{}
	defer l.close()
	s := &sectorWriter{w: bufio.NewWriterSize(out, 1<<20)}
	when := o.Time
	for _, v := range []string{"BEA01", "NSR03", "TEA01"} {
		b := make([]byte, Sector)
		b[0] = 0
		copy(b[1:], v)
		b[6] = 1
		s.put(vrsSector+uint32(slices.Index([]string{"BEA01", "NSR03", "TEA01"}, v)), b) //nolint:gosec // 0 to 2
	}
	vds := l.volumeDescriptors(o, mainVDS)
	for i, d := range vds {
		s.put(mainVDS+uint32(i), d) //nolint:gosec // six
	}
	s.put(lvidSector, l.lvid(when))
	s.put(lvidSector+1, terminator(lvidSector+1))
	s.put(anchorSector, anchor(anchorSector, l.reserve()))
	// the partition: the metadata file's entry and its mirror's
	s.put(partStart, l.metaFileEntry(0, fileTypeMeta, metaStart, when))
	s.put(partStart+1, l.metaFileEntry(1, fileTypeMirr, l.mirror, when))
	meta := l.metadata(when)
	s.put(partStart+metaStart, meta)
	for _, p := range l.data {
		if s.err != nil {
			break
		}
		if s.at > partStart+p.block {
			return fmt.Errorf("udf: the data overlaps at block %d", p.block)
		}
		s.zeros(partStart + p.block - s.at)
		if err := l.copyPiece(s, p); err != nil {
			return err
		}
	}
	s.put(partStart+l.mirror, meta)
	for i, d := range l.volumeDescriptors(o, l.reserve()) {
		s.put(l.reserve()+uint32(i), d) //nolint:gosec // six
	}
	s.put(l.total-1, anchor(l.total-1, l.reserve()))
	if s.err != nil {
		return s.err
	}
	return s.w.Flush()
}

// reserve is the reserve volume descriptors' first sector.
func (l *img) reserve() uint32 { return partStart + l.partLen }

// copyPiece writes a piece's bytes from its file's content, read on
// from where its last piece ended.
func (l *img) copyPiece(s *sectorWriter, p piece) error {
	o := l.open[p.F]
	if o == nil {
		if p.F.Content == nil {
			return fmt.Errorf("udf: %s has no content", p.F.Name)
		}
		r, err := p.F.Content()
		if err != nil {
			return err
		}
		o = &reading{r: r}
		l.open[p.F] = o
	}
	if o.at != p.Off {
		return fmt.Errorf("udf: %s read at %d, not %d", p.F.Name, p.Off, o.at)
	}
	if _, err := io.CopyN(s.w, o.r, p.Len); err != nil {
		return fmt.Errorf("udf: %s: %w", p.F.Name, err)
	}
	o.at += p.Len
	if pad := (Sector - p.Len%Sector) % Sector; pad > 0 {
		if _, err := s.w.Write(make([]byte, pad)); err != nil {
			return err
		}
	}
	s.at += uint32((p.Len + Sector - 1) / Sector) //nolint:gosec // a piece's sectors
	return nil
}

// reading is a file's content being read, and where.
type reading struct {
	r  io.Reader
	at int64
}

// close closes the contents read.
func (l *img) close() {
	for _, o := range l.open {
		if c, ok := o.r.(io.Closer); ok {
			_ = c.Close()
		}
	}
}

// --- descriptors ---------------------------------------------------------

// tag fills in the 16-byte descriptor tag at the start of d: its
// identifier, location and the CRC of the rest.
func tag(d []byte, id uint16, location uint32) []byte {
	binary.LittleEndian.PutUint16(d[0:], id)
	binary.LittleEndian.PutUint16(d[2:], 3) // descriptor version: NSR03
	binary.LittleEndian.PutUint16(d[6:], 1) // serial number
	crc := crcITU(d[16:])
	binary.LittleEndian.PutUint16(d[8:], crc)
	binary.LittleEndian.PutUint16(d[10:], uint16(len(d)-16)) //nolint:gosec // a descriptor's length
	binary.LittleEndian.PutUint32(d[12:], location)
	var sum byte
	for i := range 16 {
		if i != 4 {
			sum += d[i]
		}
	}
	d[4] = sum
	return d
}

// crcITU is the CRC-ITU-T (x^16 + x^12 + x^5 + 1) of b, UDF's.
func crcITU(b []byte) uint16 {
	var crc uint16
	for _, c := range b {
		crc ^= uint16(c) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// charspec is OSTA Compressed Unicode's character set specification.
func charspec(b []byte) {
	b[0] = 0
	copy(b[1:], "OSTA Compressed Unicode")
}

// dstring writes s into a fixed field b as UDF's d-string: 8-bit
// compressed unicode, its length in the last byte.
func dstring(b []byte, s string) {
	if s == "" {
		return
	}
	n := min(len(s), len(b)-2)
	b[0] = 8
	copy(b[1:], s[:n])
	b[len(b)-1] = byte(n + 1)
}

// regid writes an entity identifier; suffix its suffix bytes.
func regid(b []byte, id string, suffix ...byte) {
	copy(b[1:24], id)
	copy(b[24:32], suffix)
}

// udfSuffix is a domain or UDF identifier suffix: revision 2.50.
var udfSuffix = []byte{0x50, 0x02}

// timestamp writes a UDF timestamp, UTC.
func timestamp(b []byte, t time.Time) {
	t = t.UTC()
	binary.LittleEndian.PutUint16(b[0:], 1<<12)            // local time, offset 0
	binary.LittleEndian.PutUint16(b[2:], uint16(t.Year())) //nolint:gosec // a year
	b[4], b[5], b[6], b[7], b[8] = byte(t.Month()), byte(t.Day()), byte(t.Hour()), byte(t.Minute()), byte(t.Second())
	cs := t.Nanosecond() / 10_000_000
	b[9] = byte(cs)
}

func extentAD(b []byte, length, location uint32) {
	binary.LittleEndian.PutUint32(b[0:], length)
	binary.LittleEndian.PutUint32(b[4:], location)
}

func longAD(b []byte, length, location uint32, partition uint16) {
	binary.LittleEndian.PutUint32(b[0:], length)
	binary.LittleEndian.PutUint32(b[4:], location)
	binary.LittleEndian.PutUint16(b[8:], partition)
}

// volumeDescriptors is the volume descriptor sequence, from sector at:
// primary, implementation use (LV info), partition, logical volume,
// unallocated space, terminator.
func (l *img) volumeDescriptors(o Options, at uint32) [][]byte {
	label := o.Label
	pvd := make([]byte, 512)
	binary.LittleEndian.PutUint32(pvd[16:], 1) // VDS number
	dstring(pvd[24:56], label)
	binary.LittleEndian.PutUint16(pvd[56:], 1) // volume sequence number
	binary.LittleEndian.PutUint16(pvd[58:], 1)
	binary.LittleEndian.PutUint16(pvd[60:], 2) // interchange level
	binary.LittleEndian.PutUint16(pvd[62:], 3)
	binary.LittleEndian.PutUint32(pvd[64:], 1) // character set list
	binary.LittleEndian.PutUint32(pvd[68:], 1)
	dstring(pvd[72:200], fmt.Sprintf("%08X%s", uint32(o.Time.Unix()), label)) //nolint:gosec // a volume set id
	charspec(pvd[200:264])
	charspec(pvd[264:328])
	regid(pvd[344:376], "")
	timestamp(pvd[376:388], o.Time)
	regid(pvd[388:420], implementor)
	tag(pvd, 1, at)

	iuvd := make([]byte, 512)
	binary.LittleEndian.PutUint32(iuvd[16:], 2)
	regid(iuvd[20:52], "*UDF LV Info", udfSuffix...)
	charspec(iuvd[52:116])
	dstring(iuvd[116:244], label)
	regid(iuvd[352:384], implementor)
	tag(iuvd, 4, at+1)

	pd := make([]byte, 512)
	binary.LittleEndian.PutUint32(pd[16:], 3)
	binary.LittleEndian.PutUint16(pd[20:], 1) // allocated
	binary.LittleEndian.PutUint16(pd[22:], 0) // partition number
	regid(pd[24:56], "+NSR03")
	binary.LittleEndian.PutUint32(pd[184:], 1) // access: read only
	binary.LittleEndian.PutUint32(pd[188:], partStart)
	binary.LittleEndian.PutUint32(pd[192:], l.partLen)
	regid(pd[196:228], implementor)
	tag(pd, 5, at+2)

	lvd := make([]byte, 440+6+64)
	binary.LittleEndian.PutUint32(lvd[16:], 4)
	charspec(lvd[20:84])
	dstring(lvd[84:212], label)
	binary.LittleEndian.PutUint32(lvd[212:], Sector)
	regid(lvd[216:248], "*OSTA UDF Compliant", 0x50, 0x02, 0x03) // hard and soft write-protected
	longAD(lvd[248:264], Sector, 0, 1)                           // the file set descriptor
	binary.LittleEndian.PutUint32(lvd[264:], 6+64)               // map table length
	binary.LittleEndian.PutUint32(lvd[268:], 2)                  // partition maps
	regid(lvd[272:304], implementor)
	extentAD(lvd[432:440], 2*Sector, lvidSector)
	// a type 1 map, the physical partition
	m := lvd[440:]
	m[0], m[1] = 1, 6
	binary.LittleEndian.PutUint16(m[2:], 1)
	binary.LittleEndian.PutUint16(m[4:], 0)
	// a type 2 map, the metadata partition
	m = lvd[446:]
	m[0], m[1] = 2, 64
	regid(m[4:36], "*UDF Metadata Partition", udfSuffix...)
	binary.LittleEndian.PutUint16(m[36:], 1)
	binary.LittleEndian.PutUint16(m[38:], 0)
	binary.LittleEndian.PutUint32(m[40:], 0) // the metadata file's entry
	binary.LittleEndian.PutUint32(m[44:], 1) // the mirror's
	binary.LittleEndian.PutUint32(m[48:], 0xffffffff)
	binary.LittleEndian.PutUint32(m[52:], alignment)
	binary.LittleEndian.PutUint16(m[56:], alignment)
	tag(lvd, 6, at+3)

	usd := make([]byte, 24)
	binary.LittleEndian.PutUint32(usd[16:], 5)
	tag(usd, 7, at+4)

	return [][]byte{pvd, iuvd, pd, lvd, usd, terminator(at + 5)}
}

func terminator(at uint32) []byte { return tag(make([]byte, 512), 8, at) }

func anchor(at, reserve uint32) []byte {
	d := make([]byte, 512)
	extentAD(d[16:24], 16*Sector, mainVDS)
	extentAD(d[24:32], 16*Sector, reserve)
	return tag(d, 2, at)
}

// lvid is the logical volume integrity descriptor: closed.
func (l *img) lvid(when time.Time) []byte {
	d := make([]byte, 80+2*8+46)
	timestamp(d[16:28], when)
	binary.LittleEndian.PutUint32(d[28:], 1) // close
	binary.LittleEndian.PutUint64(d[40:], l.nextID)
	binary.LittleEndian.PutUint32(d[72:], 2) // partitions
	binary.LittleEndian.PutUint32(d[76:], 46)
	// free space, then sizes
	binary.LittleEndian.PutUint32(d[80:], 0)
	binary.LittleEndian.PutUint32(d[84:], 0)
	binary.LittleEndian.PutUint32(d[88:], l.partLen)
	binary.LittleEndian.PutUint32(d[92:], l.metaLen)
	iu := d[96:]
	regid(iu[0:32], implementor)
	var files, dirs uint32
	for _, f := range l.files {
		if f.Dir {
			dirs++
		} else {
			files++
		}
	}
	binary.LittleEndian.PutUint32(iu[32:], files)
	binary.LittleEndian.PutUint32(iu[36:], dirs)
	binary.LittleEndian.PutUint16(iu[40:], 0x0250)
	binary.LittleEndian.PutUint16(iu[42:], 0x0250)
	binary.LittleEndian.PutUint16(iu[44:], 0x0250)
	return tag(d, 9, lvidSector)
}

// entry is an extended file entry; ads its allocation descriptors.
func entry(location uint32, fileType byte, adType uint16, size, blocks, id uint64, perm uint32, links uint16,
	when time.Time, ads []byte) []byte {
	d := make([]byte, efeHeader+len(ads))
	icb := d[16:36]
	binary.LittleEndian.PutUint16(icb[4:], 4) // strategy 4
	binary.LittleEndian.PutUint16(icb[8:], 1) // maximum entries
	icb[11] = fileType
	binary.LittleEndian.PutUint16(icb[18:], adType)
	binary.LittleEndian.PutUint32(d[36:], 0xffffffff) // uid
	binary.LittleEndian.PutUint32(d[40:], 0xffffffff) // gid
	binary.LittleEndian.PutUint32(d[44:], perm)
	binary.LittleEndian.PutUint16(d[48:], links)
	binary.LittleEndian.PutUint64(d[56:], size)
	binary.LittleEndian.PutUint64(d[64:], size) // object size
	binary.LittleEndian.PutUint64(d[72:], blocks)
	for _, at := range []int{80, 92, 104, 116} {
		timestamp(d[at:at+12], when)
	}
	binary.LittleEndian.PutUint32(d[128:], 1) // checkpoint
	regid(d[168:200], implementor)
	binary.LittleEndian.PutUint64(d[200:], id)
	binary.LittleEndian.PutUint32(d[212:], uint32(len(ads))) //nolint:gosec // an entry's descriptors
	copy(d[efeHeader:], ads)
	return tag(d, 266, location)
}

// metaFileEntry is the entry of the metadata file (or its mirror): its
// extent, the metadata, at block start of the physical partition.
func (l *img) metaFileEntry(at uint32, fileType byte, start uint32, when time.Time) []byte {
	ad := make([]byte, 8)
	extentAD(ad, l.metaLen*Sector, start) // a short_ad
	return entry(at, fileType, 0, uint64(l.metaLen)*Sector, uint64(l.metaLen), 0, 0, 1, when, ad)
}

// metadata is the metadata partition's blocks.
func (l *img) metadata(when time.Time) []byte {
	m := make([]byte, int(l.metaLen)*Sector)
	put := func(block uint32, d []byte) { copy(m[int(block)*Sector:], d) }
	// the file set descriptor
	fsd := make([]byte, 512)
	timestamp(fsd[16:28], when)
	binary.LittleEndian.PutUint16(fsd[28:], 3)
	binary.LittleEndian.PutUint16(fsd[30:], 3)
	binary.LittleEndian.PutUint32(fsd[32:], 1)
	binary.LittleEndian.PutUint32(fsd[36:], 1)
	charspec(fsd[48:112])
	dstring(fsd[112:240], "")
	charspec(fsd[240:304])
	dstring(fsd[304:336], "")
	longAD(fsd[400:416], Sector, l.root.icb, 1)
	regid(fsd[416:448], "*OSTA UDF Compliant", 0x50, 0x02, 0x03)
	put(0, tag(fsd, 256, 0))
	put(1, terminator(1))
	for _, f := range l.files {
		if f.Dir {
			l.directory(f, when, put)
		} else {
			l.fileEntries(f, when, put)
		}
	}
	return m
}

func (l *img) directory(f *File, when time.Time, put func(uint32, []byte)) {
	ids := make([]byte, 0, f.fidLen)
	links := uint16(1)
	for _, c := range append([]*File{nil}, f.Children...) {
		at := f.fids + uint32(len(ids)/Sector) //nolint:gosec // within the directory
		d := make([]byte, fidSize(c))
		binary.LittleEndian.PutUint16(d[16:], 1) // file version
		target := f.parent
		if c != nil {
			target = c
			if c.Dir {
				d[18] = 0x02
				links++
			}
			d[19] = byte(1 + len(c.Name))
			d[38] = 8
			copy(d[39:], c.Name)
		} else {
			d[18] = 0x0a // a directory, the parent
		}
		longAD(d[20:36], Sector, target.icb, 1)
		ids = append(ids, tag(d, 257, at)...)
	}
	// the identifiers, which may cross blocks, by block
	for i := 0; i < len(ids); i += Sector {
		put(f.fids+uint32(i/Sector), ids[i:min(i+Sector, len(ids))]) //nolint:gosec // within the directory
	}
	ad := make([]byte, 8)
	extentAD(ad, uint32(len(ids)), f.fids) //nolint:gosec // a short_ad in the metadata partition
	blocks := uint64((len(ids) + Sector - 1) / Sector)
	put(f.icb, entry(f.icb, fileTypeDir, 0, uint64(len(ids)), blocks, f.id, permDir, links, when, ad))
}

func (l *img) fileEntries(f *File, when time.Time, put func(uint32, []byte)) {
	ads := make([][]byte, len(f.exts))
	var blocks uint64
	for i, e := range f.exts {
		ad := make([]byte, 16)
		longAD(ad, uint32(e.Len), e.block, 0) //nolint:gosec // under 1 GiB
		ads[i] = ad
		blocks += uint64((e.Len + Sector - 1) / Sector)
	}
	fileType := byte(fileTypeFile)
	if f.RealTime {
		fileType = fileTypeRT
	}
	cont := func(block uint32) []byte {
		ad := make([]byte, 16)
		longAD(ad, 3<<30|Sector, block, 1) // the next extent of descriptors
		return ad
	}
	first := ads
	if f.nExtra > 0 {
		first = append(append([][]byte(nil), ads[:adsInEntry-1]...), cont(f.extra))
		rest := ads[adsInEntry-1:]
		for k := range f.nExtra {
			n := min(len(rest), adsInAED-1)
			chunk := rest[:n]
			rest = rest[n:]
			if len(rest) > 0 {
				chunk = append(append([][]byte(nil), chunk...), cont(f.extra+uint32(k)+1)) //nolint:gosec // a few
			}
			body := slices.Concat(chunk...)
			aed := make([]byte, 24+len(body))
			binary.LittleEndian.PutUint32(aed[20:], uint32(len(body))) //nolint:gosec // a block's descriptors
			copy(aed[24:], body)
			put(f.extra+uint32(k), tag(aed, 258, f.extra+uint32(k))) //nolint:gosec // a few
		}
	}
	put(f.icb, entry(f.icb, fileType, 1, uint64(f.Size), blocks, f.id, permFile, 1, when, slices.Concat(first...))) //nolint:gosec // a size
}
