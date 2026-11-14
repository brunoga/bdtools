# The HEVC encoder

This is a software HEVC encoder written in Go. It lives in package
`internal/hevc`, beside the decoder. It exists so that `--encoder builtin`
can code HEVC without x265 or a GPU, in 2D and side by side first and,
later, as MV-HEVC. Work on it started on 2026-10-16.

## Shape

The encoder is the decoder plus decisions and a writer. For the picture
it is coding it keeps a `sliceDec`, which is the decoder's slice state,
over its own `picture` and `picState`. Everything the standard defines
is done with the decoder's own code: availability, intra prediction, the
inverse transform and scaling, reconstruction, and later the merge and
AMVP candidates and motion compensation. What it decides, it writes with a CABAC encoder whose context
derivations mirror the decoder's parsers line by line.

The reconstruction is therefore the decoder's by construction, and
the loop filters can be left to the decoder. Every
coded access unit goes through the package's `Decoder`, and the pictures
that come out of it are the references for the pictures that follow.
The MVC encoder in `mvc/` does the same with its decoder.

A rate-distortion decision needs an estimate of the bits and a measure
of the distortion. The bits are estimated by a `cabacCost` that follows
the context states as the arithmetic coder would. The distortion is the
squared error of the reconstruction. Lambda follows HM's
`0.57 * 2^((QP-12)/3)`, with the usual slice-type factors.

## Milestones

1. **Intra.** The encoder writes the VPS, SPS, PPS and slice headers,
   and has a CABAC encoder, forward transforms, a quantiser and a
   residual writer. It decides the CU quadtree and the TU split by
   rate-distortion. For the intra mode it prescreens with the SATD and
   runs RD on the survivors. It writes WPP substreams, one slice with a
   substream per CTB row, in 8 and 10 bits. Deblocking is left to the
   decoder and SAO is off. The output is verified by decoding it with
   the package's decoder, which must match the encoder's own
   reconstruction sample for sample with deblocking off, and with
   ffmpeg. A `cmd/hevcenc` tool codes raw video, so that the encoder
   can be benchmarked against x265.
2. **Inter.** P and B pictures in a hierarchical GOP, with merge and
   AMVP, TMVP, a quarter-sample motion search on the SATD, skip
   detection and reference B pictures. Rate control starts as a constant
   QP with the GOP's offsets and becomes CRF-like later.
3. **Speed.** CTB rows are coded in parallel on the wavefront and
   pictures in a pipeline. AVX2 kernels do the SATD, the SAD, the
   forward transforms and quantisation. RDOQ and sign hiding save bits.
4. **Features.** SAO, adaptive quantisation and HDR pass-through in the
   VUI and SEI. `--encoder builtin` for `--codec h265` in bdtools.
5. **MV-HEVC.** Layer 1 with inter-view prediction. This needs the
   decoder's second layer first. It replaces NVENC in `--layout mvhevc`
   when there is no NVIDIA GPU.

## Status

- 2026-10-16: milestone 1 is done. The code is in
  `internal/hevc/enc*.go`. It has an `Encoder` with `NewEncoder`,
  `Encode`, `Flush` and `Stats`, a picture coder that decides
  the CU quadtree, the TU split and the intra mode by rate-distortion,
  the CABAC encoder and its cost estimator, the residual writer, the
  forward transforms and quantiser, and WPP substreams, in 8 and 10
  bits. The decoder's `intraPredict` was split into `intraRefs` and
  `intraPredBlock` so that the mode search can use them.
  `TestEncoderRoundTrip` checks that the package decoder reproduces the
  reconstruction sample for sample and that ffmpeg agrees.
  `cmd/hevcenc` codes raw video. On the first 48 pictures of the Gravity
  minute at QP 28 the encoder produces 1,666,735 bytes at 44.73 dB luma,
  at 1.7 fps on one core. x265 medium all-intra at that PSNR, which
  falls between its CRF 26 and 28 points, produces about 1,658,000 bytes
  at 0.47 CPU-seconds a picture, against our 0.6. So the size is at
  parity before RDOQ, sign hiding and SAO. The search is the
  coarse-to-fine angular one, with three bits of MPM cost and no 64x64
  intra CUs.

