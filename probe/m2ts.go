package probe

import (
	"fmt"
	"io"

	"github.com/brunoga/bdtools/m2ts"
)

// M2TS probes a bare transport stream (.m2ts, .mts, .ts: a Blu-ray clip
// or an MVC remux on its own, 192- or 188-byte packets) from its start:
// the PAT, then the PMT, which repeat every few hundred milliseconds, so
// the first piece of a download is enough. The streams are the PMT's,
// named as a disc's; a dependent view (stream type 0x20) makes it 3D in
// MVC, a plain AVC, HEVC or VC-1 stream 2D. The duration is not read.
// size is the file's full length. The caller owns r.
//
// When the probe fails because a read of r failed, the error is a
// *MissingDataError naming the bytes that were wanted, as Image's is.
func M2TS(r io.ReaderAt, size int64, label string) (res *Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			res, err = nil, fmt.Errorf("probe: %s: malformed transport stream (%v)", label, p)
		}
	}()
	rr := &watchReader{r: r}
	res, err = probeM2TS(rr, size, label)
	if err != nil && rr.failed != nil {
		return nil, &MissingDataError{Offset: rr.off, Length: rr.n, Err: rr.failed, Probe: err}
	}
	return res, err
}

func probeM2TS(r io.ReaderAt, size int64, label string) (*Result, error) {
	m := m2ts.NewReader(io.NewSectionReader(r, 0, size))
	prog, err := m.ReadProgram()
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("probe: %s: no program table before the stream ends", label)
		}
		return nil, fmt.Errorf("probe: %s: %w", label, err)
	}
	res := &Result{Kind: KindTransportStream, Source: label}
	for _, s := range prog.Streams {
		name, kind := codingName(s.Type)
		switch kind {
		case kindVideo:
			res.Video = append(res.Video, VideoTrack{Codec: name, BitDepth: discBitDepth(s.Type)})
			if s.Type == m2ts.TypeMVC {
				res.Is3D, res.Layout = true, LayoutMVC
			}
		case kindAudio:
			res.Audio = append(res.Audio, AudioTrack{Codec: name, Language: s.Lang})
		case kindSubtitle:
			res.Subtitle = append(res.Subtitle, SubtitleTrack{Codec: name, Language: s.Lang})
		}
	}
	if len(res.Video) == 0 {
		return nil, fmt.Errorf("probe: %s: the program has no video stream", label)
	}
	return res, nil
}
