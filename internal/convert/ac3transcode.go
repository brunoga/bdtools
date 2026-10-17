package convert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
)

// A DTS or PCM track in an MP4 output: the file's players (Apple's, the
// Quest's) take AC-3 and E-AC-3 from a disc's codings, so ffmpeg codes it
// as AC-3 (5.1 at most, at 640 kb/s; stereo at 320). The track keeps its
// place on the timeline, gaps and a late start included: each AC-3 frame
// is timed from where its first sample was in the source.

// ac3Rate is the AC-3 output's sample rate, and ac3Frame its samples a
// frame; ffmpeg's encoder delays its output by ac3Delay samples.
const (
	ac3Rate  = 48000
	ac3Frame = 1536
	ac3Delay = 256
)

// transcodeAudio codes the track e (of format DTS or WAV) as codec ("ac3"
// or "aac") into a file beside it, returning the file, its sync points and
// its format.
func (r *Runner) transcodeAudio(ctx context.Context, e extra, format mkv.AudioFormat, codec string) (string, []mkv.SyncPoint, mkv.AudioFormat, error) {
	// Where the source's frames start, in samples counted over the file,
	// and when they play: a point at the start and at every jump.
	f, err := os.Open(e.path) //nolint:gosec // our work directory
	if err != nil {
		return "", nil, 0, err
	}
	defer func() { _ = f.Close() }()
	src, err := mkv.NewAudioSource(f, format, false, "")
	if err != nil {
		return "", nil, 0, err
	}
	src.SetSyncPoints(e.sync)
	rate := src.Track().SampleRate
	channels := src.Track().Channels
	if rate <= 0 {
		return "", nil, 0, fmt.Errorf("no sample rate")
	}
	type mark struct {
		sample int64
		at     time.Duration
	}
	var marks []mark
	var prevStart int64
	var prevAt time.Duration
	for {
		fr, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, 0, err
		}
		// Where counting from the frame before puts this one, against
		// where the source does.
		start := src.SampleStart()
		want := prevAt + time.Duration((start-prevStart)*int64(time.Second)/int64(rate))
		if len(marks) == 0 || fr.PTS-want > time.Millisecond || want-fr.PTS > time.Millisecond {
			marks = append(marks, mark{start, fr.PTS})
		}
		prevStart, prevAt = start, fr.PTS
	}
	if len(marks) == 0 {
		return "", nil, 0, errors.New("no audio")
	}
	ext := ".ac3"
	if codec == "aac" {
		ext = ".aac"
	}
	out := strings.TrimSuffix(e.path, filepath.Ext(e.path)) + ext
	// PCM and DTS (its core): the decoder and encoders here; ffmpeg (to
	// AC-3) for what they do not take.
	var c *codedAudio
	s, err := openPCM(e.path, format)
	if err == nil {
		c, err = codeAudio(s, codec, out)
		s.close()
	}
	name := map[string]string{"ac3": "AC-3", "aac": "AAC"}[codec]
	if err == nil {
		r.Report.Report("coding the %s track %d as %s here (the file format and the players take it)", e.track.Type, e.track.ID, name)
	} else {
		r.Report.Report("the %s track %d: %v; trying ffmpeg (to AC-3)", e.track.Type, e.track.ID, err)
		bits, delay, ferr := r.ffmpegAC3(ctx, e, channels, out)
		if ferr != nil {
			return "", nil, 0, ferr
		}
		st, err := os.Stat(out)
		if err != nil {
			return "", nil, 0, err
		}
		size := int64(bits) * 1000 * ac3Frame / ac3Rate / 8 // bytes a frame at the constant rate
		c = &codedAudio{path: out, frameLen: ac3Frame, delay: delay, format: mkv.AC3}
		for off := int64(0); off+size <= st.Size(); off += size {
			c.offsets = append(c.offsets, off)
		}
	}
	// A sync point for each frame where the source's timeline moves on
	// other than by counting (the first, the frames after a jump).
	var sync []mkv.SyncPoint
	m := -1
	for k, off := range c.offsets {
		// The frame's first sample, in the source's samples.
		s := (int64(k)*int64(c.frameLen) - int64(c.delay)) * int64(rate) / ac3Rate
		mi := m
		for mi+1 < len(marks) && marks[mi+1].sample <= s {
			mi++
		}
		if mi < 0 {
			mi = 0
		}
		if mi != m {
			at := marks[mi].at + time.Duration((s-marks[mi].sample)*int64(time.Second)/int64(rate))
			sync = append(sync, mkv.SyncPoint{Offset: off, At: max(at, 0)})
			m = mi
		}
	}
	return out, sync, c.format, nil
}

// ffmpegAC3 codes the track with ffmpeg (DTS, and PCM the encoder here
// does not take), giving the AC-3 rate in kb/s and the coded delay.
func (r *Runner) ffmpegAC3(ctx context.Context, e extra, channels int, out string) (kbps, delay int, err error) {
	ff, err := r.resolve(toolFFmpeg)
	if err != nil {
		return 0, 0, errors.New("ffmpeg transcodes it to AC-3, and it is not installed")
	}
	kbps = 640
	args := []string{"-v", "error", "-nostdin", "-y", "-i", e.path}
	if channels > 6 {
		args = append(args, "-ac", "6")
	}
	if channels <= 2 {
		kbps = 320
	}
	args = append(args, "-ar", strconv.Itoa(ac3Rate), "-c:a", "ac3", "-b:a", fmt.Sprint(kbps, "k"), "-f", "ac3", out)
	cmd := exec.CommandContext(ctx, ff, args...) //nolint:gosec // our ffmpeg, our files
	if msg, err := cmd.CombinedOutput(); err != nil {
		return 0, 0, fmt.Errorf("ffmpeg: %v %s", err, strings.TrimSpace(string(msg)))
	}
	r.Report.Report("coding the %s track %d as AC-3 with ffmpeg (the file format and the players take AC-3 and E-AC-3)", e.track.Type, e.track.ID)
	return kbps, ac3Delay, nil
}
