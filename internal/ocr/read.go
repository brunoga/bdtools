package ocr

import (
	"bytes"
	_ "embed"
	"runtime"
	"strings"
	"sync"
	"unicode"
)

//go:embed model.bin
var modelData []byte

var (
	defaultModel *Model
	defaultErr   error
	defaultOnce  sync.Once
)

// Default is the model trained here, embedded.
func Default() (*Model, error) {
	defaultOnce.Do(func() { defaultModel, defaultErr = Load(bytes.NewReader(modelData)) })
	return defaultModel, defaultErr
}

// ReadTrack reads a track's pictures: their text, lines apart by
// newlines, an italic picture's lines in SRT's <i> ("" for a picture
// with none). The track's line height comes
// from all its pictures; the lines are read on every CPU.
func (m *Model) ReadTrack(pics []*Bitmap) []string {
	var heights []int
	lines := make([][]Line, len(pics))
	for _, b := range pics {
		for _, l := range b.Lines(0) {
			heights = append(heights, l.Height())
		}
	}
	lh := LineHeight(heights)
	type job struct{ pic, line int }
	var jobs []job
	for i, b := range pics {
		lines[i] = b.Lines(lh)
		for j := range lines[i] {
			jobs = append(jobs, job{i, j})
		}
	}
	texts := make([][]string, len(pics))
	for i := range pics {
		texts[i] = make([]string, len(lines[i]))
	}
	var next int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				k := next
				next++
				mu.Unlock()
				if k >= len(jobs) {
					return
				}
				jb := jobs[k]
				img, w := pics[jb.pic].Image(lines[jb.pic][jb.line], lh)
				texts[jb.pic][jb.line] = Fix(m.Recognise(img, w))
			}
		}()
	}
	wg.Wait()
	out := make([]string, len(pics))
	for i, t := range texts {
		italic := pics[i].Italic(lines[i])
		var kept []string
		for _, l := range t {
			if l = strings.TrimSpace(l); l != "" {
				if italic {
					l = "<i>" + l + "</i>"
				}
				kept = append(kept, l)
			}
		}
		out[i] = strings.Join(kept, "\n")
	}
	return out
}

// lWords are words that, spelt with an l for their capital I, are none:
// the subtitles' languages' usual ones.
var lWords = map[string]string{
	"lt": "It", "lt's": "It's", "lt'll": "It'll", "lts": "Its", "ls": "Is", "lsn't": "Isn't",
	"lf": "If", "ln": "In", "lnto": "Into", "lsso": "Isso", "lsto": "Isto", "lsa": "Isa",
}

// Fix corrects what the pictures cannot tell apart: a capital I from a
// small l in sans-serif faces. An l in a word of capitals, alone, or
// before an apostrophe at a word's start (l'm, l'll) is an I, and so is
// the l of a word that is none with it (lt's, lf).
func Fix(line string) string {
	words := strings.Split(line, " ")
	for wi, w := range words {
		core := strings.TrimRight(w, ".,!?;:\"…")
		if v, ok := lWords[core]; ok {
			words[wi] = v + w[len(core):]
			continue
		}
		r := []rune(w)
		if !strings.ContainsRune(w, 'l') {
			continue
		}
		upper, lower := 0, 0
		for _, c := range r {
			switch {
			case unicode.IsUpper(c):
				upper++
			case unicode.IsLower(c) && c != 'l':
				lower++
			}
		}
		for i, c := range r {
			if c != 'l' {
				continue
			}
			alone := len(strings.Trim(w, ".,!?;:\"-…")) == 1
			start := i == 0 || !unicode.IsLetter(r[i-1])
			apostrophe := start && i+1 < len(r) && (r[i+1] == '\'' || r[i+1] == '’')
			if (upper > 0 && lower == 0) || alone || apostrophe {
				r[i] = 'I'
			}
		}
		words[wi] = string(r)
	}
	return strings.Join(words, " ")
}
