# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.8.0] - 2026-10-09

### Added

- Decoding through ffmpeg where no GPU decoder works: 2D HEVC (Ultra HD),
  VC-1 and MPEG-2 sources, with their HDR10, HDR10+ and Dolby Vision, now
  convert on every platform with ffmpeg 6.1 or later. Each picture keeps
  its own timestamp; the pictures are the ones NVDEC gives.
- An MPEG-2 decoder in Go, with a Go deinterlacer (yadif's algorithm): MPEG-2
  Blu-rays and DVDs convert with no GPU and no ffmpeg. Within the
  transform's tolerance of ffmpeg's decode on the ISO conformance streams.
- A VC-1 decoder in Go (Advanced Profile: progressive, interlaced frame and
  field pictures): VC-1 Blu-rays convert with no GPU and no ffmpeg, each
  picture byte-identical to ffmpeg's decode (the SMPTE conformance streams,
  and minutes of a real disc). About 110 fps for 1080p.
- An HEVC decoder in Go (Main and Main 10): 2D Ultra HD Blu-rays, with
  HDR10, HDR10+ and Dolby Vision (FEL composed, or kept as profile 7
  layers), convert with no GPU and no ffmpeg. Byte-identical to ffmpeg's
  decode on the 152 JCT-VC conformance streams of those profiles. Pictures
  decode several at a time (frame threading, each CTB row loop filtered as
  it is decoded), and within a picture its slices and wavefront rows in
  parallel, with AVX2 kernels for motion compensation: about 250 fps for a
  disc's 4K stream on 24 threads, the speed of ffmpeg's decoder. Streams of
  the format range extensions still go to ffmpeg.
- VAAPI decoding of HEVC on Linux (Intel and AMD GPUs), with the HEVC
  parser here driving it: byte-identical to ffmpeg on the 152 conformance
  streams on an Intel GPU, 95 fps for a disc's 4K stream with about one CPU
  core. A 4K Dolby Vision FEL conversion decoded, composed and encoded on
  an Intel iGPU runs at 47 fps.
- VAAPI decoding of MPEG-2, the MPEG-2 parser here driving it, its interlaced
  frames deinterlaced here as with the decoder in Go: frame and field
  pictures within the inverse transform's tolerance of the decoder in Go
  (71 to 80 dB on the conformance streams).
- Matroska remuxes of MPEG-2 discs (the `V_MPEG2` track).
- VAAPI decoding of 2D H.264 (progressive), the H.264 decoder here parsing:
  byte-identical to it on 65 of the 66 conformance streams it decodes, on
  an Intel GPU.
- Interlaced 2D H.264 (1080i discs: field pictures, MBAFF) decodes through
  ffmpeg where neither NVDEC nor VideoToolbox does; the decoder here takes
  progressive H.264 only, and before this such a source decoded to no
  frames.
- Matroska remuxes of VC-1 Blu-rays: the `V_MS/VFW/FOURCC` (WVC1) track, and
  PCM audio in `A_MS/ACM`.
- VideoToolbox decoding on macOS (H.264 and HEVC, 10-bit included),
  checked picture by picture against ffmpeg's decode on CI's Macs.
