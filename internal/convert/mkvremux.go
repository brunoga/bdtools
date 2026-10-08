package convert

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/dovi"
)

// A lossless 2D remux into Matroska: the disc's video, untouched, with the
// tracks chosen. The video's access units are written as the elementary
// stream they are, and the muxer times them from their picture order, as it
// does an encoder's output; the audio and subtitles are the same files a
// conversion makes.
//
// A remux cannot cut inside a GOP, so it starts at the random access point
// at or before the playlist's IN time; pictures before IN that it carries
// are a fraction of a second at most, and a disc's feature normally starts
// at one anyway.

// remuxCodecs are the video codecs a Matroska remux carries.
var remuxCodecs = map[string]Codec{"V_MPEG4/ISO/AVC": CodecH264, "V_MPEGH/ISO/HEVC": CodecH265}

// isMKVOutput reports whether the output is a Matroska file.
func isMKVOutput(path string) bool { return strings.EqualFold(filepath.Ext(path), ".mkv") }

// remuxToMKV copies the video of next (the demuxer's access units, kept by
// keep) into the work directory, then muxes it with extras.
func (r *Runner) remuxToMKV(ctx context.Context, video Track, next func() (base, dep []byte, pts int64, err error),
	keep func(int64) bool, finish func() ([]extra, error), chapters []time.Duration) error {
	codec, ok := remuxCodecs[video.StreamID]
	if !ok {
		_, _ = finish()
		return fmt.Errorf("a Matroska remux of %s video is not supported yet; convert it, or remux to .m2ts", video.Type)
	}
	path := filepath.Join(r.work.dir, "video"+codec.streamExt())
	f, err := os.Create(path) //nolint:gosec // our work directory
	if err != nil {
		_, _ = finish()
		return err
	}
	w := bufio.NewWriterSize(f, 8<<20)
	r.Report.Report("remuxing the %s video into Matroska, untouched", video.Type)
	var (
		held      [][]byte // access units since the last random access point, before IN
		heldFirst int64
		started   bool
		n, early  int
		writeErr  error
		// A UHD Blu-ray's Dolby Vision enhancement layer goes into the
		// track with the base layer.
		el           = r.Selected.Dependent.Kind() == KindEnhancement
		merged, rpus int
	)
	write := func(au []byte) {
		if writeErr == nil {
			_, writeErr = w.Write(au)
			n++
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			writeErr = err
			break
		}
		base, dep, pts, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeErr = err
			break
		}
		if len(base) == 0 {
			continue
		}
		if dep != nil && el {
			base = dovi.Merge(base, dep)
			merged++
			if dovi.HasRPU(dep) {
				rpus++
			}
		}
		in := keep == nil || keep(pts)
		switch {
		case !started && !in:
			// Before IN: only what the first picture inside needs.
			if randomAccess(base, codec) {
				held, heldFirst = nil, pts
			}
			if held != nil || randomAccess(base, codec) {
				held = append(held, base)
			}
		case !started:
			started = true
			first := pts
			if len(held) > 0 {
				first = heldFirst
				early = len(held)
				for _, au := range held {
					write(au)
				}
				held = nil
			}
			r.noteFirstPicture(first)
			write(base)
		case in:
			write(base)
		}
	}
	if ferr := w.Flush(); writeErr == nil {
		writeErr = ferr
	}
	if cerr := f.Close(); writeErr == nil {
		writeErr = cerr
	}
	extras, finErr := finish()
	if writeErr != nil {
		return writeErr
	}
	if finErr != nil {
		return finErr
	}
	if n == 0 {
		return errors.New("the title has no video to remux")
	}
	if early > 0 {
		r.Report.Report("kept %d pictures from before the playlist's IN time, from the random access point the first needs", early)
	}
	r.fpsNum, r.fpsDen = r.rateNum, r.rateDen
	if r.fpsNum == 0 {
		r.fpsNum, r.fpsDen = 24000, 1001
		r.Report.Report("warning: the source states no frame rate; assuming 24000/1001")
	}
	r.Opts.Codec = codec // the muxer's video codec
	r.Report.Report("copied %d pictures", n)
	if merged > 0 {
		cfg := dovi.UHDBluRay(0, 0, 0, 0) // the level comes from the picture, at the mux
		r.dovi = &cfg
		r.Report.Report("kept Dolby Vision profile 7: the enhancement layer on %d pictures, the RPU on %d", merged, rpus)
	}
	return r.mux(ctx, []string{path}, extras, chapters)
}

// randomAccess reports whether an access unit can start decoding: an IDR
// (or an I-slice picture with a recovery point) in H.264, an IRAP picture in
// HEVC.
func randomAccess(au []byte, codec Codec) bool {
	for i := 0; i+3 < len(au); i++ {
		if au[i] != 0 || au[i+1] != 0 || au[i+2] != 1 {
			continue
		}
		h := au[i+3]
		if codec == CodecH265 {
			if t := h >> 1 & 0x3f; t >= 16 && t <= 23 {
				return true
			} else if t < 16 {
				return false
			}
			continue
		}
		switch h & 0x1f {
		case 5:
			return true
		case 6: // SEI: a recovery point makes the next picture an entry
			if i+4 < len(au) && au[i+4] == 6 {
				return true
			}
		case 1:
			return false
		}
	}
	return false
}
