package mp4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Live writes one H.264 video stream as a fragmented MP4 as its frames
// are made, for a browser's media source (MSE) reading it live: the
// movie header once (Init), then a fragment a frame (moof and mdat),
// each decoded as it arrives (no B-frames). The frames are Annex B
// access units; their parameter sets go into the avcC, not the samples.
type Live struct {
	W io.Writer

	seq   uint32
	start int64 // the first frame's time
	n     int
}

// LiveTimescale is the stream's timescale: the 90 kHz of its timestamps.
const LiveTimescale = 90000

// Init writes the file's header for a picture w x h whose parameter sets
// are sps and pps (NAL units, without start codes).
func (l *Live) Init(sps, pps []byte, w, h int) error {
	if len(sps) < 4 || len(pps) == 0 {
		return errors.New("mp4: a live stream needs its SPS and PPS")
	}
	avcC := bytes.Join([][]byte{
		{1, sps[1], sps[2], sps[3], 0xff, 0xe1}, u16(len(sps)), sps, {1}, u16(len(pps)), pps,
	}, nil)
	entry := bytes.Join([][]byte{
		make([]byte, 6), u16(1),
		u16(0), u16(0), make([]byte, 12),
		u16(w), u16(h),
		u32(0x00480000), u32(0x00480000), u32(0), u16(1),
		make([]byte, 32),
		u16(0x0018), u16(0xffff),
		box("avcC", avcC),
		box("pasp", u32(1), u32(1)),
	}, nil)
	stsd := fullBox("stsd", 0, 0, u32(1), box("avc1", entry))
	stbl := box("stbl", stsd,
		fullBox("stts", 0, 0, u32(0)), fullBox("stsc", 0, 0, u32(0)),
		fullBox("stsz", 0, 0, u32(0), u32(0)), fullBox("stco", 0, 0, u32(0)))
	dinf := box("dinf", fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1)))
	minf := box("minf", fullBox("vmhd", 0, 1, make([]byte, 8)), dinf, stbl)
	hdlr := fullBox("hdlr", 0, 0, u32(0), []byte("vide"), make([]byte, 12), []byte("bdtools\x00"))
	mdhd := fullBox("mdhd", 0, 0, u32(0), u32(0), u32(LiveTimescale), u32(0), u16(0x55c4), u16(0))
	mdia := box("mdia", mdhd, hdlr, minf)
	tkhd := fullBox("tkhd", 0, 3, u32(0), u32(0), u32(1), u32(0), u32(0),
		make([]byte, 8), u16(0), u16(0), u16(0), u16(0), identityMatrix(), u32(int64(w)<<16), u32(int64(h)<<16))
	trak := box("trak", tkhd, mdia)
	mvhd := fullBox("mvhd", 0, 0, u32(0), u32(0), u32(LiveTimescale), u32(0), u32(0x00010000), u16(0x0100), make([]byte, 10),
		identityMatrix(), make([]byte, 24), u32(2))
	mvex := box("mvex", fullBox("trex", 0, 0, u32(1), u32(1), u32(0), u32(0), u32(0)))
	moov := box("moov", mvhd, trak, mvex)
	ftyp := box("ftyp", []byte("iso5"), u32(0x200), []byte("iso5iso6avc1mp41"))
	_, err := l.W.Write(append(ftyp, moov...))
	return err
}

// Frame writes an access unit (Annex B) shown, and decoded, at pts (90
// kHz), key when it is a keyframe. Its parameter sets are left out (the
// header carries them).
func (l *Live) Frame(au []byte, pts int64, key bool) error {
	var sample []byte
	for _, nal := range SplitAnnexB(au) {
		if t := nal[0] & 0x1f; t == 7 || t == 8 {
			continue
		}
		sample = binary.BigEndian.AppendUint32(sample, uint32(len(nal))) //nolint:gosec // a NAL unit length
		sample = append(sample, nal...)
	}
	if l.n == 0 {
		l.start = pts
	}
	l.n++
	l.seq++
	flags := int64(0x01010000) // depends on others, not a sync sample
	if key {
		flags = 0x02000000
	}
	const trunFlags = 0x001 | 0x100 | 0x200 | 0x400 // data offset, duration, size, flags
	dur := int64(LiveTimescale * 1001 / 24000)
	trun := func(offset int) []byte {
		return fullBox("trun", 0, trunFlags, u32(1), u32(int64(offset)), u32(dur), u32(int64(len(sample))), u32(flags))
	}
	tfhd := fullBox("tfhd", 0, 0x020000, u32(1)) // the base offset is the moof's
	tfdt := fullBox("tfdt", 1, 0, u64(pts-l.start))
	traf := box("traf", tfhd, tfdt, trun(0))
	mfhd := fullBox("mfhd", 0, 0, u32(int64(l.seq)))
	moof := box("moof", mfhd, traf)
	// the data offset is from the moof's start to the sample: the moof
	// and the mdat's header
	traf = box("traf", tfhd, tfdt, trun(len(moof)+8))
	moof = box("moof", mfhd, traf)
	_, err := l.W.Write(append(moof, box("mdat", sample)...))
	return err
}

// SplitAnnexB is the NAL units of an Annex B access unit, without their
// start codes.
func SplitAnnexB(au []byte) [][]byte {
	var out [][]byte
	i := 0
	for i+3 <= len(au) {
		if au[i] != 0 || au[i+1] != 0 || au[i+2] != 1 {
			i++
			continue
		}
		i += 3
		end := len(au)
		for j := i; j+2 < len(au); j++ {
			if au[j] == 0 && au[j+1] == 0 && (au[j+2] == 1 || au[j+2] == 0 && j+3 < len(au) && au[j+3] == 1) {
				end = j
				break
			}
		}
		if end > i {
			out = append(out, au[i:end])
		}
		i = end
	}
	return out
}

// AVCCodec is the codec string a browser wants for an H.264 stream
// (avc1.PPCCLL: the SPS's profile, constraints and level).
func AVCCodec(sps []byte) string {
	if len(sps) < 4 {
		return "avc1"
	}
	return fmt.Sprintf("avc1.%02X%02X%02X", sps[1], sps[2], sps[3])
}
