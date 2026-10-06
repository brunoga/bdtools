# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- `probe`: identifies a Blu-ray image or Matroska file from a pre-download
  sample — a torrent's first and last pieces, or a Matroska file's first
  32 KiB — reporting 2D or 3D and how (MVC, side by side, top-bottom), the
  feature's duration and its tracks, from what the container states. A
  sample missing what is needed is a `*MissingDataError` naming the bytes,
  never a panic or a guess. It does not reach the decoder.
- `bdmv.OpenImage` reads a disc image from an `io.ReaderAt` (a partial one
  included), and every disc has `Close`.

### Fixed

- mvctools left the disc image of a bare stream file's language lookup
  open, and a disc open when choosing its title failed.
- A Matroska prefix that ends inside an element after the tracks (a cover
  image attachment) reads as its header instead of failing.

## [0.3.1] - 2026-10-06

### Fixed

- A picture whose start code fell in the last bytes of one of the muxer's
  1 MiB reads was merged into the picture before it and lost, breaking the
  pictures that referenced it until the next keyframe (one picture in
  Raya's 4.3 GB stream; any H.264 or HEVC conversion could be hit). The
  muxer now keeps the rest of a read as it was read.

## [0.3.0] - 2026-10-06

### Added

- `--codec av1`: AV1 output from NVENC (RTX 40 and later) and VAAPI (Intel
  Arc / Core Ultra, recent AMD) in process or through ffmpeg, and from
  SVT-AV1 in software (`SvtAv1EncApp`, else ffmpeg's `libsvtav1`). The
  Matroska muxer writes AV1 (`V_AV1` with an `av1C`) from a bare OBU stream
  or IVF. `--crf` keeps its 0-51 scale, mapped onto AV1's quantiser index
  so a number gives about the quality it gives in HEVC (measured on NVENC:
  AV1 11-15% smaller at equal PSNR). VideoToolbox has no AV1 encoder.
- `--remux` joins a title made of several clips again, now in process: each
  clip is cut to its play item's window (from the GOP that opens at or
  before IN, while pictures decode before OUT) and moved onto one
  continuous timeline — PTS, DTS, PCR and arrival times — so the result is
  a single stream that plays and seeks like one clip.
- `--encoder mediafoundation` (`mf`): Intel and AMD GPUs on Windows encode in
  process through the Media Foundation encoder their driver installs, with
  no ffmpeg; `auto` tries it after NVENC. It is tested in CI with
  Microsoft's software encoder, not yet on a GPU.
- `--name-details` names the output after what it is: layout, resolution
  per eye, codec and quality setting, encoder and main audio track, e.g.
  "Moana (2016) 3D FSBS 1080p HEVC QP20 NVENC TrueHD-Atmos 7.1.mkv".
- `--playlist` picks the title of a disc image or folder by its playlist,
  instead of the one the playlists suggest.
- After the mux, a timeline line gives where the picture and each audio
  track start and end, with a warning when the picture's length is not the
  source's.
- With `--keep-fallback`, a Matroska source's lossy track that stands in for
  TrueHD's core (an "AC3 compatibility" track) is kept beside it, as the core
  is on a disc.

### Changed

- The Docker image is Debian instead of Alpine, with the VAAPI drivers for
  Intel and AMD, and asks the NVIDIA container toolkit for the video
  capability: on Alpine, NVIDIA's glibc libraries could not load, so no GPU
  encoder worked in the container.
- `--check`, and the check before a run, try a GPU encoder reached through
  ffmpeg with a short encode: an ffmpeg without it is reported as unusable.
- GPU encoders make a keyframe every 250 frames, as x264 and x265 do,
  instead of every 2 s: 7% smaller at the same quality.
- Tracks that hold nothing in the part played are reported in one line.

### Fixed

- The AC-3 core of a TrueHD track starts at its own timestamp, not the
  TrueHD frame's beside it (up to 16 ms off for its first second).
- NVENC on Windows on Arm passed its codec GUIDs by reference; that
  platform passes a 16-byte struct in registers.

## [0.2.0] - 2026-10-06

### Added

- Progress lines give the total number of frames up front (from the
  playlist's or file's length and the stream's frame rate), then how far
  along the conversion is and an estimate of the time left.
- `mvctools` reads Matroska remuxes of 3D discs (MakeMKV, mkvmerge) in
  process, like a disc: the video track's MVC access units go straight to
  the decoder, and the audio and subtitle tracks keep their names and forced
  flags in the output.
- Both commands print a banner with their name and version in their help,
  and `mvcdec` has `-version`.

### Fixed

- Audio and picture stay in step when a disc starts one after the other.
  Every track was placed at zero, so a soundtrack the disc starts late
  played early for the whole film (0.3 s on Moana, 0.14 s on The Lion King)
  and so did a picture that starts late (almost a second on Raya and both
  Toy Story discs). Tracks now start, and continue across any gap, where
  the source's timestamps put them. The tsMuxeR and mkvmerge pipeline had
  the same fault.

### Removed

- tsMuxeR and mkvmerge are no longer used: the built-in demuxer and muxer do
  everything they did. `--demuxer` and `--muxer` are gone, and so are both
  tools from the Docker image.
- MP4, MOV and VOB sources are no longer accepted (they went to tsMuxeR):
  MVC travels on Blu-rays and in Matroska remuxes of them.
- A title made of several clips can no longer be remuxed (that, too, was
  tsMuxeR's); it still converts.

## [0.1.0] - 2026-10-05

### Added

- `mvc`: a pure-Go H.264/MVC (stereo high profile) decoder producing both
  views of every access unit, with AVX2 assembly kernels on amd64 and Go
  fallbacks everywhere else; bit-exact against the JVT conformance corpus
  and edge264 on real Blu-ray 3D content.
- `mvcdec`: decodes an MVC stream (m2ts, Annex B, or a demuxed pair) to raw
  YUV or Y4M, side by side or top-and-bottom.
- `mvctools`: converts a Blu-ray 3D source to a side-by-side MKV, or remuxes
  it; moved here from pipeliner (where it was `mvc2sbs`) and now decodes with
  the built-in decoder instead of an external edge264, so `--swap-lr` no
  longer needs ffmpeg and the frame rate is taken from the stream.
- `mvctools` reads Blu-ray sources with its own demuxer by default: a disc
  image or folder is read once and in place — no extraction, no demuxed copy
  of either view — and a `--remux` copies packets directly, with no tools.
  The audio and subtitle files it writes are byte-identical to tsMuxeR's.
  `--demuxer tsmuxer` keeps the old pipeline; Matroska, MP4 and VOB sources
  use it automatically.
- Title selection reads the playlists and counts each stretch of a clip once,
  so a looped menu or decoy playlist that runs longer than the feature is no
  longer chosen; between copies of the feature, the one with chapters wins.
- Chapters from the playlist are carried into the MKV.
- `mvctools` writes the MKV with its own Matroska muxer by default, so a
  conversion needs no tool but the encoder. Its output matches mkvmerge's
  packet for packet (checked on a whole film); `--muxer mkvmerge` keeps
  mkvmerge.
- `mvctools` drives NVENC, VAAPI and VideoToolbox in process through their
  system libraries, loaded at run time (no cgo, no ffmpeg): the frames go
  from the decoder straight into the encoder's buffers, half-SBS squeezed on
  the way. `auto` tries them before ffmpeg; `--gpu-api ffmpeg` keeps ffmpeg.
  With NVENC a minute of film converts in 7.0 s instead of 7.9 s (full SBS)
  and 5.0 s instead of 7.5 s (half SBS).

### Fixed

- `--crf` with VideoToolbox no longer runs backwards: it was passed as
  ffmpeg's `-q:v`, where higher is better, so the default 18 asked for low
  quality. It now maps onto VideoToolbox's scale with lower better.
- Audio and subtitles before the playlist's IN time (or a loose stream's
  first picture) and after its OUT time are no longer muxed. tsMuxeR's demux
  keeps them, which put the sound ahead of the picture on discs whose audio
  starts early (1.16 s on The Wild Robot).

[Unreleased]: https://github.com/brunoga/mvc/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/brunoga/mvc/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/brunoga/mvc/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/brunoga/mvc/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/brunoga/mvc/releases/tag/v0.1.0
