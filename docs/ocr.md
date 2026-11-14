# Reading PGS subtitles (internal/ocr)

`--subs-ocr` reads a Blu-ray's picture subtitles as text. It uses a
network trained here, in Go, with no OCR tool.

## The pipeline

1. `internal/pgs` decodes the track's display sets into pictures. Each
   picture is shown from its composition to the next.
2. Each picture's ink is cut into lines at blank rows. The ink is the
   luma times the alpha, the fill and not the outline. An accent that sits apart from its capital is joined to it, and lines
   that touch are cut apart.
3. Each line is scaled so that the track's line height is 26 of 32
   rows. The line height is taken from the upper third of the track's
   lines' heights, which are the lines with capitals and descenders.
   There is one scale for the whole track, so a line of small letters
   stays small.
4. The network that reads the line is four 3x3 convolutions with
   pooling, taking the 32 rows to 2 and the width to a quarter, then a
   bidirectional LSTM of 96 each way over the columns, then a linear
   layer to 196 classes. The classes are Latin script, accents,
   punctuation and the blank. The output is CTC, decoded greedily. It
   has 268K weights, stored as half floats, 535 KB, embedded.
5. A capital I read as l is put right where the word says so, as in
   GLORlA, l'm and lt's. A picture whose strokes lean is italic, meaning
   that the shear that makes them most upright is past 0.1.

## Training

`tools/ocrtrain` draws subtitle lines the way discs draw them. It is
its own module, because it needs golang.org/x/image for the fonts. Each
line is a corpus line, drawn in one of 28 installed fonts at 30-65
pixels, filled white, yellow or grey and outlined black 1-4 pixels, with
a PGS palette's few levels, and with blur and noise now and then. The
corpus is the text of SRT files, 2 million lines, mostly English and
Portuguese. The fonts are Liberation, Nimbus, DejaVu, Noto, Lato and
FreeSans, in regular, bold and italic. A quarter of the time the line is
written as a track for the hard of hearing writes it, with sounds in
brackets, speakers in capitals, songs, dialogue dashes and capitals
throughout.

    ocrtrain -corpus DIR -steps 20000 -o model.bin          # from scratch
    ocrtrain -corpus DIR -resume model.bin -lr 5e-4 ...     # on from there
    ocrtrain -resume model.bin -eval DIR                     # discs' .sup + .srt

The shipped model was trained in two stages: 9000 steps of 48 lines at
1e-3 on plain text, then 11000 at 5e-4 with the hard-of-hearing lines.
They took about 45 minutes on 24 cores, with AVX2 and FMA kernels for
the products.

## How well

We measured seven discs' English PGS tracks and one Portuguese against
SRT tracks of the same films. Each cue was compared with the pictures
within it. The texts were compared without dashes, line breaks and what
only a track for the hard of hearing has, which is sounds and speakers
at a line's start. 1.16% of characters differ. Much of what is left is the SRT's own: its typos, such as
"Herjourney", and its wording, such as "..." for the disc's "--", or
another translation. The network's own mistakes are mostly a
capital at a word's start missed in one face, 0 read as o among
letters, and speakers' names in the middle of a line.

| Film | Characters wrong |
|---|---|
| A Dog's Journey | 0.01% |
| Can You Ever Forgive Me? | 0.19% |
| Companion | 0.72% |
| Airplane! | 0.38% (its SRT's wording differs) |
| Airplane! (Portuguese) | 2.27% (another translation) |
| Blue Beetle | 2.53% (Spanish lines its SRT leaves out) |
| Another Round | 1.01% |
| Cold Storage | 2.41% (speakers mid-line) |

Italics were scored against the SRTs' `<i>`, where the SRT marks
italics at all. 93% of italic pictures were found, 1323 of 1429, and
under 1% of upright ones were taken for italic.
