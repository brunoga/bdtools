package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brunoga/bdtools/internal/ocr"
	"github.com/brunoga/bdtools/internal/pgs"
)

// Measuring on discs' subtitles: a film's PGS track (name.sup) beside its
// SRT (name.srt) of the same language, each picture read and set against
// the SRT cue that overlaps it most.

// event is a PGS picture and its time on screen.
type event struct {
	start, end time.Duration
	pic        *pgs.Picture
}

func readSup(path string) ([]event, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's files
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	d := pgs.NewDecoder()
	var comps []*pgs.Composition
	err = pgs.ReadSup(f, func(pts int64, seg []byte) error {
		if c, ok := d.Segment(pts, seg); ok {
			comps = append(comps, c)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var evs []event
	for i, c := range comps {
		if c.Empty() || i+1 >= len(comps) {
			continue
		}
		pic := c.Draw()
		if pic == nil {
			continue
		}
		t := func(pts int64) time.Duration { return time.Duration(pts) * time.Second / 90000 }
		evs = append(evs, event{t(c.PTS), t(comps[i+1].PTS), pic})
	}
	return evs, nil
}

// bitmap is a PGS picture as the OCR takes it: limited-range luma as
// ink, as opaque as the alpha.
func bitmap(p *pgs.Picture) *ocr.Bitmap {
	b := &ocr.Bitmap{W: p.W, H: p.H, Ink: make([]float32, len(p.A)), Opacity: make([]float32, len(p.A))}
	for i, a := range p.A {
		op := float32(a) / 255
		y := min(max((float32(p.Luma[i])-16)/219, 0), 1)
		b.Ink[i], b.Opacity[i] = y*op, op
	}
	return b
}

type srtCue struct {
	start, end time.Duration
	text       string
	italic     bool // the cue's text is in <i>
}

var srtTime = regexp.MustCompile(`(\d+):(\d+):(\d+)[,.](\d+)\s*-->\s*(\d+):(\d+):(\d+)[,.](\d+)`)

func readSRT(path string) []srtCue {
	b, err := os.ReadFile(path) //nolint:gosec // the operator's files
	if err != nil {
		return nil
	}
	var cues []srtCue
	for _, block := range strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n\n") {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		for i, l := range lines {
			m := srtTime.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			n := func(s string) time.Duration { v, _ := strconv.Atoi(s); return time.Duration(v) }
			at := n(m[1])*time.Hour + n(m[2])*time.Minute + n(m[3])*time.Second + n(m[4])*time.Millisecond
			end := n(m[5])*time.Hour + n(m[6])*time.Minute + n(m[7])*time.Second + n(m[8])*time.Millisecond
			raw := strings.Join(lines[i+1:], "\n")
			cues = append(cues, srtCue{at, end, clean(raw), strings.HasPrefix(strings.TrimSpace(raw), "<i>")})
			break
		}
	}
	return cues
}

// clean is a cue's text as compared: no markup, single spaces, lines
// apart by newlines.
func clean(s string) string {
	s = tag.ReplaceAllString(s, "")
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// readPicture reads a picture's lines.
func readPicture(m *ocr.Model, b *ocr.Bitmap, lh float64) string {
	var out []string
	for _, l := range b.Lines(lh) {
		img, w := b.Image(l, lh)
		if t := strings.TrimSpace(m.Recognise(img, w)); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}

// evaluateDiscs measures a model on the .sup and .srt pairs of a
// directory, printing each film's character error rate and the worst
// pictures.
func evaluateDiscs(m *ocr.Model, dir string, show int, dumpDir string) error {
	sups, _ := filepath.Glob(filepath.Join(dir, "*.sup"))
	if len(sups) == 0 {
		return fmt.Errorf("no .sup files in %s", dir)
	}
	var allErr, allChars int
	for _, sup := range sups {
		evs, err := readSup(sup)
		if err != nil {
			return fmt.Errorf("%s: %w", sup, err)
		}
		cues := readSRT(strings.TrimSuffix(sup, ".sup") + ".srt")
		// The track's line height, from all its pictures' lines.
		var heights []int
		bms := make([]*ocr.Bitmap, len(evs))
		for i, e := range evs {
			bms[i] = bitmap(e.pic)
			for _, l := range bms[i].Lines(0) {
				heights = append(heights, l.Height())
			}
		}
		lh := ocr.LineHeight(heights)
		type miss struct {
			d         int
			got, want string
		}
		var misses []miss
		var errs, chars, pictures int
		dumped := 0
		texts := make([]string, len(evs))
		for i := range evs {
			if dumpDir != "" && dumped < 12 && i%97 == 5 {
				for _, l := range bms[i].Lines(lh) {
					img, w := bms[i].Image(l, lh)
					_ = writeGray(filepath.Join(dumpDir, fmt.Sprintf("%s-%03d.png", strings.TrimSuffix(filepath.Base(sup), ".sup"), dumped)), img, w)
					dumped++
				}
			}
		}
		read := func(i int) string {
			if texts[i] == "" {
				texts[i] = readPicture(m, bms[i], lh)
			}
			return texts[i]
		}
		// Each cue against the pictures within it, read in order (a
		// picture the same as the one before once).
		k := 0
		for _, c := range cues {
			for k < len(evs) && evs[k].end <= c.start {
				k++
			}
			var parts []string
			for i := k; i < len(evs) && evs[i].start < c.end; i++ {
				e := evs[i]
				if min(e.end, c.end)-max(e.start, c.start) < (e.end-e.start)/2 {
					continue
				}
				if t := read(i); len(parts) == 0 || parts[len(parts)-1] != t {
					if m := os.Getenv("DUMPMATCH"); m != "" && strings.Contains(t, m) && dumpDir != "" {
						for j, l := range bms[i].Lines(lh) {
							img, w := bms[i].Image(l, lh)
							_ = writeGray(filepath.Join(dumpDir, fmt.Sprintf("match-%d-%d.png", i, j)), img, w)
						}
						raw := bms[i]
						_ = writeGray(filepath.Join(dumpDir, fmt.Sprintf("raw-%d.png", i)), raw.Ink, raw.W)
					}
					parts = append(parts, t)
				}
				pictures++
			}
			if len(parts) == 0 {
				continue
			}
			got, want := normalised(strings.Join(parts, "\n")), normalised(c.text)
			if want == "" {
				continue
			}
			d := distance(got, want)
			errs += d
			chars += utf8.RuneCountInString(want)
			if d > 0 {
				misses = append(misses, miss{d, got, want})
				if dumpDir != "" && len(misses) <= 6 {
					for i := k; i < len(evs) && evs[i].start < c.end; i++ {
						for j, l := range bms[i].Lines(lh) {
							img, w := bms[i].Image(l, lh)
							_ = writeGray(filepath.Join(dumpDir, fmt.Sprintf("%s-miss%d-%d-%d.png", strings.TrimSuffix(filepath.Base(sup), ".sup"), len(misses), i, j)), img, w)
						}
					}
				}
			}
		}
		// Italics against the cues' <i>.
		var conf [2][2]int
		for i, e := range evs {
			for _, c := range cues {
				if min(e.end, c.end)-max(e.start, c.start) >= (e.end-e.start)/2 {
					got := bms[i].Italic(bms[i].Lines(lh))
					conf[b2i(c.italic)][b2i(got)]++
					break
				}
			}
		}
		fmt.Printf("    italics: %d of %d italic pictures found, %d of %d upright ones taken for italic\n",
			conf[1][1], conf[1][0]+conf[1][1], conf[0][1], conf[0][0]+conf[0][1])
		allErr += errs
		allChars += chars
		fmt.Printf("%s: %d pictures, line height %.0f, %.2f%% characters wrong, %d pictures wrong\n",
			filepath.Base(sup), pictures, lh, 100*float64(errs)/float64(max(chars, 1)), len(misses))
		sort.Slice(misses, func(i, j int) bool { return misses[i].d > misses[j].d })
		for _, x := range misses[:min(show, len(misses))] {
			fmt.Printf("    %q\n  vs %q\n", x.got, x.want)
		}
	}
	fmt.Printf("all: %.2f%% characters wrong\n", 100*float64(allErr)/float64(max(allChars, 1)))
	return nil
}

var (
	annotation = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|♪|♫`)
	speaker    = regexp.MustCompile(`(?m)^\s*[A-Z][A-Z0-9 .'#-]+:\s*`)
	dash       = regexp.MustCompile(`(?m)^\s*-+\s*`)
)

// normalised is a cue's text as compared: without what a track for the
// hard of hearing adds (sounds, speakers), dialogue dashes, or line
// breaks.
func normalised(s string) string {
	s = annotation.ReplaceAllString(s, "")
	s = speaker.ReplaceAllString(s, "")
	s = dash.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
}

// overlapping is the text of the cue that overlaps [start, end) most, if
// one overlaps most of it.
func overlapping(cues []srtCue, start, end time.Duration) (string, bool) {
	best, at := time.Duration(0), -1
	for i, c := range cues {
		o := min(end, c.end) - max(start, c.start)
		if o > best {
			best, at = o, i
		}
	}
	if at < 0 || best < (end-start)/2 {
		return "", false
	}
	return cues[at].text, true
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
