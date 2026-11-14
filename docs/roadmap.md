# Roadmap: a complete converter in Go

We decided this on 2026-10-16. bdtools should do everything it does today
without external programs. Every Go piece should be competitive with the
tool it replaces, and should aim to beat it. External tools and GPUs stay
as options for speed. They are never requirements. Beyond that, bdtools
should become a general "swiss army knife" of video conversion.

A piece counts as done once it has been measured against the tool it
replaces. We measure the size at equal quality, as a BD-rate, and the CPU
time. The pieces are worked on one at a time, in the order below.

## What was asked

- A pure-Go alternative to everything bdtools does today.
- Each one competitive with the tool it replaces, and aiming to be better.
- MP4 and MOV readers and writers.
- Side-by-side (SBS) and over-under (OU, top-and-bottom) 3D as inputs,
  and as outputs.
- The other features a general video converter needs. They are listed
  below as suggestions.

## Where each external tool is still needed

| Need | Today | Go replacement |
|---|---|---|
| H.264 output (other than MVC) | x264, or the Go encoder, which is wired but behind | Tune the Go encoder. It is +9% BD-rate against x264 medium --tune psnr on five disc clips, at 4x its CPU. Speed is next |
| AV1 output | SVT-AV1, or the Go encoder (`--codec av1 --encoder builtin`) | The Go encoder at speed 0 is smaller than SVT-AV1 preset 4 by PSNR-YUV (5%) and SSIM at 1080p, and by every metric at 2160p. Speed 2 (`--preset fast`) is at PSNR-YUV parity at 5.8 times preset 4's CPU, and 8% behind by VMAF. `--tune vmaf` leans the other way. Its `--crf` is on SVT-AV1's scale, and its defaults are a quarter smaller than the HEVC ones at the same quality. It was soaked on 20 minutes of a 1080p disc and a 2160p HDR excerpt. See [AV1: next steps](#av1-next-steps) |
| Dolby Vision with the enhancement layer kept or re-encoded | none | Done. The Go HEVC encoder codes both layers |
| Interlaced H.264 input | none | Done. Field pictures and MBAFF are decoded in Go, then deinterlaced |
| HEVC format range extensions input (4:2:2, 4:4:4) | ffmpeg | Range extension tools in the Go HEVC decoder. They never appear on discs |
| GPU encoders and decoders | System libraries | None needed. They are optional speed-ups |

Much of bdtools is already pure Go. Disc and playlist reading is, and so
are demuxing and muxing for M2TS, Matroska and MP4 for MV-HEVC. The
decoders for H.264 and MVC, HEVC with MV-HEVC, MPEG-2 and VC-1 are pure
Go. So are the HEVC encoder, which is 9% smaller than x265 medium at
equal PSNR, the MV-HEVC encoding and the MVC encoder. Dolby Vision and
HDR metadata handling, 3D subtitles and subtitle burn-in are pure Go too.

## Formats

- **MP4 and MOV in:** a reader for 2D and 3D files, MV-HEVC included. It
  is a source like a disc or a Matroska file.
- **MP4 and MOV out:** output for every layout and codec, not only
  MV-HEVC.
- **AVI in:** a reader for old archives (Xvid/DivX, DV, MJPEG). It waits
  for their decoders, which are MPEG-4 Part 2, DV and MJPEG. There is no
  AVI output. The format cannot carry modern codecs, B-frames done right,
  or HDR.
- **SBS and OU in:** a 3D file whose frame holds both eyes, full or half,
  read as a stereo pair. It then converts to MVC, MV-HEVC or the other
  packing.
- **OU out:** over-under, full and half, beside side by side.

## Suggested features

Picture:
- Scaling, from UHD to 1080p and to other sizes, and cropping. The black
  bars are found automatically.
- HDR to SDR tone mapping.
- Deinterlacing and inverse telecine, for DVDs and interlaced extras.
- Conversion between every 3D form, which means MVC, MV-HEVC, SBS and OU,
  full and half. Also 3D to 2D, and a depth (convergence) adjustment.

Audio:
- Transcoding with downmix to AAC, AC-3 and Opus, in Go, from any input.
  AAC tracks are carried as they are. FLAC and Opus tracks are still
  dropped.
- Loudness normalisation.

Subtitles:
- Text formats in and out, which means SRT, ASS and WebVTT, and text
  subtitles in MP4.
- OCR of Blu-ray (PGS) subtitles to text. Burn-in stays as the fallback.

Control:
- A target size or bitrate, in two passes, as well as a quality.
- Trimming by time, and joining files.
- Device presets for the Apple TV, Meta Quest, Plex, Kodi and Ugoos.
- A batch or watch-folder mode.

Measurement:
- PSNR, SSIM and VMAF in Go, so that comparisons need no ffmpeg.

## Order

1. SBS and OU in and out.
2. MP4 and MOV reader; MP4 and MOV output for every layout.
3. Audio codecs in Go, which means an AC-3 encoder, a DTS decoder, then AAC.
4. The Go H.264 encoder made competitive with x264.
5. Interlaced H.264 decoding, deinterlacing.
6. Dolby Vision's two-layer form in the Go HEVC encoder.
7. Scaling, cropping, tone mapping.
8. Text subtitles, then PGS OCR.
9. Control features, which means size targets, trimming, presets and batch.
10. Quality metrics in Go.
11. An AV1 encoder in Go.
12. 2D to 3D conversion. A video depth network in Go, the views made from it, and every 3D layout out. See [2d-to-3d.md](2d-to-3d.md).

## Status

| Piece | State |
|---|---|
| Builtin H.264 for every layout | Wired and tested. +9% BD-rate against x264 --tune psnr, see item 4 |
| 1. SBS and OU | Done. `--layout ou` and `half-ou` out. Side-by-side and over-under sources in, through `--input-layout` or the Matroska stereo mode |
| 2. MP4 and MOV | Done. MP4 and MOV sources, MV-HEVC and st3d 3D included. MP4 and MOV output for every layout but MVC. Burn-in for any MP4 |
| 3. Audio codecs in Go | Done. An AC-3 encoder, with coupling still to do for 128 kb/s stereo. A bit-exact DTS core decoder. AAC passthrough and an AAC-LC encoder, with short blocks still to do |
| 4. H.264 encoder | Done for now. +9% BD-rate against x264 --tune psnr, with trellis, B-pyramid and lookahead. Speed comes later |
| 5. Interlaced H.264 | Done. PAFF and MBAFF are bit-exact on 79 of 80 JVT streams. 1080i sources are decoded and deinterlaced in Go |
| 6. Dolby Vision two layers | Done. `--dv-fel keep` or `reencode` with `--encoder builtin`. Profile 7 out, with no NVENC or x265 |
| 7. Scaling, cropping, tone mapping | `--crop` and `--scale` are done. `--crop auto` finds the bars at twelve places. Scaling is Lanczos, for 2D, and the subtitles and DV L5 follow it. `--tonemap` is done, as BT.2390 on max(R,G,B) with dynamic peaks from DV L1 and HDR10+. Both run on an NVIDIA GPU when there is one, bit-exact with the CPU |
| 8. Text subtitles, PGS OCR | Done. SRT, ASS/SSA, WebVTT and tx3g are carried, in Matroska as they are and in MP4 as tx3g. `--subs-file` adds files. `--subs-ocr` reads PGS as text, with a CRNN trained here in internal/ocr |
| 9. Control features | Done. Trimming with `--start`, `--end` and `--duration`. Device presets with `--for`. A target size with `--video-size`. Batches, with `--input` a directory or with `--watch` |
| 10. Quality metrics in Go | Done. VMAF with libvmaf's integer VIF, ADM and motion and its v0.6.1 models, plus PSNR and float SSIM. They give the same numbers as libvmaf's tool. The flag is `--compare` |
| 11. AV1 encoder | Done for now. `--encoder builtin --codec av1`, and every stream is decoded bit-exact by libdav1d in the tests. Key frames are -4.5% BD-rate (PSNR-Y) against SVT-AV1 preset 4 `tune=1` on six 1080p disc frames. 120-picture clips of Blade Runner 2049 and Frozen II are +5.7%, and +2.8% at `--preset veryslow`. The encoder has loop restoration, OBMC, every compound mode, all seven references, temporal motion vectors, local warped motion, masked compound, inter-intra, a temporal filter and model, and one tile a picture. It takes about 0.46 s a 1080p picture on 24 cores, where SVT-AV1 preset 4 takes 0.04 s. See [AV1: next steps](#av1-next-steps) |
| 12. 2D to 3D | Started. The plan is in [2d-to-3d.md](2d-to-3d.md). `internal/nn` reads PyTorch checkpoints as they are and has the layers of Video Depth Anything, each checked against PyTorch. `internal/depth` runs Video Depth Anything Small's streaming form from its published checkpoint and gives the same depth as PyTorch, within 1e-3 on film frames. It takes 25 ms a 1080p frame on an RTX 5090 through `internal/cuda`, and 1.3 s on the CPU. `--to-3d` converts a film to any 3D layout. In internal/stereo the depth is shaped into disparity, the eyes are rendered with the nearer covering the farther, and the holes are filled from the background. `tools/stereobench`, a semi-global matcher in Go, measures the result against native and studio-converted 3D discs, and the default depth is now the discs'. A faster GPU path is done, with tensor cores, attention in one pass, and the networks' inputs and the eyes on the GPU, at about 100 pictures a second. Blu-ray 3D images are done too, with `--output film.iso`. Still to do: layered glass and reflections, filling across a shot, and the subtitles' 3D on a disc. A grading learnt from 27 films was tried and gained nothing. A learnt depth refinement, built in, gains 0.5 to 1 dB |

## AV1: next steps

### Standing (2026-11-07)

The bench clips are now 240 pictures each, with keyint 250. On 120
pictures the key frame had 1.7 times its share of the bytes, which
flattered every finer key frame and anchor, and the standing itself. The
clips are Blade Runner 2049, Children of Men, Free Solo, Godzilla vs.
Kong, Raya and the Last Dragon and Toy Story 4. Frozen II and
Incredibles 2 were lost with their sources. The copies now live in
`/mnt/media/av1bench-sources`. The six are measured at 1080p 8-bit, tone
mapped. The five HDR ones are also measured at 1080p 10-bit and at 2160p
10-bit as they are, over 120 pictures. The BD-rate is against SVT-AV1
2.3.0 preset 4, on 24 cores:

| clips | config | PSNR-Y | PSNR-YUV | SSIM | VMAF | CPU s a picture (SVT) |
|---|---|---|---|---|---|---|
| 1080p 8-bit, six | speed 0 | -3.1% | -5.0% | -0.1% | +2.1% | 4.33 (0.30) |
| 1080p 8-bit, six | speed 2 | +2.9% | +0.3% | +4.0% | +8.1% | 1.72 (0.30) |
| 1080p 10-bit, five (v0.17.0) | speed 0 | +1.3% | -0.8% | +1.8% | +0.5% | 4.01 (0.32) |
| 1080p 10-bit, five (v0.17.0) | speed 2 | +3.8% | +1.6% | +4.0% | +3.1% | 2.08 (0.32) |
| 2160p 10-bit, five | speed 0 | -2.3% | -6.1% | -4.0% | -1.5% | 11.5 (0.96) |
| 2160p 10-bit, five | speed 2 | +0.5% | -3.7% | -1.8% | +1.7% | 5.15 (0.96) |

Speed 0 is smaller than preset 4 by every metric at 2160p, and by PSNR
and SSIM at 1080p. Speed 2 is at PSNR-YUV parity at 5.8 times preset 4's
CPU, or 5.4 times at 2160p. At v0.17.0 it was 8 times. v0.18.0 made the
difference. Its temporal model looks a mini-GOP ahead, with finer anchors
and coarser leaves at a given size, and that is where the VMAF gap comes
from. The deep inter frames are searched as SVT-AV1's preset 4 searches
them, which saved 19% of the CPU. The partition depths taken from the
nearest reference's blocks saved 4%, restoration held to depth 2 saved
5%, the model keeping its choices for a held mini-GOP saved 3%, and AVX2
kernels for the remaining transforms, the dequantizer and the temporal
filter's weights saved 4%. Starting the motion search from the enclosing
square's vector made the streams 1% smaller.

What is left of the speed gap is what SVT-AV1 does per evaluation. At
preset 4 it fully codes 20 to 60 blocks in a 64x64 region where ours
codes hundreds, and it has AVX2 C at every stage. We ported its light
depth pre-decision. It lost 3.7% for 21% less CPU, which is no better
than the speeds' own curve, so it was not taken.

After v0.18.0 the VMAF gap is down to the allocation's balance. The
lookahead boosts anchors harder than SVT-AV1 does. PSNR rewards that and
VMAF does not. `--tune vmaf` moves it back, with the anchors' ratio at
0.80, deblocking sharpness 4 and no CDEF on the deep frames at speed 2.
That is 3.7% smaller by VMAF and 1.1% larger by PSNR-YUV. Rate-distortion
quantization, the sharpness of the filters and the temporal filter were
ruled out as causes.

In production, `--crf` is on SVT-AV1's scale, where q = 4 crf. It had
NVENC's before. The defaults are the HEVC ones plus 2, matched by VMAF on
a disc minute. 20 minutes of a 1080p disc and a 2160p HDR minute went
through the tool at speed 2 with steady memory, 3 to 5 GB at 1080p and
15.5 GB at 2160p, and with consistent timelines.

### History

`tools/av1bench` measures the encoder against SVT-AV1, through ffmpeg's
libsvtav1. It ran on six 120-picture 1080p 8-bit clips: Blade Runner
2049, Frozen II, Godzilla vs. Kong, Children of Men, Free Solo and
Incredibles 2, with the HDR ones tone mapped. It reports the BD-rate by
PSNR-Y, PSNR-YUV, SSIM and VMAF against SVT-AV1 2.3.0 preset 4, and every
stream is decoded by libdav1d. This was the baseline, on 24 cores:

| config | PSNR-Y | PSNR-YUV | SSIM | VMAF | fps | CPU s a picture |
|---|---|---|---|---|---|---|
| SVT-AV1 preset 2 | -9.2% | -10.4% | -9.3% | -7.0% | 10.3 | 0.85 |
| SVT-AV1 preset 4 | 0 | 0 | 0 | 0 | 24.5 | 0.30 |
| SVT-AV1 preset 6 | +22.6% | +22.5% | +15.5% | +26.1% | 58.6 | 0.10 |
| SVT-AV1 preset 8 | +42.0% | +43.9% | +32.2% | +43.9% | 78.3 | 0.06 |
| ours, speed 0 | +7.0% | +7.7% | +11.4% | +6.7% | 0.99 | 15.7 |
| ours, speed 1 | +9.7% | +10.4% | +13.8% | +8.8% | 1.58 | 10.1 |
| ours, speed 2 | +10.9% | +11.6% | +15.0% | +10.8% | 2.05 | 7.6 |

So we were 7 to 11% larger than preset 4 and took 25 to 50 times its CPU
time. Speed was the larger gap. Three changes followed. The extended
partitions are estimated from the halves and quarters. The motion search
is cached for a superblock's partitions. The filters' and the temporal
model's searches are cut short from speed 1 up. With those:

| config | PSNR-Y | PSNR-YUV | SSIM | VMAF | fps | CPU s a picture |
|---|---|---|---|---|---|---|
| ours, speed 0 | +7.3% | +8.0% | +11.7% | +7.0% | 1.31 | 12.6 |
| ours, speed 1 | +9.8% | +10.6% | +13.5% | +8.7% | 2.19 | 7.5 |
| ours, speed 2 | +11.3% | +12.1% | +15.2% | +10.8% | 2.84 | 5.6 |

Then the inter candidates were costed with DCT_DCT alone, and the best
one's transforms were searched afterwards. This is libaom's way:

| config | PSNR-Y | PSNR-YUV | SSIM | VMAF | fps | CPU s a picture |
|---|---|---|---|---|---|---|
| ours, speed 0 | +7.8% | +8.4% | +12.2% | +7.1% | 1.72 | 9.9 |
| ours, speed 1 | +10.4% | +11.0% | +14.1% | +9.5% | 2.58 | 6.4 |
| ours, speed 2 | +11.6% | +12.2% | +15.3% | +11.4% | 3.00 | 5.3 |

The transforms in AVX2 then made the same streams 1.10 times faster.
Speed 2 reached 3.28 pictures a second, at 4.8 CPU seconds each. A few
things were tried since and dropped. Intra in inter pictures with fewer
modes, depths or type searches cost +0.6 to +1.4% for 1.08 to 1.14 times
the speed. No 4x4 blocks, or 8x8 whole only, in inter pictures cost
+0.1% and +0.3% for 1.02 and 1.08 times, which is the same trade as a
speed step. No 4:1 partitions cost +1.1% for 1.14 times. The inter-intra
masks made once and blended in AVX2, and the distortions summed in AVX2,
took it to 4.65 CPU seconds a picture on the same streams.

The cores were then idle a third of the time. In every 32-picture
mini-GOP, the frames below the anchor could use any earlier frame still
in a slot. Among those was the previous mini-GOP's deepest frame, which
is coded last, so each mini-GOP waited for most of the one before it.
Only 6 to 9 of the 24 cores were busy for 3 to 4 seconds in every 8. The
fix limits every frame to depth 2 of the earlier mini-GOPs' frames, as
the anchors already were, and codes four mini-GOPs at once. That keeps
23 to 24 cores busy past the stream's first mini-GOP. The coded frames
and their sources are also let go of as soon as they are coded, which
brings the 1080p peak to 4.0 GB from 4.7:

| config | PSNR-Y | PSNR-YUV | SSIM | VMAF | fps | CPU s a picture |
|---|---|---|---|---|---|---|
| ours, speed 0 | +7.9% | +8.5% | +12.4% | +7.2% | 2.25 | 8.9 |
| ours, speed 2 | +11.5% | +12.1% | +15.3% | +11.5% | 3.87 | 4.7 |

Depth 3 was 0.2% smaller at 1.10 times the time, and depth 4 was 0.1%
smaller at 1.09 times. Both are within the clips' noise. 2 and 3
mini-GOPs at once were 1.10 and 1.15 times.

More was tried and dropped. Pruning the horizontal and vertical
partitions by the split's cost gave +0.6% for 1.11 times. A first pass
that decides the partitions with a cheaper search gave +0.9% for no
gain, or +12% for 1.49 times. Cutting intra in inter pictures where
inter's estimate is lower gave +3.3% for 1.06 times. A skip decided from
the prediction error has no threshold that separates it, because the
boundary spans four octaves and moves with q. The candidates' estimates
with one filter gave +0.3% and were slower.

What is left is listed below, by what each step is likely worth. The
size numbers are from the earlier two-clip bench, the two 120-picture
1080p clips. The small tunings around today's settings are all within
0.3% of them. Those tunings are lambda, the key frame and pyramid q, the
temporal filter's and model's strengths, and the motion search's starts.

1. **A full temporal model.** Done. It is libaom's form, where each block
   is coded in the model and the references' coding loss is propagated
   in distortion and rate. Against the SATD ratios we had before, it is
   0.9% smaller by PSNR-YUV and 2.5% by SSIM, at the same speed:

   | config | PSNR-Y | PSNR-YUV | SSIM | VMAF | fps | CPU s a picture |
   |---|---|---|---|---|---|---|
   | ours, speed 0 | +7.0% | +7.7% | +10.2% | +7.3% | | |
   | ours, speed 2 | +10.9% | +11.7% | +13.4% | +11.1% | 3.86 | 4.7 |

   Then the key frame went into the model. It is coded with the mini-GOP
   after it, and its q is set by what that mini-GOP takes from it.
   libaom's decoder built with CONFIG_ACCOUNTING gives each syntax
   element's bits per frame, and `av1bench -keep` runs it on both
   encoders' streams. On Free Solo, SVT-AV1 spent twice what we did on
   its key frame, at q 16 to our 44, and a third less on the frames after
   it, which predicted from it.

   | config | PSNR-Y | PSNR-YUV | SSIM | VMAF | fps | CPU s a picture |
   |---|---|---|---|---|---|---|
   | ours, speed 0 | +6.1% | +6.3% | +10.3% | +7.7% | 2.23 | 8.9 |
   | ours, speed 2 | +9.3% | +9.6% | +12.6% | +10.7% | 3.84 | 4.7 |

   The clips have one key frame in 120 pictures, and a film has one in
   250, so a film gains less from this. Left to do are intra in the model
   from the reconstruction's neighbours, and compound references.
2. **Frame-level parallelism.** Done as far as whole frames go, as
   described above. Past the first mini-GOP the cores are busy. What is
   left is the stream's start, which is a key frame and then one frame
   at each pyramid level, and the cost of one tile a picture. One tile is
   5.9% smaller than 2x2 tiles, but a 1080p frame's rows, two superblocks
   apart, keep about 8 cores busy where four tiles kept 17. Starting a
   frame's rows as its references' rows are done is what x264's frame
   threads do. It needs a frame's CDFs from earlier frames than its
   references, and its temporal motion vectors too. The CDFs come
   through primary_ref_frame and are worth 0.5%.
3. **128x128 superblocks.** Blocks up to 128x128 mean fewer partition and
   skip symbols where the picture is flat. In libaom this is worth 0.5 to
   1% on HD. It touches the superblock loops, the partition search,
   CDEF's and loop restoration's signalling, and the wavefront. The
   wavefront would have half the rows and fewer cores busy. See 2.
4. **The side information.** Coefficients are only a third of an inter
   frame's bits. Motion vectors are about 17%, compound modes and
   references about 10%, and skip mode 3 to 6%. A breakdown of the
   symbols' bits says this is where the remaining gap is. One finding to
   keep in mind here is that every tool earns its bits. Without OBMC we
   are 0.6% worse, without warped motion 0.6%, without masked compound
   0.6%, without inter-intra 0.2%, and without skip mode 1.2%.
5. **Smaller things.** A frame's interpolation filter could be chosen by
   its content. EIGHTTAP_SHARP throughout is 0.8% better on Blade Runner
   2049 and 0.5% worse on Frozen II, and EIGHTTAP_SMOOTH is 7.1% worse.
   The remaining SIMD is the temporal filter's weights and the
   quantizer's scan. And 4K, which is now also coded as one tile, should
   be measured.
