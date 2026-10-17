package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/brunoga/bdtools/internal/ocr"
)

// Lines from discs to learn from: a film's PGS pictures against its SRT
// of the same language. A picture is taken when the cue over it has as
// many lines, and each line as the model reads it now is near the cue's
// (an SRT made for another cut, or a track for the hard of hearing beside
// a plain one, does not teach).

// realLine is a disc's line, its picture kept as bytes.
type realLine struct {
	img  []byte
	w    int
	text string
}

func (r realLine) sample() ocr.Sample {
	img := make([]float32, len(r.img))
	for i, v := range r.img {
		img[i] = float32(v) / 255
	}
	return ocr.Sample{Img: img, W: r.w, Text: r.text}
}

// loadReal reads the pairs of a directory's .sup and .srt files.
func loadReal(dir string, m *ocr.Model) []realLine {
	sups, _ := filepath.Glob(filepath.Join(dir, "*.sup"))
	var mu sync.Mutex
	var out []realLine
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for _, sup := range sups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines := realPairs(sup, m, sem)
			mu.Lock()
			out = append(out, lines...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func realPairs(sup string, m *ocr.Model, sem chan struct{}) []realLine {
	evs, err := readSup(sup)
	if err != nil {
		return nil
	}
	cues := readSRT(strings.TrimSuffix(sup, ".sup") + ".srt")
	var heights []int
	bms := make([]*ocr.Bitmap, len(evs))
	for i, e := range evs {
		bms[i] = bitmap(e.pic)
		for _, l := range bms[i].Lines(0) {
			heights = append(heights, l.Height())
		}
	}
	lh := ocr.LineHeight(heights)
	var out []realLine
	for i, e := range evs {
		want, ok := overlapping(cues, e.start, e.end)
		if !ok {
			continue
		}
		labels := strings.Split(want, "\n")
		ls := bms[i].Lines(lh)
		if len(ls) != len(labels) {
			continue
		}
		for j, l := range ls {
			label := strings.TrimSpace(labels[j])
			if _, ok := ocr.Encode(label); !ok || label == "" {
				continue
			}
			img, w := bms[i].Image(l, lh)
			sem <- struct{}{}
			got := m.Recognise(img, w)
			<-sem
			if d := distance(got, label); d > max(2, utf8.RuneCountInString(label)/4) {
				continue
			}
			b := make([]byte, len(img))
			for k, v := range img {
				b[k] = byte(255*min(max(v, 0), 1) + 0.5)
			}
			out = append(out, realLine{b, w, label})
		}
	}
	return out
}
