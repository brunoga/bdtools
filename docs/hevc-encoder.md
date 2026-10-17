# The HEVC encoder

A software HEVC encoder in Go, in package `internal/hevc` beside the
decoder, so that `--encoder builtin` can code HEVC (2D, side by side and,
later, MV-HEVC) without x265 or a GPU. Started 2026-10-16.

## Shape

The encoder is the decoder plus decisions and a writer. For the picture
it codes it keeps a `sliceDec`, the decoder's slice state, over its own
`picture` and `picState`, and uses the decoder's own code for everything
the standard defines: availability, intra prediction, the inverse
transform and scaling, reconstruction, and (later) merge and AMVP
candidates and motion compensation. What it decides, it writes with a
CABAC encoder whose context derivations mirror the decoder's parsers
line by line. The reconstruction is therefore the decoder's by
construction, and the loop filters are left to the decoder: every coded
access unit goes through the package's `Decoder`, whose output pictures
are the references for the pictures that follow, as the MVC encoder in
`mvc/` does with its decoder.

Rate-distortion decisions estimate bits with a `cabacCost` that follows
the context states as the arithmetic coder would, and measure distortion
as the squared error of the reconstruction; lambda follows HM's
`0.57 * 2^((QP-12)/3)` with the usual slice-type factors.

## Milestones

1. **Intra.** VPS, SPS, PPS and slice header writers; CABAC encoder;
   forward transforms and quantiser; residual writer; CU quadtree and TU
   split by rate-distortion; intra mode decision (SATD prescreen, RD on
   the survivors); WPP substreams (one slice, a substream per CTB row);
   8 and 10 bits; deblocking by the decoder; SAO off. Verified by
   decoding the output with the package's decoder (sample-exact against
   the encoder's own reconstruction with deblocking off) and with ffmpeg.
   A `cmd/hevcenc` tool for raw video, for benchmarks against x265.
2. **Inter.** P and B pictures with a hierarchical GOP, merge and AMVP,
   TMVP, quarter-sample motion search on the SATD, skip detection,
   reference B pictures; rate control as constant QP with the GOP's
   offsets, CRF-like later.
3. **Speed.** CTB rows coded in parallel on the wavefront, pictures in a
   pipeline, AVX2 kernels for SATD, SAD, the forward transforms and
   quantisation; RDOQ and sign hiding for the bits.
4. **Features.** SAO, adaptive quantisation, HDR pass-through (VUI, SEI),
   `--encoder builtin` for `--codec h265` in bdtools.
5. **MV-HEVC.** Layer 1 with inter-view prediction (needs the decoder's
   second layer first), replacing NVENC in `--layout mvhevc` when there is
   no NVIDIA GPU.

## Status

- 2026-10-16: milestone 1 done. `internal/hevc/enc*.go`: `Encoder`
  (`NewEncoder`, `Encode`, `Flush`, `Stats`), the picture coder with the
  CU quadtree, TU split and intra mode decisions by rate-distortion, the
  CABAC encoder and cost estimator, the residual writer, the forward
  transforms and quantiser, WPP substreams, 8 and 10 bits; the decoder's
  `intraPredict` split into `intraRefs` and `intraPredBlock` for the mode
  search. `TestEncoderRoundTrip` checks the package decoder reproduces
  the reconstruction sample for sample and that ffmpeg agrees.
  `cmd/hevcenc` codes raw video. On the first 48 pictures of the Gravity
  minute at QP 28: 1,666,735 bytes at 44.73 dB luma, 1.7 fps on one core;
  x265 medium all-intra at that PSNR (between its CRF 26 and 28 points)
  is about 1,658,000 bytes at 0.47 CPU-seconds a picture against our 0.6.
  Parity in size before RDOQ, sign hiding and SAO; the search is the
  coarse-to-fine angular one, three bits of MPM cost, no 64x64 intra CUs.

- 2026-10-16, later: milestone 2 coded. P and B pictures: groups of
  BFrames + 1 pictures (0, 1, 3 or 7 B pictures), the anchor as a P
  picture from the last Refs anchors, the B pictures in a bisection
  hierarchy from the pictures around them, reference picture sets
  listing everything kept; merge and skip through the decoder's own
  `mergeCandidate`, AMVP through its `amvp`, TMVP on; an integer
  diamond search on the SAD from the predictors, then half and quarter
  samples on the SATD of the exact prediction; a bi-predictive
  candidate from the two lists' best vectors; the inter transform tree
  by rate-distortion against the kept prediction; intra tried in every
  inter CU. The in-loop decoder's pictures are the references
  (`finishPicture` after each access unit). Found on the way: the two
  lists' reference indexes share one context set, as HM and ffmpeg
  have it; ffmpeg decodes a stream coded otherwise without complaint
  and silently wrong. On the 48 Gravity pictures: P only at QP 28,
  418,537 bytes at 44.28 dB (x265 medium with P only: about 390,000 at
  44.4); three B pictures at QP 26, 462,045 bytes at 44.82 dB (x265
  medium default: about 385,000 at that PSNR), 0.47 fps on one core.
  Next: the QP cascade over the hierarchy, partitions, a better
  bi-prediction, early skip, then speed.

