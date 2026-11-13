package udf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

// Reading a UDF image (a Blu-ray's, UDF 2.50 with a metadata partition;
// plain UDF too): the anchor at sector 256 names the volume descriptor
// sequence, which names the partition and the logical volume with its
// partition maps; a type 2 map is the metadata partition, whose blocks
// are the extents of the metadata file in the physical partition; the
// file set descriptor names the root directory; an entry's allocation
// descriptors (short or long, continued in allocation extent descriptors
// as often as needed) are a file's extents or a directory's identifiers.
// An image may be partial: a read beyond what the reader has is an
// error, never a panic.

// Image is an image open for reading.
type Image struct {
	r      io.ReaderAt
	parts  []partition // by partition reference number (the logical volume's maps)
	root   *Entry
	fsdLoc longRef
}

// partition is a map of the logical volume: physical, or the metadata
// partition laid over the physical one by its file's extents.
type partition struct {
	start uint32   // the physical partition's first sector
	meta  []extent // the metadata file's extents in the physical partition; nil for the physical partition
	phys  *partition
}

// extent is a stretch of a file: len bytes at a block of a partition (or,
// resolved, at a sector of the image), recorded or not (a hole, zeros).
type extent struct {
	block    uint32
	part     uint16
	len      uint32
	recorded bool
}

type longRef struct {
	length   uint32
	location uint32
	part     uint16
}

// Entry is a file or directory of the image.
type Entry struct {
	name     string
	dir      bool
	size     int64
	icb      longRef
	img      *Image
	exts     []extent // resolved to sectors, once read
	embedded []byte   // a small file's data, in its entry
	known    bool
}

// Open reads an image's structure from r: size bytes of it.
func Open(r io.ReaderAt, size int64) (*Image, error) {
	img := &Image{r: r}
	anchor, err := img.block(anchorSector)
	if err != nil {
		return nil, fmt.Errorf("udf: the anchor: %w", err)
	}
	if err := checkTag(anchor, 2, anchorSector); err != nil {
		return nil, fmt.Errorf("udf: the anchor at sector %d: %w", anchorSector, err)
	}
	vdsLen, vdsAt := binary.LittleEndian.Uint32(anchor[16:]), binary.LittleEndian.Uint32(anchor[20:])
	if err := img.volumeDescriptors(vdsAt, vdsLen); err != nil {
		return nil, err
	}
	fsd, err := img.readAt(img.fsdLoc.part, img.fsdLoc.location, Sector)
	if err != nil {
		return nil, fmt.Errorf("udf: the file set descriptor: %w", err)
	}
	if err := checkTag(fsd, 256, img.fsdLoc.location); err != nil {
		return nil, fmt.Errorf("udf: the file set descriptor: %w", err)
	}
	img.root = &Entry{name: "", dir: true, icb: readLongRef(fsd[400:416]), img: img}
	return img, nil
}

// Root is the image's root directory.
func (img *Image) Root() *Entry { return img.root }