- NVDEC on Windows (its structures as Windows lays them out, generated
  from NVIDIA's headers; not yet tried on Windows hardware).
- `--dv-fel keep` and `reencode` (profile 7 out) with x265 (`--encoder
  software`), where there is no NVENC: x265 runs in process for both layers,
  set up to code them alike.

### Changed

- The READMEs describe the tools as they are now (2D, Ultra HD, HDR and
  Dolby Vision, the decoders and which takes what), and `bdtools`' starts
  with examples of common tasks; `bdtools --help` shows a few.

### Fixed

- NVDEC's frame rate is stated reduced (24000/1001, not 96000/4004).

## [0.7.0] - 2026-10-08

### Added

- 2D Blu-rays, Ultra HD included, and 2D Matroska files: converted as a 2D
  film (no stereo mode), with the same codec, quality, encoder and track
  choices, and resuming. `--2d` converts a 3D source's base view alone. A
  disc with no 3D title has its 2D feature converted.
- NVDEC decoding in process (no cgo; Linux for now): H.264 and HEVC
  (10-bit included) bit-identical to ffmpeg's decoders, MPEG-2 within its
  transform's tolerance, VC-1 untried; 580 fps for 4K 10-bit HEVC.
  `--decoder auto|gpu|cpu`; without a GPU decoder H.264 decodes here.
- `--remux` into a `.mkv` for 2D sources (a 3D disc's base view with
  `--2d`, or a Matroska file): the H.264 or HEVC video untouched, with the
  chosen tracks and chapters, from the random access point at or before
  IN. `--name-details` names it `Remux`.
- A stream that states no frame rate takes the container's (a playlist's,
  or a Matroska track's frame duration or frame spacing).
- HDR10 and HDR10+ are kept through a conversion: the colour signalling
  (BT.2020, PQ/HLG) stated by every encoder (NVENC, VAAPI, VideoToolbox,
  x265, SVT-AV1, ffmpeg) and in Matroska's Colour element; the mastering
  display and content light level on every keyframe; HDR10+'s dynamic
  metadata on every frame it was on. HEVC as SEI, AV1 as metadata OBUs.
- Dolby Vision profile 7 (an Ultra HD disc's, FEL or MEL) is kept by a
  `--remux` into Matroska: the enhancement layer (PID 0x1015) and RPU go
  into the HEVC track as players expect them, with the Dolby Vision
  configuration record. A Matroska source keeps its Dolby Vision in a
  remux, and a `.m2ts` remux keeps the enhancement layer's stream.
- Dolby Vision through a conversion to HEVC, as profile 8.1: each frame
  carries its picture's RPU, converted from profile 7 as dovi_tool's mode
  2 does (byte-identical on every RPU of the test clips), with the profile
  8.1 configuration record. The RPU reader and writer are in Go.
- A Dolby Vision full enhancement layer (FEL) is composed into the picture
  in a conversion: the base layer mapped by each RPU and corrected by the
  enhancement layer (decoded on a second NVDEC session, upsampled), in Go
  with AVX2 kernels, run beside decoding and encoding (a few percent
  slower than the base layer alone). `--dv-fel drop` keeps the base layer
  as it is.
- Or the full enhancement layer is kept as a layer: profile 7 out, the
  source's RPUs unchanged, the enhancement layer on a second NVENC session
  coded in step with the base layer. `--dv-fel keep` rebuilds it for the
  encoded base layer (decoded again as it is written), so the composition
  makes up for the base layer's encoding error; `--dv-fel reencode`
  re-encodes the source's. `--dv-el-crf` sets its quality.

### Changed

- `--bit-depth` defaults to the source's: 10 for a 10-bit source when the
  codec can, 8 otherwise (Blu-ray 3D included, so 3D output is unchanged).

### Known limits

- Decoding a 2D source other than H.264 (Ultra HD HEVC, VC-1, MPEG-2)
  needs NVDEC, so Linux with an NVIDIA GPU; so do the Dolby Vision and HDR
  conversions of such sources. Remuxes and 3D conversions work everywhere.
  See "What works where" in the README.

## [0.6.0] - 2026-10-08

### Changed

- The project is now **bdtools** (github.com/brunoga/bdtools), the first
  step towards 2D and Ultra HD Blu-ray. The command `mvctools` is now
  `bdtools`, and its image `ghcr.io/brunoga/bdtools`. The module path is
  `github.com/brunoga/bdtools`; the decoder moved to its own package,
  `github.com/brunoga/bdtools/mvc` (still package `mvc`), and `probe` and
  `m2ts` are `github.com/brunoga/bdtools/probe` and `.../m2ts`. An
  interrupted conversion's work directory is now `.<output>.bdtools`, so a
  run interrupted under the old name starts over.

## [0.5.3] - 2026-10-07

### Changed

- With `--subs-3d both` the first flat subtitle track is marked default.
  Left unmarked, Kodi could take a 3D track, which in frame-packed playback
  it draws squeezed into one eye and looks jagged. `mkvpropedit FILE --edit
  track:s1 --set flag-default=1 --edit track:s2 --set flag-default=0 ...`
  sets the flags on a file made before.

## [0.5.2] - 2026-10-07

### Fixed

- Full side-by-side output now states its display size the way mkvmerge
  does (3840x2160 for a 3840x1080 pair). Without it, players built on
  ffmpeg, Kodi and CoreELEC among them, took each eye for 3840 wide: the
  file showed as "3D UHD", at 64:9 in 2D, and CoreELEC chose a 4K output
  mode, which cannot be frame packed. `mkvpropedit FILE --edit track:v1
  --set display-width=3840 --set display-height=2160` fixes a file made
  before, in place.

## [0.5.1] - 2026-10-07

### Fixed

- A PGS segment longer than its PES packet holds (a full-screen subtitle
  graphic, such as the end credits Avatar's Blu-ray draws as subtitles)
  was written cut short, which made the mux fail with "not a .sup file" at
  the end of a film. It is now completed from the next packet.

## [0.5.0] - 2026-10-06

### Added

- `probe` reports video bit depth: on a disc, the depth Blu-ray allows the
  coding; in Matroska, the Colour element's or the codec configuration's.
- `probe` reports the MVC view's frame size, from the dependent clip's clip
  info, which lists the MVC stream in its 3D extension.
- `--bit-depth 10`: HEVC Main 10 or 10-bit AV1, in process on NVENC, VAAPI
  and VideoToolbox, through ffmpeg, and with x265 and SVT-AV1. Less banding,
  and about 4% smaller at equal quality on NVENC HEVC, at no cost in speed.
  `--name-details` adds `10bit`.
- `mvc.Y4MWriter.Depth` writes 10-bit Y4M (C420p10).
- An interrupted conversion resumes: the video is encoded in segments of
  2,500 frames into a work directory named after the output, and the same
  command run again encodes only what no finished segment holds, giving
  the same file an uninterrupted run would. Other settings or another
  source start over, as does `--restart`. Ctrl-C now stops a conversion
  cleanly.
- `--subs-3d on|both`: subtitles drawn into both halves of the side-by-side
  frame at the depth the disc gives them (the offset metadata in the MVC
  stream, the sequence the playlist assigns each track), for players that
  show the frame as it is; `both` keeps the flat track too. Squeezed with the
  picture for `--layout half`.

### Changed

- The work directory is `.<output name>.mvctools` beside the output (or
  under `--temp`) rather than a random `mvctools-*` one.

## [0.4.1] - 2026-10-06

### Fixed

- The module declared `go 1.27` without needing it, which forced every
  module importing `probe` onto Go 1.27. It now declares `go 1.26.2`.

## [0.4.0] - 2026-10-06

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

[Unreleased]: https://github.com/brunoga/bdtools/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/brunoga/bdtools/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/brunoga/bdtools/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/brunoga/bdtools/compare/v0.5.3...v0.6.0
[0.5.3]: https://github.com/brunoga/bdtools/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/brunoga/bdtools/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/brunoga/bdtools/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/brunoga/bdtools/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/brunoga/bdtools/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/brunoga/bdtools/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/brunoga/bdtools/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/brunoga/bdtools/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/brunoga/bdtools/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/brunoga/bdtools/releases/tag/v0.1.0
