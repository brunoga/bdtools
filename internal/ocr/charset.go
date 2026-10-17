// Package ocr reads the text of subtitle pictures (a Blu-ray's PGS): a
// line of text at a time, by a small network trained here (a CRNN:
// convolutions over the line's picture, a bidirectional LSTM over its
// columns, CTC to the characters), its weights embedded.
package ocr

// charset is what the network reads: the Latin script of the subtitles'
// languages (Western European ones, accented letters included),
// punctuation and the space. Index 0 is CTC's blank.
var charset = []rune(" !\"#$%&'()*+,-./0123456789:;<=>?@" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_" +
	"abcdefghijklmnopqrstuvwxyz{|}~" +
	"¡¿«»°ºª€£¥§©®…–—‘’“”•·♪♫" +
	"ÀÁÂÃÄÅÆÇÈÉÊËÌÍÎÏÑÒÓÔÕÖØÙÚÛÜÝŒŸ" +
	"àáâãäåæçèéêëìíîïñòóôõöøùúûüýÿœß")

// classes is how many outputs the network has: the blank, then charset.
var classes = len(charset) + 1

// classOf maps a character to its class; 0 for none.
var classOf = func() map[rune]int {
	m := make(map[rune]int, len(charset))
	for i, r := range charset {
		m[r] = i + 1
	}
	return m
}()

// normalise maps characters the charset does not have to ones it does
// (typographic variants), or 0 to leave them out.
func normalise(r rune) rune {
	if _, ok := classOf[r]; ok {
		return r
	}
	switch r {
	case ' ', '\t':
		return ' '
	case '´', '`', '′':
		return '\''
	case '″', '„':
		return '"'
	case '‐', '‑', '‒', '−':
		return '-'
	case '‹':
		return '<'
	case '›':
		return '>'
	}
	return 0
}

// Encode turns a text into its classes, leaving out what the charset
// cannot say; ok is false when something was left out.
func Encode(text string) (labels []int, ok bool) {
	ok = true
	for _, r := range text {
		if r = normalise(r); r == 0 {
			ok = false
			continue
		}
		labels = append(labels, classOf[r])
	}
	return labels, ok
}

// Decode turns classes back into text.
func Decode(labels []int) string {
	out := make([]rune, 0, len(labels))
	for _, l := range labels {
		if l > 0 && l <= len(charset) {
			out = append(out, charset[l-1])
		}
	}
	return string(out)
}
