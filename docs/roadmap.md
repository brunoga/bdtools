# Roadmap: a complete converter in Go

Decided on 2026-10-16. bdtools should do everything it does without
external programs, with every Go piece competitive with the tool it
replaces, and aiming to beat it. External tools and GPUs stay as options
for speed, never as requirements. Beyond that it should become a general
"swiss army knife" of video conversion.

Each piece is measured against the tool it replaces (size at equal
quality as a BD-rate, and CPU time) before it counts as done, and is
worked on one at a time in the order below.

## What was asked

- A pure-Go alternative to everything bdtools does today.
- Each one competitive with the tool it replaces, and aiming to be better.
- MP4 and MOV readers and writers.
- Side-by-side (SBS) and over-under (OU, top-and-bottom) 3D as inputs,
  and as outputs.
- The other features a general video converter needs (listed below as
  suggestions).

## Where each external tool is still needed

| Need | Today | Go replacement |
|---|---|---|
| H.264 output (other than MVC) | x264, or the Go encoder (wired, behind) | Tune the Go encoder: +9% BD-rate against x264 medium --tune psnr on five disc clips, at 4x its CPU; speed next |
| AV1 output | SVT-AV1 | An AV1 encoder in Go (the largest piece) |
| Dolby Vision with the enhancement layer kept or re-encoded | none | Done: the Go HEVC encoder codes both layers |
| Interlaced H.264 input | none | Done: field pictures and MBAFF decode in Go, deinterlaced |
| HEVC format range extensions input (4:2:2, 4:4:4) | ffmpeg | Range extension tools in the Go HEVC decoder (never on discs) |
| GPU encoders and decoders | System libraries | None needed: optional speed-ups |

Already pure Go: disc and playlist reading, demuxing and muxing (M2TS,
Matroska, MP4 for MV-HEVC), the H.264/MVC, HEVC (MV-HEVC included),
MPEG-2 and VC-1 decoders, the HEVC encoder (9% smaller than x265 medium
at equal PSNR), MV-HEVC encoding, the MVC encoder, Dolby Vision and HDR
metadata handling, 3D subtitles and subtitle burn-in.

## Formats

- **MP4 and MOV in:** a reader for 2D and 3D files (MV-HEVC included), as
  a source like a disc or Matroska file.
- **MP4 and MOV out:** for every layout and codec, not only MV-HEVC.
- **AVI in:** a reader for old archives (Xvid/DivX, DV, MJPEG), once
  their decoders exist (MPEG-4 Part 2, DV, MJPEG). No AVI output: the
  format cannot carry modern codecs, B-frames done right, or HDR.
- **SBS and OU in:** a 3D file whose frame holds both eyes, full or half,
  read as a stereo pair, so it converts to MVC, MV-HEVC or the other
  packing.
- **OU out:** over-under, full and half, beside side by side.

## Suggested features

Picture:
- Scaling (UHD to 1080p, and other sizes) and cropping, black bars found
  automatically.
- HDR to SDR tone mapping.
- Deinterlacing and inverse telecine (DVDs, interlaced extras).
- Every 3D form to every other: MVC, MV-HEVC, SBS and OU, full and half,
  3D to 2D, and a depth (convergence) adjustment.

Audio:
- Transcoding with downmix to AAC, AC-3 and Opus, in Go, from any input.
  AAC tracks are carried as they are; FLAC and Opus tracks (and text
  subtitles) are still dropped.
- Loudness normalisation.

Subtitles:
- Text formats in and out: SRT, ASS, WebVTT; text subtitles in MP4.
- OCR of Blu-ray (PGS) subtitles to text. Burn-in stays as the fallback.

Control:
- A target size or bitrate (two passes) as well as a quality.
- Trimming by time, and joining files.
- Device presets (Apple TV, Meta Quest, Plex, Kodi, Ugoos).
- A batch or watch-folder mode.

Measurement:
- PSNR, SSIM and VMAF in Go, so comparisons need no ffmpeg.

## Order

1. SBS and OU in and out.
2. MP4 and MOV reader; MP4 and MOV output for every layout.
3. Audio codecs in Go: AC-3 encoder, DTS decoder, then AAC.
4. The Go H.264 encoder made competitive with x264.
5. Interlaced H.264 decoding, deinterlacing.
6. Dolby Vision's two-layer form in the Go HEVC encoder.
7. Scaling, cropping, tone mapping.
8. Text subtitles, then PGS OCR.
9. Control features: size targets, trimming, presets, batch.
10. Quality metrics in Go.
11. An AV1 encoder in Go.

## Status

| Piece | State |
|---|---|
| Builtin H.264 for every layout | Wired and tested; +9% BD-rate against x264 --tune psnr (see item 4) |
| 1. SBS and OU | Done: `--layout ou`/`half-ou` out; side-by-side and over-under sources in (`--input-layout`, Matroska stereo mode) |
| 2. MP4 and MOV | Done: MP4/MOV sources (MV-HEVC and st3d 3D included), MP4/MOV output for every layout but MVC, burn-in for any MP4 |
| 3. Audio codecs in Go | Done: AC-3 encoder (coupling to do for 128 kb/s stereo), DTS core decoder (bit-exact), AAC passthrough and an AAC-LC encoder (short blocks to do) |
| 4. H.264 encoder | Done for now: +9% BD-rate against x264 --tune psnr (trellis, B-pyramid, lookahead); speed later |
| 5. Interlaced H.264 | Done: PAFF and MBAFF bit-exact on 79 of 80 JVT streams; 1080i sources decoded and deinterlaced in Go |
| 6. Dolby Vision two layers | Done: `--dv-fel keep`/`reencode` with `--encoder builtin` (profile 7 out, no NVENC or x265) |
| 7. Scaling, cropping, tone mapping | `--crop` (and `--crop auto`, the bars found at twelve places) and `--scale` done (2D, Lanczos, subtitles and DV L5 follow); next: tone mapping |