- 2026-10-16, night: milestone 3 begun. Rectangular partitions behind
  `Rect` (1.7% for 1.8 times the work); the QP cascade P+4, B+8 (x265's)
  as the default, measured best among eight; no intra trials in B
  pictures (as x265 medium); the search's visited set a bitset, the
  diamond from step 4. CTB rows coded in parallel on the wavefront
  (`picShared` with per-worker `picCoder`s, `waitAbove`/`advance`), and
  pictures in a pipeline (`picJob`s in decoding order, the in-loop
  decoder fed in that order, references by frame number, back pressure
  on the jobs to code), each anchor emitted before the previous group's
  B pictures so the anchors run ahead; streams byte-identical across
  thread counts and pipeline depths (`TestEncoderThreadsDeterministic`).
  AVX2 kernels for the SAD and the 8x8 SATD on 16-bit samples
  (`enc_amd64.s`, `TestEncKernels`), explicit butterflies for the
  forward transform (`TestForwardTransformMatchesMatrix`). The 48
  pictures: 103 s on one core to 8.1 s with all, 362,428 bytes at 44.15
  dB (x265 medium default: about 379,000 at that PSNR). The critical
  path is now the anchors coded in sequence at about 0.7 s each with six
  cores busy on a picture's wavefront.

## Where it stands (2026-10-16)

The mid-film Gravity minute (1390 frames of 1080p, the left eye) on the
24-core 285K, x265 4.3 against this encoder at its defaults, all
decoded by ffmpeg 8 and scored against the source (BD-rate on luma
PSNR, four rate points each; times are the mean of the four):

| encoder | BD-rate vs x265 medium | wall | CPU per picture |
|---|---|---|---|
| x265 medium | 0% | 14.9 s (93 fps) | 0.13 s |
| this encoder | -9.1% | 27.2 s (51 fps) | 0.37 s |
| x265 slow | -13.8% | 36.9 s (38 fps) | 0.30 s |

Sign data hiding, SAO (statistics on the reconstruction before
deblocking, merge left and up) and RDOQ (each level the rounded one, one
less or zero, then the best last position, bits from the CTU's context
states) took it from -1.2% at 63 fps. On the first 240 pictures:
-7.0% base, -8.4% with sign hiding, -11.5% with SAO, -14.6% with RDOQ,
for 20% more CPU in all. QP 19 (--crf 24) now gives 14.7 MB at 45.05 dB
where x265 medium's CRF 24 gives 15.0 MB at 44.80 dB.

Our time includes the verification decoder (about 3%). The first 240
pictures flatter the encoder against x265; the full minute is the
fairer measure.

How it got there from 5.6 fps (2026-10-16), in order of effect:
decision shortcuts measured one by one against x265 (see the memory
notes and the commit history), x265-style frame parallelism (each
picture filtered row by row on the encoder's own state, pictures waiting
on reference rows rather than on the decoder), groups of seven B
pictures with sixteen pictures in flight (the anchor chain bound the
clock with three), and AVX2 for SAD, SATD, SSD, the forward and inverse
transforms and the quantiser. Fractional reference planes (each
reference's luma interpolated at the sixteen quarter-sample phases, read
by the sub-sample search) helped once; with the interpolation in AVX2
they no longer did (1080p 4-9% faster without them, 4K 3% slower) and
held 280 MB a reference at 4K, 37 GB at once, so they went (2026-10-17).

What is left: the CPU per picture is 2.8 times x265 medium's. Its
largest parts are the motion search (sub-sample SATD), the merge
candidates, the interpolation and the residual trials; beyond that,
x265's lookahead-driven analysis and its assembly for intra prediction
and interpolation. A cheaper sub-sample search (four quarter-sample
points, or four at both steps) was measured and rejected: it lost 0.7
and 1.9 points for 4% and 7% less CPU.

## MV-HEVC (2026-10-16)

With `Views: 2` the encoder codes the left view as the base layer and
the right as layer 1 (Annex F/G, Apple's HEVC Stereo Video profile):

- The VPS extension, from NVENC's and x265's choices: splitting on, the
  multiview dimension, view ids 0 and 1, layer 1 predicting from layer 0
  by default (no flag in the slice headers), sample prediction only, one
  picture format, one buffer size for both. Layer 1 has its SPS
  (MultiLayerExtSpsFlag, the format from the VPS) and PPS, id 1, and the
  3D reference displays SEI names the views left and right.
- Each base picture gets a twin job on layer 1: the same type (P for an
  IDR picture's), QP and reference picture set over layer 1's own
  pictures, plus the base picture of the access unit as a long-term
  reference after the pictures before it in list 0 (F.8.3.4). An IDR
  picture's twin is an IDR P picture predicting from the base picture
  alone, without temporal motion prediction.
- The twin waits on the base picture's rows like any reference. The
  search always tries the base picture, with a horizontal scan for the
  disparity, which the predictors rarely carry.
- The decoder in the loop decodes both layers (`SetLayers(2)`), and every
  picture of both is checked.

On 200 pictures of a 3D disc (the two eyes 24.4 dB apart) at QP 21,
coding the right eye as layer 1 took 1.03 MB, 44% of the 2.36 MB it
takes coded alone, at the same PSNR (42.1 dB); both views run at 22 pairs
a second. NVENC's MVC right eye took 92% of its left. The decoder
decodes NVENC's, x265's and this encoder's MV-HEVC bit for bit as
ffmpeg 8 does (`TestDecodeMVHEVC`, `TestEncoderStereo`).
