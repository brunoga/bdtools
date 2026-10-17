// Command ocrtrain trains the subtitle OCR's network (internal/ocr) on
// lines drawn from subtitle text: SRT files' lines, drawn as a disc's
// subtitles are, in the fonts here.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brunoga/bdtools/internal/ocr"
)

func main() {
	var (
		corpus = flag.String("corpus", "", "a directory of .srt files: the text drawn")
		out    = flag.String("o", "model.bin", "the model written")
		resume = flag.String("resume", "", "a model to go on training")
		steps  = flag.Int("steps", 20000, "training steps")
		batch  = flag.Int("batch", 48, "lines a step")
		lr     = flag.Float64("lr", 1e-3, "the peak learning rate")
		every  = flag.Int("every", 500, "steps between validations and checkpoints")
		seed   = flag.Uint64("seed", 1, "the random seed")
		dump   = flag.String("dump", "", "write 40 samples as PNG pictures here, and stop")
		evalD  = flag.String("eval", "", "measure the model (-resume) on the .sup and .srt pairs of this directory, and stop")
		show   = flag.Int("show", 5, "with -eval, the worst pictures shown a film")
		mk     = flag.String("mksup", "", "write the test subtitles (.sup) here, and stop")
		realD  = flag.String("real", "", "a directory of discs' .sup and .srt pairs to learn from too")
		realP  = flag.Float64("realp", 0.3, "with -real, the share of each batch from discs")
	)
	flag.Parse()
	if *mk != "" {
		if err := mkSup(*mk, fixtureCues); err != nil {
			fmt.Fprintln(os.Stderr, "ocrtrain:", err)
			os.Exit(1)
		}
		return
	}
	if *evalD != "" {
		f, err := os.Open(*resume)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ocrtrain:", err)
			os.Exit(1)
		}
		m, err := ocr.Load(f)
		_ = f.Close()
		if err == nil {
			err = evaluateDiscs(m, *evalD, *show, *dump)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "ocrtrain:", err)
			os.Exit(1)
		}
		return
	}
	lines, err := readCorpus(*corpus)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ocrtrain:", err)
		os.Exit(1)
	}
	fonts, err := loadFonts()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ocrtrain: no fonts:", err)
		os.Exit(1)
	}
	fmt.Printf("%d lines of text, %d fonts\n", len(lines), len(fonts))
	rng := rand.New(rand.NewPCG(*seed, 99)) //nolint:gosec // training data
	rng.Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
	nVal := min(1000, len(lines)/20)
	valText, trainText := lines[:nVal], lines[nVal:]
	val := make([]ocr.Sample, 0, nVal)
	vfc := faces{}
	vrng := rand.New(rand.NewPCG(7, 7)) //nolint:gosec // training data
	for _, t := range valText {
		if s, ok := sample(vrng, fonts, vfc, t); ok {
			val = append(val, s)
		}
	}
	if *dump != "" && *evalD == "" {
		if err := dumpSamples(*dump, val[:min(40, len(val))]); err != nil {
			fmt.Fprintln(os.Stderr, "ocrtrain:", err)
			os.Exit(1)
		}
		return
	}
	m := ocr.NewModel(*seed)
	if *resume != "" {
		f, err := os.Open(*resume)
		if err == nil {
			m, err = ocr.Load(f)
			_ = f.Close()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "ocrtrain:", err)
			os.Exit(1)
		}
	}
	var real []realLine
	if *realD != "" {
		real = loadReal(*realD, m)
		fmt.Printf("%d lines from discs\n", len(real))
	}
	tr := ocr.NewTrainer(m, *lr)
	batches := make(chan []ocr.Sample, 4)
	for g := range max(1, runtime.GOMAXPROCS(0)/6) {
		go func() {
			r := rand.New(rand.NewPCG(*seed, uint64(g)+1000)) //nolint:gosec // training data
			fc := faces{}
			for {
				b := make([]ocr.Sample, 0, *batch)
				for len(b) < *batch {
					if len(real) > 0 && r.Float64() < *realP {
						b = append(b, real[r.IntN(len(real))].sample())
						continue
					}
					if s, ok := sample(r, fonts, fc, sdh(r, trainText)); ok {
						b = append(b, s)
					}
				}
				batches <- b
			}
		}()
	}
	start := time.Now()
	var sum float64
	var n int
	for step := 1; step <= *steps; step++ {
		// Warm up over 500 steps, then a cosine down to a hundredth.
		frac := float64(step) / float64(*steps)
		tr.LR = *lr * (0.01 + 0.99*0.5*(1+math.Cos(math.Pi*frac)))
		if step < 500 {
			tr.LR = *lr * float64(step) / 500
		}
		loss, k := tr.Step(<-batches)
		if k > 0 {
			sum += loss
			n++
		}
		if step%50 == 0 {
			fmt.Printf("step %d loss %.3f lr %.2g %.1f lines/s\n", step, sum/float64(max(n, 1)), tr.LR,
				float64(step**batch)/time.Since(start).Seconds())
			sum, n = 0, 0
		}
		if step%*every == 0 || step == *steps {
			cer, ler := evaluate(tr.M, val)
			fmt.Printf("step %d validation: %.2f%% characters wrong, %.1f%% lines\n", step, 100*cer, 100*ler)
			if err := save(tr.M, *out); err != nil {
				fmt.Fprintln(os.Stderr, "ocrtrain:", err)
			}
		}
	}
}