// volumeDescriptors reads the sequence at sector at: the partition
// descriptor and the logical volume descriptor.
func (img *Image) volumeDescriptors(at, length uint32) error {
	var pdStart map[uint16]uint32 // by partition number
	var lvd []byte
	n := length / Sector
	if n == 0 || n > 64 {
		n = 16
	}
sequence:
	for i := range n {
		d, err := img.block(at + i)
		if err != nil {
			return fmt.Errorf("udf: the volume descriptors: %w", err)
		}
		id := binary.LittleEndian.Uint16(d)
		if checkTag(d, id, at+i) != nil {
			continue
		}
		switch id {
		case 5: // a partition descriptor
			if pdStart == nil {
				pdStart = map[uint16]uint32{}
			}
			pdStart[binary.LittleEndian.Uint16(d[22:])] = binary.LittleEndian.Uint32(d[188:])
		case 6: // the logical volume descriptor
			lvd = d
		case 8: // the terminator
			break sequence
		}
	}
	if lvd == nil || pdStart == nil {
		return errors.New("udf: no logical volume or partition descriptor")
	}
	if bs := binary.LittleEndian.Uint32(lvd[212:]); bs != Sector {
		return fmt.Errorf("udf: a block size of %d", bs)
	}
	img.fsdLoc = readLongRef(lvd[248:264])
	maps := binary.LittleEndian.Uint32(lvd[268:])
	m := lvd[440:]
	for range maps {
		if len(m) < 2 {
			return errors.New("udf: a partition map runs off its descriptor")
		}
		kind, size := m[0], int(m[1])
		if size < 2 || size > len(m) {
			return errors.New("udf: a partition map's size")
		}
		switch kind {
		case 1:
			num := binary.LittleEndian.Uint16(m[4:])
			start, ok := pdStart[num]
			if !ok {
				return fmt.Errorf("udf: partition %d is not described", num)
			}
			img.parts = append(img.parts, partition{start: start})
		case 2:
			ident := strings.TrimRight(string(m[5:28]), "\x00")
			num := binary.LittleEndian.Uint16(m[38:])
			start, ok := pdStart[num]
			if !ok {
				return fmt.Errorf("udf: partition %d is not described", num)
			}
			if !strings.HasPrefix(ident, "*UDF Metadata Partition") {
				return fmt.Errorf("udf: a partition map of kind %q", ident)
			}
			phys := &partition{start: start}
			// the metadata file (its mirror if the file cannot be read)
			var exts []extent
			var err error
			for _, loc := range []uint32{binary.LittleEndian.Uint32(m[40:]), binary.LittleEndian.Uint32(m[44:])} {
				if loc == 0xffffffff {
					continue
				}
				if exts, err = img.fileExtents(phys, loc); err == nil && len(exts) > 0 {
					break
				}
			}
			if len(exts) == 0 {
				return fmt.Errorf("udf: the metadata file: %w", err)
			}
			img.parts = append(img.parts, partition{start: start, meta: exts, phys: phys})
		default:
			return fmt.Errorf("udf: a partition map of type %d", kind)
		}
		m = m[size:]
	}
	if int(img.fsdLoc.part) >= len(img.parts) {
		return errors.New("udf: the file set descriptor's partition")
	}
	return nil
}

// fileExtents is the extents (in the image's sectors) of the file whose
// entry is at block loc of partition p.
func (img *Image) fileExtents(p *partition, loc uint32) ([]extent, error) {
	e, err := img.readAt(p.ref(img), loc, Sector)
	if err != nil {
		return nil, err
	}
	_, _, ads, err := parseEntry(e, loc)
	if err != nil {
		return nil, err
	}
	return img.resolve(ads, p.ref(img))
}

// ref is a partition's reference number: its index among the maps (the
// physical partition under a metadata one is the one before it).
func (p *partition) ref(img *Image) uint16 {
	for i := range img.parts {
		if &img.parts[i] == p || img.parts[i].meta == nil && img.parts[i].start == p.start {
			return uint16(i) //nolint:gosec // a few maps
		}
	}
	return 0
}

// sector is where block of partition part lies in the image.
func (img *Image) sector(part uint16, block uint32) (uint32, error) {
	if int(part) >= len(img.parts) {
		return 0, fmt.Errorf("udf: partition %d", part)
	}
	p := &img.parts[part]
	if p.meta == nil {
		return p.start + block, nil
	}
	// the metadata partition: its block through the metadata file's extents
	off := uint64(block) * Sector
	for _, e := range p.meta {
		if off < uint64(e.len) {
			if !e.recorded {
				return 0, errors.New("udf: a metadata block not recorded")
			}
			return e.block + uint32(off/Sector), nil //nolint:gosec // within the extent
		}
		off -= uint64(e.len)
	}
	return 0, fmt.Errorf("udf: metadata block %d is past the metadata file", block)
}

// block reads a sector of the image.
func (img *Image) block(sector uint32) ([]byte, error) {
	b := make([]byte, Sector)
	if _, err := img.r.ReadAt(b, int64(sector)*Sector); err != nil {
		return nil, err
	}
	return b, nil
}

// readAt reads n bytes from a block of a partition (n within the block).
func (img *Image) readAt(part uint16, block uint32, n int) ([]byte, error) {
	s, err := img.sector(part, block)
	if err != nil {
		return nil, err
	}
	b := make([]byte, n)
	if _, err := img.r.ReadAt(b, int64(s)*Sector); err != nil {
		return nil, err
	}
	return b, nil
}

