package convert

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brunoga/bdtools/internal/ocr"
	"github.com/brunoga/bdtools/internal/pgs"
)

// Subtitles read (--subs-ocr): each PGS track's pictures read as text
// (internal/ocr), a text track beside the PGS one: SRT in Matroska, tx3g
// in MP4 (whose players take text and not pictures).

// ocrLangs are the languages the reading knows the letters of: those
// written in the Latin letters of Western Europe ("" and und too: a
// track that does not say is tried).
var ocrLangs = map[string]bool{
	"": true, "und": true, "eng": true, "por": true, "pob": true, "ptb": true, "spa": true, "esp": true,
	"fra": true, "fre": true, "deu": true, "ger": true, "ita": true, "nld": true, "dut": true,
	"swe": true, "nor": true, "nob": true, "nno": true, "dan": true, "fin": true, "cat": true,
	"glg": true, "eus": true, "baq": true, "isl": true, "ice": true, "gle": true, "lat": true,
}

// ocrTrack reads a PGS track's .sup into a text track in dir.
func (r *Runner) ocrTrack(e extra, id int, dir string) (extra, bool, error) {
	if !ocrLangs[strings.ToLower(strings.TrimSpace(e.track.Lang))] {
		r.Report.Report("subtitles: track %d (%s) is not read: its language's letters are not known here", e.track.ID, e.track.Lang)
		return extra{}, false, nil
	}
	m, err := ocr.Default()
	if err != nil {
		return extra{}, false, err
	}
	f, err := os.Open(e.path)
	if err != nil {
		return extra{}, false, err
	}
	d := pgs.NewDecoder()
	var comps []*pgs.Composition
	err = pgs.ReadSup(f, func(pts int64, seg []byte) error {
		if c, ok := d.Segment(pts, seg); ok {
			comps = append(comps, c)
		}
		return nil
	})
	_ = f.Close()
	if err != nil {
		return extra{}, false, err
	}
	start := time.Now()
	type shown struct{ at, end time.Duration }
	var times []shown
	var bms []*ocr.Bitmap
	t := func(pts int64) time.Duration { return time.Duration(pts) * time.Second / 90000 }
	for i, c := range comps {
		pic := c.Draw()
		if pic == nil {
			continue
		}
		end := t(c.PTS) + textDefaultDuration
		if i+1 < len(comps) {
			end = t(comps[i+1].PTS)
		}
		times = append(times, shown{t(c.PTS), end})
		bms = append(bms, ocr.NewBitmap(pic.W, pic.H, pic.Luma, pic.A))
	}
	texts := m.ReadTrack(bms)
	path := filepath.Join(dir, fmt.Sprintf("ocr_%d.cues", id))
	out, err := os.Create(path) //nolint:gosec // the work directory
	if err != nil {
		return extra{}, false, err
	}
	w := bufio.NewWriter(out)
	cues := 0
	for i, text := range texts {
		if text == "" || times[i].end <= times[i].at {
			continue
		}
		if err := writeCue(w, times[i].at, times[i].end-times[i].at, []byte(text)); err != nil {
			_ = out.Close()
			return extra{}, false, err
		}
		cues++
	}
	if err := w.Flush(); err != nil {
		_ = out.Close()
		return extra{}, false, err
	}
	if err := out.Close(); err != nil {
		return extra{}, false, err
	}
	r.Report.Report("subtitles: read track %d (%s): %d pictures, %d cues, in %.1f s", e.track.ID, e.track.Lang,
		len(bms), cues, time.Since(start).Seconds())
	if cues == 0 {
		return extra{}, false, nil
	}
	name := "Text"
	if e.track.Name != "" {
		name = e.track.Name + " (text)"
	}
	return extra{path: path, track: Track{ID: id, Type: "SRT", StreamID: "S_TEXT/UTF8", Lang: e.track.Lang,
		Name: name, Forced: e.track.Forced, Info: "SRT read from PGS"}}, true, nil
}