- 2026-10-16, later: milestone 2 is coded. The encoder now codes P and
  B pictures. Pictures come in groups of BFrames + 1, with 0, 1, 3 or 7
  B pictures in a group. The anchor is a P picture predicted from the
  last Refs anchors. The B pictures form a bisection hierarchy, each
  predicted from the pictures around it. The reference picture sets list
  everything kept. Merge and skip go through the decoder's own
  `mergeCandidate` and AMVP through its `amvp`, with TMVP on. The motion
  search is an integer diamond search on the SAD from the predictors,
  then half and quarter samples on the SATD of the exact prediction. A
  bi-predictive candidate is made from the two lists' best vectors. The
  inter transform tree is decided by rate-distortion against the kept
  prediction, and intra is tried in every inter CU. The in-loop
  decoder's pictures are the references, with `finishPicture` after each
  access unit. Found on the way: the two lists' reference
  indexes share one context set, as HM and ffmpeg have it. ffmpeg
  decodes a stream coded otherwise without complaint, and silently
  wrong. On the 48 Gravity pictures, P only at QP 28 gives 418,537 bytes
  at 44.28 dB, where x265 medium with P only gives about 390,000 at
  44.4. Three B pictures at QP 26 give 462,045 bytes at 44.82 dB, where
  x265 medium at its defaults gives about 385,000 at that PSNR. This
  runs at 0.47 fps on one core. Next come the QP cascade over the
  hierarchy, partitions, a better bi-prediction and early skip, and then
  speed.

- 2026-10-16, night: milestone 3 is begun. Rectangular partitions are
  behind `Rect`. They save 1.7% for 1.8 times the work. The QP cascade
  is P+4, B+8, which is x265's, as the default; it measured best among
  eight. There are no intra trials in B pictures, as in x265 medium. The
  search's visited set is a bitset and the diamond starts from step 4.
  CTB rows are coded in parallel on the wavefront, with `picShared`
  holding per-worker `picCoder`s and `waitAbove`/`advance` between the
  rows. Pictures run in a pipeline. `picJob`s are made in decoding
  order, the in-loop decoder is fed in that order, references are found
  by frame number, and there is back pressure on the jobs to code. Each
  anchor is emitted before the previous group's B pictures, so the
  anchors run ahead. The streams are byte-identical across thread counts
  and pipeline depths, which `TestEncoderThreadsDeterministic` checks.
  There are AVX2 kernels for the SAD and the 8x8 SATD on 16-bit samples
  in `enc_amd64.s`, tested by `TestEncKernels`, and explicit butterflies
  for the forward transform, tested by
  `TestForwardTransformMatchesMatrix`. The 48 pictures went from 103 s
  on one core to 8.1 s with all of this, at 362,428 bytes and 44.15 dB.
  x265 medium at its defaults gives about 379,000 at that PSNR. The
  critical path is now the anchors, which are coded in sequence at about
  0.7 s each with six cores busy on a picture's wavefront.

## Where it stands (2026-10-16)

The measure is the mid-film Gravity minute, 1390 frames of 1080p, the
left eye, on the 24-core 285K. x265 4.3 is compared with this encoder
at its defaults. Everything is decoded by ffmpeg 8 and scored against
the source as BD-rate on luma PSNR, over four rate points each. The
times are the mean of the four.

| encoder | BD-rate vs x265 medium | wall | CPU per picture |
|---|---|---|---|
| x265 medium | 0% | 14.9 s (93 fps) | 0.13 s |
| this encoder | -9.1% | 27.2 s (51 fps) | 0.37 s |
| x265 slow | -13.8% | 36.9 s (38 fps) | 0.30 s |