// fixtureCues are the test subtitles' (internal/convert's testdata).
var fixtureCues = []srtCue{
	{1 * time.Second, 2500 * time.Millisecond, "Hello there.", false},
	{3 * time.Second, 5 * time.Second, "Where are you going?\nHome, I think.", false},
	{6 * time.Second, 7500 * time.Millisecond, "It's 10:45 already!", false},
}

// sdh is a corpus line, or now and then one as a track for the hard of
// hearing writes it (sounds in brackets, speakers in capitals, songs),
// or with a dialogue dash: what plain subtitles rarely have.
func sdh(r *rand.Rand, lines []string) string {
	l := lines[r.IntN(len(lines))]
	words := func(n int) string {
		w := strings.Fields(lines[r.IntN(len(lines))])
		if len(w) > n {
			w = w[:n]
		}
		return strings.Trim(strings.Join(w, " "), ".,!?-")
	}
	switch k := r.IntN(100); {
	case k < 6:
		return "[" + strings.ToUpper(words(1+r.IntN(3))) + "]"
	case k < 9:
		return "(" + strings.ToLower(words(1+r.IntN(3))) + ")"
	case k < 12:
		return "[" + strings.ToUpper(words(1+r.IntN(2))) + "] " + l
	case k < 16:
		return strings.ToUpper(words(1)) + ": " + l
	case k < 18:
		return "♪ " + l + " ♪"
	case k < 23:
		return "-" + l
	case k < 26:
		return strings.ToUpper(l)
	}
	return l
}

// sample draws a line of text and readies it for the network.
func sample(rng *rand.Rand, fonts []*fontFile, fc faces, text string) (ocr.Sample, bool) {
	b, lh, ok := render(rng, fonts, fc, text)
	if !ok {
		return ocr.Sample{}, false
	}
	ls := b.Lines(lh)
	if len(ls) != 1 {
		return ocr.Sample{}, false
	}
	img, w := b.Image(ls[0], lh)
	return ocr.Sample{Img: img, W: w, Text: text}, true
}

func evaluate(m *ocr.Model, val []ocr.Sample) (cer, ler float64) {
	var errs, chars, wrong int
	for _, s := range val {
		got := m.Recognise(s.Img, s.W)
		d := distance(got, s.Text)
		errs += d
		chars += utf8.RuneCountInString(s.Text)
		if d > 0 {
			wrong++
		}
	}
	return float64(errs) / float64(max(chars, 1)), float64(wrong) / float64(max(len(val), 1))
}

// distance is the edit distance between two texts, by character.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			c := 1
			if ra[i-1] == rb[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func save(m *ocr.Model, path string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // the operator's path
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if err := m.Save(w); err != nil {
		_ = f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var (
	tag      = regexp.MustCompile(`<[^>]*>|\{[^}]*\}`)
	timeLine = regexp.MustCompile(`-->`)
)

// readCorpus reads the lines of every .srt file in a directory: their
// text without markup, those the charset can say, once each.
func readCorpus(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.srt"))
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("no .srt files in %q", dir)
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // the operator's corpus
		if err != nil || !utf8.Valid(b) {
			continue // not UTF-8 (old Latin-1 files): left out
		}
		for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n") {
			l = strings.TrimSpace(tag.ReplaceAllString(l, ""))
			if l == "" || timeLine.MatchString(l) || isNumber(l) || utf8.RuneCountInString(l) > 60 || seen[l] {
				continue
			}
			if _, ok := ocr.Encode(l); !ok {
				continue
			}
			seen[l] = true
			out = append(out, l)
		}
	}
	return out, nil
}

func isNumber(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// dumpSamples writes samples' pictures, for looking at what the network
// sees.
func dumpSamples(dir string, samples []ocr.Sample) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for i, s := range samples {
		if err := writeGray(filepath.Join(dir, fmt.Sprintf("%02d.png", i)), s.Img, s.W); err != nil {
			return err
		}
		fmt.Printf("%02d %q\n", i, s.Text)
	}
	return nil
}

func writeGray(path string, px []float32, w int) error {
	img := image.NewGray(image.Rect(0, 0, w, len(px)/w))
	for j, v := range px {
		img.Pix[j] = uint8(255 * min(max(v, 0), 1))
	}
	f, err := os.Create(path) //nolint:gosec // the operator's directory
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
