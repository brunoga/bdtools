package convert

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Resuming an interrupted conversion. The video is encoded in segments of
// segmentFrames pictures, each a complete stream from its own encoder run,
// and a manifest in the work directory lists the segments that finished.
// Run again with the same source and settings, a conversion decodes from
// the start (the decoder needs the pictures before a point to decode it,
// and runs at several times the encoder's speed) but encodes only from the
// first picture no segment holds. The audio and subtitles are demuxed again
// on the way, which costs nothing extra: they come out of the same read.
//
// A segment's pictures are counted among those kept (inside the playlist's
// IN and OUT), so the same source and settings always cut the same
// segments. Anything that would change them — the source, the codec, the
// encoder, the quality, the layout, the eye order, the bit depth, the
// title — is in the manifest's key, and a run whose key differs starts
// over.

// segmentFrames is how many pictures a segment holds: ten keyframe
// intervals, under two minutes of film. An interrupted run loses at most
// that much encoding, seconds on a GPU and minutes in software.
var segmentFrames = 10 * 250 // a variable for tests

// manifestName is the manifest's file in the work directory.
const manifestName = "segments.json"

// manifestVersion changes when the segments' meaning does.
const manifestVersion = 1

type manifest struct {
	Version  int       `json:"version"`
	Key      string    `json:"key"`
	Segments []segment `json:"segments"`
}

type segment struct {
	File   string `json:"file"` // in the work directory
	Frames int    `json:"frames"`
	// EL is the Dolby Vision enhancement layer's file for the segment, when
	// the layers are kept apart.
	EL string `json:"el,omitempty"`
}

// done is how many pictures the finished segments hold.
func (m *manifest) done() int {
	n := 0
	for _, s := range m.Segments {
		n += s.Frames
	}
	return n
}

// work is the conversion's work directory and what an earlier run left in
// it.
type work struct {
	dir string
	m   manifest
}

// workDirName is the work directory for an output: beside it (or under
// --temp), named after it, so the same command finds it again.
func workDirName(tmp, output string) string {
	return filepath.Join(tmp, "."+filepath.Base(output)+".bdtools")
}

// openWork makes or reopens the work directory. What an earlier run left
// other than its segments (demuxed audio and subtitles, a segment cut
// short) is removed: the run makes them again.
func openWork(dir string) (*work, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("creating the work directory %s: %w", dir, err)
	}
	w := &work{dir: dir}
	if b, err := os.ReadFile(filepath.Join(dir, manifestName)); err == nil { //nolint:gosec // our work directory
		if json.Unmarshal(b, &w.m) != nil || w.m.Version != manifestVersion {
			w.m = manifest{}
		}
	}
	// Only segments that are all there count.
	var good []segment
	for _, s := range w.m.Segments {
		if fi, err := os.Stat(filepath.Join(dir, s.File)); err != nil || fi.Size() == 0 || s.Frames <= 0 {
			break
		}
		if s.EL != "" {
			if fi, err := os.Stat(filepath.Join(dir, s.EL)); err != nil || fi.Size() == 0 {
				break
			}
		}
		good = append(good, s)
	}
	w.m.Segments = good
	w.tidy()
	return w, nil
}

// tidy removes everything in the directory but the manifest and its
// segments.
func (w *work) tidy() {
	keep := map[string]bool{manifestName: true}
	for _, s := range w.m.Segments {
		keep[s.File] = true
		if s.EL != "" {
			keep[s.EL] = true
		}
	}
	entries, _ := os.ReadDir(w.dir)
	for _, e := range entries {
		if !keep[e.Name()] {
			_ = os.RemoveAll(filepath.Join(w.dir, e.Name()))
		}
	}
}

// begin settles the segments against the run's key: kept when it is the
// key they were made with, dropped otherwise. It returns how many pictures
// are already encoded.
func (w *work) begin(key string, report Reporter) (int, error) {
	if w.m.Key != key && len(w.m.Segments) > 0 {
		report.Report("an earlier run left %d encoded frames, with other settings or another source: starting over", w.m.done())
		w.m.Segments = nil
		w.tidy()
	}
	w.m.Version, w.m.Key = manifestVersion, key
	return w.m.done(), w.save()
}

