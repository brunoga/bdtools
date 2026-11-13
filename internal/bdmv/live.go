package bdmv

import (
	"io"

	"github.com/brunoga/bdtools/m2ts"
)

// Live writes one video stream as a transport stream as its frames are
// made, for a player reading it live (over HTTP, say): plain 188-byte
// packets, the PAT and PMT ahead of every keyframe and at least every
// LivePSIFrames frames, a PCR before every frame. The frames come in
// display order and are decoded in it (no B-frames). The parameter sets
// of the last keyframe that carried them go ahead of any keyframe that
// does not, so a player joining late starts at the next keyframe.
type Live struct {
	W      io.Writer
	Coding byte // CodingAVC or CodingHEVC
	// FromKey drops what comes before the first keyframe: a player that
	// joins a stream under way starts there.
	FromKey bool
	// Params are the parameter sets to put ahead of a keyframe that
	// carries none, with their start codes: the last ones seen (Frame
	// keeps them), or a stream's first for a writer that joins it late.
	Params []byte

	cc      [4]byte // PAT, PMT, PCR and video continuity counters
	since   int     // frames since the last PAT and PMT
	n       int     // frames written
	started bool
}

const (
	// LiveVideoPID is the video's PID in a Live stream.
	LiveVideoPID = 0x1011
	// LivePSIFrames is how often, in frames, the PAT and PMT go out
	// between keyframes.
	LivePSIFrames = 24
	// livePreload is how far ahead of its PCR a frame's presentation is,
	// 27 MHz: what a player buffers before it starts.
	livePreload = 27_000_000 / 2
)

// Frame writes an access unit (Annex B) shown, and decoded, at pts (90
// kHz). It reports whether it was written (FromKey drops frames).
func (l *Live) Frame(au []byte, pts int64) (bool, error) {
	key, params := AnnexBKey(l.Coding, au)
	switch {
	case params != nil:
		l.Params = params
	case key && l.Params != nil:
		au = append(append([]byte(nil), l.Params...), au...)
	}
	if !l.started {
		if l.FromKey && !key {
			return false, nil
		}
		l.started = true
	}
	var out []byte
	if key || l.since >= LivePSIFrames || l.n == 0 {
		prog := &m2ts.Program{PCRPID: pcrPID, Streams: []m2ts.ProgramStream{{Type: l.Coding, PID: LiveVideoPID}}}
		out = append(out, sectionPacket(0, &l.cc[0], m2ts.Section(0, 0, 0, m2ts.PATBody(1, pmtPID)))...)
		out = append(out, sectionPacket(pmtPID, &l.cc[1], m2ts.Section(2, 1, 0, m2ts.PMTBody(prog)))...)
		l.since = 0
	}
	out = append(out, pcrPacket(&l.cc[2], max(pts*300-livePreload, 0))...)
	for _, p := range pesPackets(LiveVideoPID, &l.cc[3], 0xe0, MuxFrame{PTS: pts, DTS: pts, Data: au, Key: key}) {
		out = append(out, p...)
	}
	l.since++
	l.n++
	_, err := l.W.Write(out)
	return true, err
}

// AnnexBKey reports whether an Annex B access unit of a coding (CodingAVC
// or CodingHEVC) is a keyframe (an IDR or, for HEVC, any IRAP picture),
// and gives its parameter sets (SPS and PPS, HEVC's VPS too), with their
// start codes, when it carries any.
func AnnexBKey(coding byte, au []byte) (key bool, params []byte) {
	hevc := coding == CodingHEVC
	for i := 0; i+3 < len(au); {
		if au[i] != 0 || au[i+1] != 0 || au[i+2] != 1 {
			i++
			continue
		}
		start := i // the start code (a zero before it belongs to it too)
		if start > 0 && au[start-1] == 0 {
			start--
		}
		i += 3
		if i >= len(au) {
			break
		}
		end := len(au)
		for j := i; j+2 < len(au); j++ {
			if au[j] == 0 && au[j+1] == 0 && (au[j+2] == 1 || au[j+2] == 0 && j+3 < len(au) && au[j+3] == 1) {
				end = j
				break
			}
		}
		var t int
		if hevc {
			t = int(au[i]>>1) & 0x3f
		} else {
			t = int(au[i]) & 0x1f
		}
		switch {
		case hevc && t >= 32 && t <= 34, !hevc && (t == 7 || t == 8):
			params = append(params, au[start:end]...)
		case hevc && t >= 16 && t <= 21, !hevc && t == 5:
			key = true
		}
		i = end
	}
	return key, params
}