// parseEntry reads a file entry (tag 261) or an extended one (266) at
// block loc: its file type, size and allocation descriptors (in the
// entry's own partition), the descriptors' kind (0 short, 1 long, 3 the
// data in the entry itself).
func parseEntry(e []byte, loc uint32) (fileType byte, size uint64, ads adList, err error) {
	id := binary.LittleEndian.Uint16(e)
	if err := checkTag(e, id, loc); err != nil {
		return 0, 0, adList{}, err
	}
	var lea, lad, hdr int
	switch id {
	case 261:
		hdr = 176
		lea, lad = int(binary.LittleEndian.Uint32(e[168:])), int(binary.LittleEndian.Uint32(e[172:]))
	case 266:
		hdr = efeHeader
		lea, lad = int(binary.LittleEndian.Uint32(e[208:])), int(binary.LittleEndian.Uint32(e[212:]))
	default:
		return 0, 0, adList{}, fmt.Errorf("udf: a descriptor of tag %d where a file entry should be", id)
	}
	fileType = e[16+11]
	kind := binary.LittleEndian.Uint16(e[16+18:]) & 7
	size = binary.LittleEndian.Uint64(e[56:])
	if hdr+lea+lad > len(e) || lea < 0 || lad < 0 {
		return 0, 0, adList{}, errors.New("udf: a file entry's descriptors run off the block")
	}
	return fileType, size, adList{kind: kind, data: e[hdr+lea : hdr+lea+lad]}, nil
}

// adList is an entry's allocation descriptors as read.
type adList struct {
	kind uint16
	data []byte
}

// resolve turns allocation descriptors of an entry in partition part
// into extents in the image's sectors, following the chain of allocation
// extent descriptors as far as it goes. Embedded data (kind 3) is one
// extent of its own: the caller reads it from the entry.
func (img *Image) resolve(ads adList, part uint16) ([]extent, error) {
	var out []extent
	const most = 1 << 20 // extents: a 50 GB file in 6 KB pieces is far fewer
	data := ads.data
	for hops := 0; ; hops++ {
		if hops > 1<<16 {
			return nil, errors.New("udf: an allocation descriptor chain without end")
		}
		var next *longRef
		switch ads.kind {
		case 0: // short_ad, in the entry's partition
			for len(data) >= 8 {
				l, pos := binary.LittleEndian.Uint32(data), binary.LittleEndian.Uint32(data[4:])
				data = data[8:]
				if l&(1<<30-1) == 0 {
					continue
				}
				if l>>30 == 3 {
					next = &longRef{length: l & (1<<30 - 1), location: pos, part: part}
					break
				}
				s, err := img.sector(part, pos)
				if err != nil {
					return nil, err
				}
				out = append(out, extent{block: s, part: part, len: l & (1<<30 - 1), recorded: l>>30 == 0})
			}
		case 1: // long_ad
			for len(data) >= 16 {
				l := binary.LittleEndian.Uint32(data)
				ad := readLongRef(data)
				data = data[16:]
				if l&(1<<30-1) == 0 {
					continue
				}
				if l>>30 == 3 {
					next = &ad
					break
				}
				s, err := img.sector(ad.part, ad.location)
				if err != nil {
					return nil, err
				}
				out = append(out, extent{block: s, part: ad.part, len: ad.length, recorded: l>>30 == 0})
			}
		case 3: // the data in the entry: nothing to resolve
			return nil, nil
		default:
			return nil, fmt.Errorf("udf: allocation descriptors of kind %d", ads.kind)
		}
		if len(out) > most {
			return nil, errors.New("udf: too many extents")
		}
		if next == nil {
			return out, nil
		}
		// an allocation extent descriptor: tag 258, L_AD at 20, the
		// descriptors from 24, of the same kind
		aed, err := img.readAt(next.part, next.location, Sector)
		if err != nil {
			return nil, fmt.Errorf("udf: an allocation extent descriptor: %w", err)
		}
		if err := checkTag(aed, 258, next.location); err != nil {
			return nil, fmt.Errorf("udf: an allocation extent descriptor: %w", err)
		}
		lad := int(binary.LittleEndian.Uint32(aed[20:]))
		if lad < 0 || 24+lad > len(aed) {
			return nil, errors.New("udf: an allocation extent descriptor's length")
		}
		data, part = aed[24:24+lad], next.part
	}
}

// load reads an entry's file entry: its type, size and extents.
func (e *Entry) load() error {
	if e.known {
		return nil
	}
	b, err := e.img.readAt(e.icb.part, e.icb.location, Sector)
	if err != nil {
		return err
	}
	fileType, size, ads, err := parseEntry(b, e.icb.location)
	if err != nil {
		return err
	}
	e.dir = fileType == fileTypeDir
	e.size = int64(size) //nolint:gosec // a file's size
	if ads.kind == 3 {
		// the data in the entry itself
		e.exts = nil
		e.embedded = append([]byte(nil), ads.data...)
	} else if e.exts, err = e.img.resolve(ads, e.icb.part); err != nil {
		return err
	}
	e.known = true
	return nil
}