// add records a finished segment.
func (w *work) add(s segment) error {
	w.m.Segments = append(w.m.Segments, s)
	return w.save()
}

// save writes the manifest so that a crash at any point leaves either the
// old one or the new one whole.
func (w *work) save() error {
	b, err := json.MarshalIndent(w.m, "", "\t")
	if err != nil {
		return err
	}
	tmp := filepath.Join(w.dir, manifestName+".new")
	f, err := os.Create(tmp) //nolint:gosec // our work directory
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(w.dir, manifestName))
}

// segmentPath is where segment i is written.
func (w *work) segmentPath(i int, ext string) string {
	return filepath.Join(w.dir, fmt.Sprintf("video-%04d%s", i, ext))
}

// elSegmentPaths are the finished segments' enhancement layer files, in
// order: nil when the segments have none, an error when some do and some
// do not.
func (w *work) elSegmentPaths() ([]string, error) {
	var out []string
	for _, s := range w.m.Segments {
		if s.EL == "" {
			if out != nil {
				return nil, errors.New("some segments have an enhancement layer and some do not: start over with --restart")
			}
			continue
		}
		if len(out) == 0 && s != w.m.Segments[0] {
			return nil, errors.New("some segments have an enhancement layer and some do not: start over with --restart")
		}
		out = append(out, filepath.Join(w.dir, s.EL))
	}
	return out, nil
}

// segmentPaths are the finished segments, in order.
func (w *work) segmentPaths() []string {
	var out []string
	for _, s := range w.m.Segments {
		out = append(out, filepath.Join(w.dir, s.File))
	}
	return out
}

// resumeKey is what the segments depend on: the source (by name, size and
// time) and every setting that changes the encoded pictures.
func (r *Runner) resumeKey() (string, error) {
	in, err := filepath.Abs(r.Opts.Input)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(in)
	if err != nil {
		return "", err
	}
	o := r.Opts
	b, err := json.Marshal(struct {
		Input     string
		Size      int64
		Modified  time.Time
		Playlist  string
		Codec     Codec
		Encoder   Encoder
		NativeGPU bool
		CRF       int
		Preset    string
		Layout    Layout
		SwapLR    bool
		BitDepth  int
		Segment   int
		DVFEL     FEL `json:",omitempty"`
		DVELCRF   int `json:",omitempty"`
	}{in, fi.Size(), fi.ModTime().UTC(), o.Playlist, o.Codec, o.Encoder, o.NativeGPU, o.CRF, o.Preset, o.Layout,
		o.SwapLR, max(o.BitDepth, 8), segmentFrames, o.DVFEL, o.DVELCRF})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// openSegments reads the segments as one stream. An IVF segment (what
// SvtAv1EncApp writes) after the first loses its file header, so its frame
// records follow on from the last segment's.
func openSegments(paths []string) (io.Reader, func(), error) {
	var files []*os.File
	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	var readers []io.Reader
	for i, p := range paths {
		f, err := os.Open(p) //nolint:gosec // our work directory
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		files = append(files, f)
		if i > 0 {
			if err := skipIVFHeader(f); err != nil {
				closeAll()
				return nil, nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
			}
		}
		readers = append(readers, f)
	}
	return io.MultiReader(readers...), closeAll, nil
}

// skipIVFHeader moves past an IVF file header, or leaves a file that has
// none where it was.
func skipIVFHeader(f *os.File) error {
	var h [32]byte
	n, err := io.ReadFull(f, h[:])
	if err == nil && bytes.Equal(h[:4], []byte("DKIF")) {
		size := int64(h[6]) | int64(h[7])<<8
		_, err = f.Seek(size, io.SeekStart)
		return err
	}
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return err
	}
	_, err = f.Seek(int64(-n), io.SeekCurrent)
	return err
}
