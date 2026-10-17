# Reading PGS subtitles (internal/ocr)

`--subs-ocr` reads a Blu-ray's picture subtitles as text with a network
trained here, in Go, with no OCR tool.

## The pipeline

1. `internal/pgs` decodes the track's display sets into pictures, each
   shown from its composition to the next.
2. Each picture's ink (luma times alpha: the fill, not the outline) is cut
   into lines at blank rows; accents apart from their capitals join them,
   lines touching are cut apart.
3. Each line is scaled so that the track's line height (the upper third of
   its lines' heights: capitals and descenders) is 26 of 32 rows, one
   scale for the whole track, so a line of small letters stays small.
4. The network reads it: four 3x3 convolutions with pooling (32 rows to 2,
   the width to a quarter), a bidirectional LSTM (96 each way) over the
   columns, a linear layer to 196 classes (Latin script, accents,
   punctuation, the blank), CTC, greedy decoding. 268K weights, stored
   as half floats (535 KB, embedded).
5. A capital I read as l is put right where the word says so (GLORlA,
   l'm, lt's); a picture whose strokes lean (the shear that makes them
   most upright is past 0.1) is italic.

## Training

`tools/ocrtrain` (its own module: it needs golang.org/x/image for the
fonts) draws subtitle lines as discs draw them: a corpus line (SRT files'
text: 2 million lines, English and Portuguese mostly), in one of 28
installed fonts (Liberation, Nimbus, DejaVu, Noto, Lato, FreeSans:
regular, bold, italic), 30-65 pixels, filled white, yellow or grey,
outlined black 1-4 pixels, with a PGS palette's few levels, blur and noise
now and then; and, a quarter of the time, as a track for the hard of
hearing writes it (sounds in brackets, speakers in capitals, songs,
dialogue dashes, capitals throughout).

    ocrtrain -corpus DIR -steps 20000 -o model.bin          # from scratch
    ocrtrain -corpus DIR -resume model.bin -lr 5e-4 ...     # on from there
    ocrtrain -resume model.bin -eval DIR                     # discs' .sup + .srt

The shipped model: 9000 steps of 48 lines at 1e-3 (plain text), then
11000 at 5e-4 with the hard-of-hearing lines, about 45 minutes on 24
cores (AVX2 and FMA kernels for the products).

## How well

Seven discs' English PGS tracks and one Portuguese against SRT tracks
of the same films, each cue against the pictures within it, the texts
without what only a track for the hard of hearing has (sounds, speakers
at a line's start), dashes and line breaks: 1.16% of characters differ.
Much of what is left is the SRT's: its typos ("Herjourney"), its
wording ("..." for the disc's "--", another translation); the network's
own are mostly a capital at a word's start missed in one face, 0 read as
o among letters, and speakers' names in the middle of a line.

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

Italics, against the SRTs' `<i>`: 93% of italic pictures found (1323 of
1429), under 1% of upright ones taken for italic (where the SRT marks
italics at all).