// Name is the entry's name; IsDir says it is a directory.
func (e *Entry) Name() string { return e.name }
func (e *Entry) IsDir() bool  { return e.dir }

// Size is a file's length in bytes.
func (e *Entry) Size() (int64, error) {
	if err := e.load(); err != nil {
		return 0, err
	}
	return e.size, nil
}

// ReadDir is a directory's entries, in the directory's order, the parent
// and deleted ones left out.
func (e *Entry) ReadDir() ([]*Entry, error) {
	if err := e.load(); err != nil {
		return nil, err
	}
	if !e.dir {
		return nil, fmt.Errorf("udf: %s is not a directory", e.name)
	}
	r, err := e.Reader()
	if err != nil {
		return nil, err
	}
	data := make([]byte, r.Size())
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("udf: the directory %s: %w", e.name, err)
	}
	var out []*Entry
	for len(data) >= 38 {
		if err := checkTag(data, 257, 0); err != nil && !errors.Is(err, errTagLocation) {
			return nil, fmt.Errorf("udf: a file identifier in %s: %w", e.name, err)
		}
		chars, lfi := data[18], int(data[19])
		icb := readLongRef(data[20:36])
		liu := int(binary.LittleEndian.Uint16(data[36:]))
		n := 38 + liu + lfi
		n = (n + 3) &^ 3
		if n > len(data) {
			return nil, fmt.Errorf("udf: a file identifier in %s runs off the directory", e.name)
		}
		name := decodeName(data[38+liu : 38+liu+lfi])
		data = data[n:]
		if chars&0x08 != 0 || chars&0x04 != 0 || name == "" {
			continue // the parent, or deleted
		}
		out = append(out, &Entry{name: name, dir: chars&0x02 != 0, icb: icb, img: e.img})
	}
	return out, nil
}

// Reader reads a file's bytes: its extents in order, holes as zeros.
func (e *Entry) Reader() (*io.SectionReader, error) {
	if err := e.load(); err != nil {
		return nil, err
	}
	if e.embedded != nil {
		return io.NewSectionReader(bytesAt(e.embedded), 0, int64(len(e.embedded))), nil
	}
	return io.NewSectionReader(&extentReader{img: e.img, exts: e.exts}, 0, e.size), nil
}

type bytesAt []byte

func (b bytesAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// extentReader reads across a file's extents.
type extentReader struct {
	img  *Image
	exts []extent
}

func (r *extentReader) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for _, e := range r.exts {
		if len(p) == 0 {
			break
		}
		if off >= int64(e.len) {
			off -= int64(e.len)
			continue
		}
		take := min(int64(len(p)), int64(e.len)-off)
		if e.recorded {
			m, err := r.img.r.ReadAt(p[:take], int64(e.block)*Sector+off)
			n += m
			if err != nil {
				return n, err
			}
		} else {
			clear(p[:take])
			n += int(take)
		}
		p = p[take:]
		off = 0
	}
	if len(p) > 0 {
		return n, io.EOF
	}
	return n, nil
}

// decodeName is a file identifier's name: compressed unicode, 8- or
// 16-bit.
func decodeName(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	switch b[0] {
	case 8:
		return string(b[1:])
	case 16:
		u := make([]uint16, 0, len(b)/2)
		for i := 1; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
		return string(utf16.Decode(u))
	}
	return ""
}

func readLongRef(b []byte) longRef {
	return longRef{length: binary.LittleEndian.Uint32(b) & (1<<30 - 1), location: binary.LittleEndian.Uint32(b[4:]), part: binary.LittleEndian.Uint16(b[8:])}
}

var errTagLocation = errors.New("udf: a descriptor's location is not where it was read")

// checkTag checks a descriptor's tag: its identifier, checksum and, when
// loc is not 0, its location.
func checkTag(d []byte, id uint16, loc uint32) error {
	if len(d) < 16 {
		return errors.New("udf: a descriptor too short for its tag")
	}
	if got := binary.LittleEndian.Uint16(d); got != id {
		return fmt.Errorf("udf: a descriptor of tag %d, not %d", got, id)
	}
	var sum byte
	for i := range 16 {
		if i != 4 {
			sum += d[i]
		}
	}
	if sum != d[4] {
		return errors.New("udf: a descriptor's tag checksum")
	}
	if loc != 0 && binary.LittleEndian.Uint32(d[12:]) != loc {
		return errTagLocation
	}
	return nil
}
