// Package mkv writes Matroska files: the muxer behind bdtools' default
// output, taking an encoder's raw H.264 or HEVC stream and the audio and
// subtitle tracks a Blu-ray demux produces.
package mkv

import (
	"encoding/binary"
	"math"
)

// Element IDs used here, as their full encoded bytes.
const (
	idEBML               = 0x1A45DFA3
	idEBMLVersion        = 0x4286
	idEBMLReadVersion    = 0x42F7
	idEBMLMaxIDLength    = 0x42F2
	idEBMLMaxSizeLength  = 0x42F3
	idDocType            = 0x4282
	idDocTypeVersion     = 0x4287
	idDocTypeReadVersion = 0x4285

	idSegment     = 0x18538067
	idSeekHead    = 0x114D9B74
	idSeek        = 0x4DBB
	idSeekID      = 0x53AB
	idSeekPos     = 0x53AC
	idVoid        = 0xEC
	idInfo        = 0x1549A966
	idTimestampSc = 0x2AD7B1
	idDuration    = 0x4489
	idMuxingApp   = 0x4D80
	idWritingApp  = 0x5741
	idSegmentUID  = 0x73A4

	idTracks          = 0x1654AE6B
	idTrackEntry      = 0xAE
	idTrackNumber     = 0xD7
	idTrackUID        = 0x73C5
	idTrackType       = 0x83
	idFlagDefault     = 0x88
	idFlagLacing      = 0x9C
	idDefaultDuration = 0x23E383
	idName            = 0x536E
	idLanguage        = 0x22B59C
	idCodecID         = 0x86
	idCodecPrivate    = 0x63A2
	idVideo           = 0xE0
	idPixelWidth      = 0xB0
	idPixelHeight     = 0xBA
	idStereoMode      = 0x53B8
	idDisplayWidth    = 0x54B0
	idDisplayHeight   = 0x54BA
	idColour          = 0x55B0
	idMatrix          = 0x55B1
	idBitsPerChannel  = 0x55B2
	idRange           = 0x55B9
	idTransfer        = 0x55BA
	idPrimaries       = 0x55BB
	idMaxCLL          = 0x55BC
	idMaxFALL         = 0x55BD
	idMastering       = 0x55D0
	idAudio           = 0xE1
	idSamplingFreq    = 0xB5
	idChannels        = 0x9F
	idBitDepth        = 0x6264

	idCluster     = 0x1F43B675
	idTimestamp   = 0xE7
	idSimpleBlock = 0xA3

	idCues               = 0x1C53BB6B
	idCuePoint           = 0xBB
	idCueTime            = 0xB3
	idCueTrackPositions  = 0xB7
	idCueTrack           = 0xF7
	idCueClusterPosition = 0xF1

	idChapters         = 0x1043A770
	idEditionEntry     = 0x45B9
	idEditionUID       = 0x45BC
	idChapterAtom      = 0xB6
	idChapterUID       = 0x73C4
	idChapterTimeStart = 0x91
	idChapterDisplay   = 0x80
	idChapString       = 0x85
	idChapLanguage     = 0x437C
)

// putID appends an element ID (its bytes are its own length marker).
func putID(b []byte, id uint32) []byte {
	switch {
	case id >= 1<<24:
		return append(b, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	case id >= 1<<16:
		return append(b, byte(id>>16), byte(id>>8), byte(id))
	case id >= 1<<8:
		return append(b, byte(id>>8), byte(id))
	}
	return append(b, byte(id))
}

// putSize appends a data size in the shortest variable-length form.
func putSize(b []byte, n uint64) []byte {
	l := 1
	for l < 8 && n >= 1<<(7*l)-1 {
		l++
	}
	return putSizeLen(b, n, l)
}

// putSizeLen appends a data size in exactly l bytes.
func putSizeLen(b []byte, n uint64, l int) []byte {
	v := n | 1<<(7*l)
	for i := l - 1; i >= 0; i-- {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}

// elem appends a whole element.
func elem(b []byte, id uint32, data []byte) []byte {
	b = putID(b, id)
	b = putSize(b, uint64(len(data)))
	return append(b, data...)
}

func elemUint(b []byte, id uint32, v uint64) []byte {
	n := 1
	for n < 8 && v>>(8*n) != 0 {
		n++
	}
	var d [8]byte
	binary.BigEndian.PutUint64(d[:], v)
	return elem(b, id, d[8-n:])
}

func elemFloat(b []byte, id uint32, v float64) []byte {
	var d [8]byte
	binary.BigEndian.PutUint64(d[:], math.Float64bits(v))
	return elem(b, id, d[:])
}

func elemString(b []byte, id uint32, s string) []byte { return elem(b, id, []byte(s)) }