Three things took it there from -1.2% at 63 fps: sign data hiding, SAO
and RDOQ. The SAO gathers its statistics on the reconstruction before
deblocking, and merges left and up. The RDOQ tries each level as the
rounded one, one less or zero, then picks the best last position, with
the bits taken from the CTU's context states. On the first 240 pictures
the steps were -7.0% for the base, -8.4% with sign hiding, -11.5% with
SAO and -14.6% with RDOQ, for 20% more CPU in all. QP 19, or --crf 24,
now gives 14.7 MB at 45.05 dB where x265 medium's CRF 24 gives 15.0 MB
at 44.80 dB.

Our time includes the verification decoder, which is about 3% of it.
The first 240 pictures flatter the encoder against x265. The full
minute is the fairer measure.

How it got there from 5.6 fps on 2026-10-16, in order of effect.
First, decision shortcuts, measured one by one against x265.
The memory notes and the commit history have them. Second, x265-style
frame parallelism. Each picture is filtered row by row on the encoder's
own state, and pictures wait on reference rows rather than on the
decoder. Third, groups of seven B pictures with sixteen pictures in
flight, where before the anchor chain had bound the clock with three.
Fourth, AVX2 for the SAD, SATD, SSD, the forward and inverse transforms
and the quantiser. Fractional reference planes helped once. Each
reference's luma was interpolated at the sixteen quarter-sample phases
and the sub-sample search read it. Once the interpolation was in AVX2
they no longer helped. 1080p was 4-9% faster without them and 4K was 3%
slower. They also held 280 MB a reference at 4K, 37 GB at once. So they
went, on 2026-10-17.

The CPU per picture is still 2.8 times x265 medium's.
Its largest parts are the motion search, which is the sub-sample SATD,
the merge candidates, the interpolation and the residual trials. Beyond
those, x265 has a lookahead-driven analysis and assembly for intra
prediction and interpolation that we do not. A cheaper sub-sample search
was measured and rejected. With four quarter-sample points, or four at
both steps, it lost 0.7 and 1.9 points for 4% and 7% less CPU.

## MV-HEVC (2026-10-16)

With `Views: 2` the encoder codes the left view as the base layer and
the right as layer 1. This is Annex F/G, and Apple's HEVC Stereo Video
profile.

- The VPS extension follows NVENC's and x265's choices. Splitting is
  on, the dimension is multiview, the view ids are 0 and 1, and layer 1
  predicts from layer 0 by default, so there is no flag in the slice
  headers. Prediction is sample prediction only. There is one picture
  format and one buffer size for both layers. Layer 1 has its own SPS,
  with MultiLayerExtSpsFlag and the format from the VPS, and its own
  PPS, both with id 1. A 3D reference displays SEI names the views left
  and right.
- Each base picture gets a twin job on layer 1. The twin has the base
  picture's type, except that an IDR picture's twin is a P picture. It
  has the same QP. Its reference picture set is over layer 1's own
  pictures, plus the base picture of the access unit as a long-term
  reference after the pictures before it in list 0, as F.8.3.4 has it.
  An IDR picture's twin is an IDR P picture that predicts from the base
  picture alone, without temporal motion prediction.
- The twin waits on the base picture's rows like any reference. The
  search always tries the base picture, with a horizontal scan for the
  disparity, because the predictors rarely carry it.
- The decoder in the loop decodes both layers, through `SetLayers(2)`,
  and every picture of both is checked.

On 200 pictures of a 3D disc whose two eyes are 24.4 dB apart, at QP
21, coding the right eye as layer 1 took 1.03 MB. That is 44% of the
2.36 MB it takes coded alone, at the same PSNR of 42.1 dB. Both views
run at 22 pairs a second. NVENC's MVC right eye took 92%
of its left. The decoder decodes NVENC's, x265's and this encoder's
MV-HEVC bit for bit as ffmpeg 8 does, which `TestDecodeMVHEVC` and
`TestEncoderStereo` check.
