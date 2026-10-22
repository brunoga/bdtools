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
| AV1 output | SVT-AV1, or the Go encoder (wired, behind) | The Go encoder: key frames 4.5% smaller than SVT-AV1 preset 4's, sequences +5.7% (two 120-picture 1080p disc clips; +2.8% at `--preset veryslow`), and slow; to tune and speed up |
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
  AAC tracks are carried as they are; FLAC and Opus tracks are still
  dropped.
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
12. 2D to 3D conversion: a video depth network in Go, views made from it, every 3D layout out (see [2d-to-3d.md](2d-to-3d.md)).

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
| 7. Scaling, cropping, tone mapping | `--crop` (and `--crop auto`, the bars found at twelve places) and `--scale` done (2D, Lanczos, subtitles and DV L5 follow); `--tonemap` done (BT.2390 on max(R,G,B), dynamic peaks from DV L1 and HDR10+) |
| 8. Text subtitles, PGS OCR | Done: SRT, ASS/SSA, WebVTT and tx3g carried (Matroska as they are, MP4 as tx3g), `--subs-file` adds files; `--subs-ocr` reads PGS as text (a CRNN trained here, internal/ocr) |
| 9. Control features | Trimming (`--start`, `--end`, `--duration`), device presets (`--for`), a target size (`--video-size`) and batches (`--input` a directory, `--watch`) done |
| 10. Quality metrics in Go | Done: VMAF (libvmaf's integer VIF, ADM, motion and its v0.6.1 models), PSNR and float SSIM, the same numbers as libvmaf's tool; `--compare` |
| 11. AV1 encoder | Done for now: `--encoder builtin --codec av1`, every stream decoded bit-exact by libdav1d in the tests. Key frames -4.5% BD-rate (PSNR-Y) against SVT-AV1 preset 4 `tune=1` on six 1080p disc frames; 120-picture clips +5.7% (Blade Runner 2049, Frozen II), +2.8% at `--preset veryslow` (loop restoration, OBMC, every compound mode, all seven references, temporal motion vectors, local warped motion, masked compound, inter-intra, a temporal filter and model, one tile a picture); about 0.46 s a 1080p picture on 24 cores (SVT-AV1 preset 4: 0.04 s). See [AV1: next steps](#av1-next-steps) |
| 12. 2D to 3D | Started: the plan in [2d-to-3d.md](2d-to-3d.md); `internal/nn` reads PyTorch checkpoints as they are and has the layers of Video Depth Anything (each checked against PyTorch); `internal/depth` runs Video Depth Anything Small's streaming form from its published checkpoint, the same depth as PyTorch's (within 1e-3 on film frames: 25 ms a 1080p frame on an RTX 5090 through `internal/cuda`, 1.3 s on the CPU); `--to-3d` converts a film to any 3D layout (internal/stereo: depth shaped into disparity, the eyes rendered with the nearer covering the farther, holes filled from the background). Measured against native and studio-converted 3D discs by `tools/stereobench` (a semi-global matcher in Go), the default depth now the discs'. To do: a faster GPU path (tensor cores, the eyes on the GPU), layered glass and reflections, filling across a shot, a learnt refinement, Blu-ray 3D authoring (a grading learnt from 27 films was tried: no gain) |

## AV1: next steps

What is left of the gap to SVT-AV1 preset 4 (+5.7% at the default speed)
and of its speed, by what each step is likely worth. The numbers are from
the two 120-picture 1080p clips; the small tunings around today's settings
(lambda, key frame and pyramid q, the temporal filter and model's
strengths, the motion search's starts) are all within 0.3% of them.

1. **A full temporal model.** Ours costs blocks by SATD on the source
   pictures and is worth 3.0%; SVT-AV1's own is worth 5.5% to it. libaom's
   form codes each block in the model (a transform, the frame's quantizer,
   a reconstruction the later frames predict from) and propagates rates and
   distortions, not SATD ratios. A cheap quantization-aware cost (a 4x4
   Hadamard dead-zone estimate) was 3.2% worse: its rate floor makes intra
   and inter costs alike, and any weaker propagation loses (the inter costs
   scaled by 1.4 or the references' quantization noise added: 0.8% to
   4.7% worse). Likely 1 to 2%.
2. **Frame-level parallelism.** One tile a picture (5.9% smaller than 2x2)
   costs 1.31 times the time: a 1080p frame is 30 by 17 superblocks, and
   its rows, two superblocks apart, keep about 8 cores busy where four
   tiles kept 17; more mini-GOPs at once do not help (the pyramid's first
   levels are one frame each). Starting a frame's rows as its references'
   rows are done (x264's frame threads) is the way, but a frame's CDFs
   (primary_ref_frame, 0.5%) and its temporal motion vectors need its
   references whole: they would have to come from earlier frames, or the
   references' rows be done in an order that gives them early.
3. **128x128 superblocks.** Fewer partition and skip symbols where the
   picture is flat, blocks up to 128x128: in libaom 0.5 to 1% on HD. It
   touches the superblock loops, the partition search, CDEF's and loop
   restoration's signalling and the wavefront (half the rows, fewer cores
   busy: see 2).
4. **The side information.** Coefficients are only a third of an inter
   frame's bits (motion vectors about 17%, compound modes and references
   about 10%, skip mode 3 to 6%): where the remaining gap is, by a breakdown
   of the symbols' bits. Kept for this: every tool earns its bits (no OBMC
   0.6% worse, no warped motion 0.6%, no masked compound 0.6%, no
   inter-intra 0.2%, no skip mode 1.2%).
5. **Smaller things.** A frame's interpolation filter chosen by its content
   (EIGHTTAP_SHARP throughout is 0.8% better on Blade Runner 2049 and 0.5%
   worse on Frozen II; EIGHTTAP_SMOOTH 7.1% worse); the remaining SIMD (the
   temporal filter's weights, the quantizer's scan); 4K, coded as one tile
   now too, measured.

