package probe

import (
	"fmt"
	"io"

	"github.com/brunoga/bdtools/internal/mp4"
)

// MP4 probes an MP4 or QuickTime file (an ISO base media file): its
// tracks from the movie box, which sits at the file's start or end, so
// the first and last pieces of a download are enough. size is the file's
// full length. The caller owns r and MP4 does not close it.
//
// MV-HEVC (Apple's spatial video) is told by its second layer's
// configuration (lhvC) and reported as LayoutMVHEVC, unless Apple's vexu
// box names one eye only; a frame holding both eyes is told by its st3d
// box. When a read fails because the sample lacks a piece, the error is a
// *MissingDataError naming the bytes wanted.
func MP4(r io.ReaderAt, size int64, label string) (res *Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			res, err = nil, fmt.Errorf("probe: %s: malformed MP4 (%v)", label, p)
		}
	}()
	rr := &watchReader{r: r}
	moov, err := mp4.MovieAt(rr, size)
	if err != nil {
		err = fmt.Errorf("probe: %s: %w", label, err)
		if rr.failed != nil {
			return nil, &MissingDataError{Offset: rr.off, Length: rr.n, Err: rr.failed, Probe: err}
		}
		return nil, err
	}
	m, err := mp4.ParseMovie(moov)
	if err != nil {
		return nil, fmt.Errorf("probe: %s: %w", label, err)
	}
	res = &Result{Kind: KindMP4, Source: label, Duration: m.Duration}
	for _, t := range m.Tracks {
		switch {
		case t.CodecID != "" && t.CodecID[0] == 'V':
			res.Video = append(res.Video, VideoTrack{Codec: videoCodec(t.CodecID), Width: t.Width, Height: t.Height, BitDepth: mkvBitDepth(t)})
			if res.Is3D {
				continue
			}
			switch {
			case len(m.LayerParams[t.Number]) > 0 && m.Stereo[t.Number] != "one":
				res.Is3D, res.Layout = true, LayoutMVHEVC
			case t.StereoMode != 0:
				res.Is3D, res.Layout = true, stereoLayout(t.StereoMode)
			}
		case t.CodecID != "":
			lang := t.Language
			if lang == "und" {
				lang = ""
			}
			res.Audio = append(res.Audio, AudioTrack{Codec: audioCodec(t.CodecID), Language: lang, Channels: t.Channels, SampleRate: t.SampleRate})
		}
	}
	if len(res.Video) == 0 {
		return nil, fmt.Errorf("probe: %s: no video track", label)
	}
	return res, nil
}
