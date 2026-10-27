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
| 7. Scaling, cropping, tone mapping | `--crop` (and `--crop auto`, the bars found at twelve places) and `--scale` done (2D, Lanczos, subtitles and DV L5 follow); `--tonemap` done (BT.2390 on max(R,G,B), dynamic peaks from DV L1 and HDR10+); both on an NVIDIA GPU when there is one, bit-exact with the CPU |
| 8. Text subtitles, PGS OCR | Done: SRT, ASS/SSA, WebVTT and tx3g carried (Matroska as they are, MP4 as tx3g), `--subs-file` adds files; `--subs-ocr` reads PGS as text (a CRNN trained here, internal/ocr) |
| 9. Control features | Trimming (`--start`, `--end`, `--duration`), device presets (`--for`), a target size (`--video-size`) and batches (`--input` a directory, `--watch`) done |
| 10. Quality metrics in Go | Done: VMAF (libvmaf's integer VIF, ADM, motion and its v0.6.1 models), PSNR and float SSIM, the same numbers as libvmaf's tool; `--compare` |
| 11. AV1 encoder | Done for now: `--encoder builtin --codec av1`, every stream decoded bit-exact by libdav1d in the tests. Key frames -4.5% BD-rate (PSNR-Y) against SVT-AV1 preset 4 `tune=1` on six 1080p disc frames; 120-picture clips +5.7% (Blade Runner 2049, Frozen II), +2.8% at `--preset veryslow` (loop restoration, OBMC, every compound mode, all seven references, temporal motion vectors, local warped motion, masked compound, inter-intra, a temporal filter and model, one tile a picture); about 0.46 s a 1080p picture on 24 cores (SVT-AV1 preset 4: 0.04 s). See [AV1: next steps](#av1-next-steps) |
| 12. 2D to 3D | Started: the plan in [2d-to-3d.md](2d-to-3d.md); `internal/nn` reads PyTorch checkpoints as they are and has the layers of Video Depth Anything (each checked against PyTorch); `internal/depth` runs Video Depth Anything Small's streaming form from its published checkpoint, the same depth as PyTorch's (within 1e-3 on film frames: 25 ms a 1080p frame on an RTX 5090 through `internal/cuda`, 1.3 s on the CPU); `--to-3d` converts a film to any 3D layout (internal/stereo: depth shaped into disparity, the eyes rendered with the nearer covering the farther, holes filled from the background). Measured against native and studio-converted 3D discs by `tools/stereobench` (a semi-global matcher in Go), the default depth now the discs'. A faster GPU path is done (tensor cores, attention in one pass, the networks' inputs and the eyes on the GPU: about 100 pictures a second), and Blu-ray 3D images (`--output film.iso`). To do: layered glass and reflections, filling across a shot, the subtitles' 3D on a disc (a grading learnt from 27 films was tried: no gain; a learnt depth refinement, built in, gains 0.5 to 1 dB) |

## AV1: next steps

`tools/av1bench` measures the encoder against SVT-AV1 (through ffmpeg's
libsvtav1) on six 120-picture 1080p 8-bit clips (Blade Runner 2049,
Frozen II, Godzilla vs. Kong, Children of Men, Free Solo, Incredibles 2,
the HDR ones tone mapped): BD-rate by PSNR-Y, PSNR-YUV, SSIM and VMAF
against SVT-AV1 2.3.0 preset 4, each stream decoded by libdav1d. The
baseline (24 cores):

| config | PSNR-Y | PSNR-YUV | SSIM | VMAF | fps | CPU s a picture |
|---|---|---|---|---|---|---|
| SVT-AV1 preset 2 | -9.2% | -10.4% | -9.3% | -7.0% | 10.3 | 0.85 |
| SVT-AV1 preset 4 | 0 | 0 | 0 | 0 | 24.5 | 0.30 |
| SVT-AV1 preset 6 | +22.6% | +22.5% | +15.5% | +26.1% | 58.6 | 0.10 |
| SVT-AV1 preset 8 | +42.0% | +43.9% | +32.2% | +43.9% | 78.3 | 0.06 |
| ours, speed 0 | +7.0% | +7.7% | +11.4% | +6.7% | 0.99 | 15.7 |
| ours, speed 1 | +9.7% | +10.4% | +13.8% | +8.8% | 1.58 | 10.1 |
| ours, speed 2 | +10.9% | +11.6% | +15.0% | +10.8% | 2.05 | 7.6 |

So 7 to 11% larger than preset 4 and 25 to 50 times its CPU time: speed
is the larger gap. Since then (the extended partitions estimated from the
halves and quarters, the motion search cached for a superblock's
partitions, the filters' and the temporal model's searches cut short from
speed 1):

| config | PSNR-Y | PSNR-YUV | SSIM | VMAF | fps | CPU s a picture |
|---|---|---|---|---|---|---|
| ours, speed 0 | +7.3% | +8.0% | +11.7% | +7.0% | 1.31 | 12.6 |
| ours, speed 1 | +9.8% | +10.6% | +13.5% | +8.7% | 2.19 | 7.5 |
| ours, speed 2 | +11.3% | +12.1% | +15.2% | +10.8% | 2.84 | 5.6 |

Then the inter candidates costed with DCT_DCT alone, the best's
transforms searched after (libaom's way):

| config | PSNR-Y | PSNR-YUV | SSIM | VMAF | fps | CPU s a picture |
|---|---|---|---|---|---|---|
| ours, speed 0 | +7.8% | +8.4% | +12.2% | +7.1% | 1.72 | 9.9 |
| ours, speed 1 | +10.4% | +11.0% | +14.1% | +9.5% | 2.58 | 6.4 |
| ours, speed 2 | +11.6% | +12.2% | +15.3% | +11.4% | 3.00 | 5.3 |

The transforms in AVX2 then make the same streams 1.10 times faster
(speed 2: 3.28 pictures a second, 4.8 CPU seconds each). Tried and dropped
since: intra in inter pictures with fewer modes, depths or type searches
(+0.6 to +1.4% for 1.08 to 1.14 times), no 4x4 blocks or 8x8 whole only
in inter pictures (+0.1% and +0.3% for 1.02 and 1.08 times, the same
trade as a speed step), no 4:1 partitions (+1.1% for 1.14 times).
The inter-intra masks made once and blended in AVX2, and the distortions
summed in AVX2, take it to 4.65 CPU seconds a picture, the same streams.

The cores were then idle a third of the time: every 32-picture mini-GOP's
frames below its anchor could use any earlier frame still in a slot, among
them the previous mini-GOP's deepest, coded last, so each mini-GOP waited
for most of the one before (6 to 9 of the 24 cores busy for 3 to 4 seconds
in every 8). Limiting the earlier mini-GOPs' frames to depth 2 for every
frame (as for the anchors already), with four mini-GOPs coded at once,
keeps 23 to 24 cores busy past the stream's first mini-GOP, and the coded
frames and their sources are let go of as soon as they are coded (1080p:
4.0 GB at peak, from 4.7):

| config | PSNR-Y | PSNR-YUV | SSIM | VMAF | fps | CPU s a picture |
|---|---|---|---|---|---|---|
| ours, speed 0 | +7.9% | +8.5% | +12.4% | +7.2% | 2.25 | 8.9 |
| ours, speed 2 | +11.5% | +12.1% | +15.3% | +11.5% | 3.87 | 4.7 |

(Depth 3 was 0.2% smaller at 1.10 times, depth 4 0.1% at 1.09 times,
within the clips' noise; 2 and 3 mini-GOPs at once 1.10 and 1.15 times.)

Tried and dropped: pruning the horizontal and vertical partitions by the
split's cost (+0.6%, 1.11 times), a first pass deciding the partitions
with a cheaper search (+0.9% for no gain, or +12% for 1.49 times), intra
in inter pictures cut where inter's estimate is lower (+3.3%, 1.06
times), a skip decided from the prediction error (no threshold separates
it: the boundary spans four octaves and moves with q), the candidates'
estimates with one filter (+0.3%, slower). What is left, by what each step is likely worth (the
size numbers below are from the earlier two-clip bench). The numbers are from
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
2. **Frame-level parallelism.** Done as far as whole frames go (above):
   past the first mini-GOP the cores are busy. What is left is the
   stream's start (a key frame, then each pyramid level one frame) and
   one tile a picture (5.9% smaller than 2x2): a 1080p frame's rows, two
   superblocks apart, keep about 8 cores busy where four tiles kept 17.
   Starting a frame's rows as its references' rows are done (x264's frame
   threads) needs a frame's CDFs (primary_ref_frame, 0.5%) and temporal
   motion vectors from earlier frames than its references.
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

