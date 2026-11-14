# 2D to 3D conversion: a plan

This document explains how bdtools turns a 2D film into a stereoscopic
one, and how we measured the result. The conversion takes a 2D film in
and writes a 3D film out. The output can be full or half side by side, or
full or half over-under, at the source's resolution or at 1080p a view
for players such as the Ugoos. It can also be MVC, both as the MKV that
CoreELEC plays and as a Blu-ray 3D that a disc player plays. As with
everything else in this project, all of it is written in Go. That
includes the depth network, the view synthesis and the encoders. The GPU
is driven from Go, the way NVENC already is.

Ideas for what the conversion could still take from a 2D film are in
[2d-to-3d-ideas.md](2d-to-3d-ideas.md). They are written up to be picked
one by one.

## Can a conversion look like a 3D film?

Two kinds of conversion are on the market, and they are not alike.

A studio conversion is made by hand. Titanic, Gravity's actors and Mad
Max: Fury Road were converted this way. Artists cut every object out of
every shot, which is called rotoscoping. They give each object its depth
and shape, and they paint by hand what the second eye sees behind it. The
result looks like a 3D film. Viewers rarely tell it from one shot in 3D,
and the stereo check in `~/reencode/tools/stereocheck` could not tell
either.

An automatic conversion is what the fan releases are: JFC's, iFA's
"AI3D", iw3's. A network estimates a depth map for each frame, and the
pixels are shifted by it. Today's video depth networks give a convincing
depth for most shots. People and things stand out as they should, with
their roundness, and the depth holds still from frame to frame. Where
they fall short is exactly what a hand conversion is paid for. Hair and
fur, glass, smoke, water and reflections get one depth where they need
two. Thin things such as railings and wires melt into what is behind
them. The strips one eye sees behind an edge must be made up, and they
look smeared when they are. And a depth that is right but flat, which is
called cardboard, or one that is too strong, tires the eyes.

So the answer is yes, within limits. An automatic conversion can look
like a good 3D film for most of its running time, and clearly converted
in its hardest shots. With a conservative depth, most of the picture
behind the screen and little coming out of it, the failures are mild.
That is how the better fan releases are made, and it is what this plan
aims to beat. Hand-conversion quality is out of reach for any automatic
method.

## The state of the art (open source, late 2026)

Three things matter here: the depth networks, the view synthesis with
its hole filling, and the ways of measuring the result.

- **Video depth networks.** Video Depth Anything (CVPR 2025) adds a
  temporal head to Depth Anything V2. It gives a depth that does not
  flicker over films of any length. Its small model is Apache-2.0; the
  base and large ones are CC-BY-NC. Depth Anything 3 (November 2025) is
  newer, and its small and base models are Apache-2.0. DepthCrafter, from
  Tencent, is a video diffusion model. It is better on fine detail but far
  slower, and it is under a non-commercial licence inherited from Stable
  Video Diffusion. Apple's Depth Pro works per frame.
- **View synthesis and filling.** The pixels are shifted by the depth.
  This is called depth-image-based rendering, or splatting. Shifting them
  opens holes, and the holes have to be filled. StereoCrafter, from
  Tencent, with weights released in 2026, fills them with a video
  diffusion model. Its results are the best-looking published, but they
  cost minutes of GPU time for a second of film, and its licence is not
  free. iw3, part of nunif, is the most used open-source converter. It
  runs Depth Anything plus small networks of its own for the warping and
  the filling. Fan releases are mostly made with it or with Owl3D, which
  is commercial.
- **Measuring it.** Mono2Stereo (2025) is a benchmark of stereo conversion
  against true stereo footage. Recent work on "artistic disparity" (2026)
  grades the depth the way a stereographer does, not only by geometry.

The plan builds on the free models. Video Depth Anything Small comes
first, and Depth Anything 3 Small and Base are there to compare. For the
holes it builds on classical, fast filling, with a small learned filler
as a later step. It does not build on diffusion, because a two-hour film
must convert in hours, not days.

## How it works

1. **Decode** the 2D source, as today. Any source bdtools reads will do,
   HDR included.
2. **Cut detection.** The shots are found from the histogram and motion
   breaks the encoders already see. Depth is never smoothed across a cut.
3. **Depth.** Each frame is scaled to the network's size, which is 518
   pixels on the short side, and goes through the depth network. The
   frames go in windows of 32 for the temporal head. HDR sources are tone
   mapped for the network only; the tone mapper exists already. The views
   keep the source's pixels.
4. **Depth finishing.** The depth is brought to the picture's resolution
   along its edges. A guided filter does this, so that an edge in the
   depth sits on the edge in the picture. Where the network still wavers,
   the depth is steadied in time. This is motion-compensated, with the
   motion search the encoders have. Then the depth is normalised per
   shot.
5. **Depth grading.** The relative depth becomes a disparity in pixels.
   The grading decides how much depth there is in all, as a percentage of
   the width, about 2 to 3% by default. It decides where the screen sits,
   with most of the picture behind it. It decides how much may come out
   of the screen and how fast that may change from shot to shot. And it
   adds floating windows where something in front of the screen meets the
   frame's edge. The options are `--3d-depth`, `--3d-screen` and
   `--3d-popout`.
6. **Views.** Each eye is made from the source shifted half the disparity
   each way, so that each eye's holes are half as wide. The shifting is
   forward splatting with a depth test at a quarter of a pixel.
7. **Filling.** An eye sees things the source does not. Those are filled
   first from the neighbouring frames. Behind a moving object, the
   background was visible a moment before or after, and the search for it
   is motion-compensated. What is still missing is filled by depth-aware
   extension from the background side only, with the texture continued
   rather than stretched. A small learned filler, like iw3's and trained
   here as the OCR network was, is a later step if the classical one is
   not good enough.
8. **Subtitles.** Each subtitle is placed in front of what is under it.
   The depth under its area gives its offset, the same way the disc's
   offset metadata does for a 3D disc. The 3D subtitle code exists
   already.
9. **Encode and mux** into any of the layouts below.

## Outputs

| Output | Picture | Plays on |
|---|---|---|
| Full side by side, full resolution | two 3840x2160 views (7680x2160) for UHD, two 1920x1080 for HD | PCs, headsets, players that take it |
| Full side by side, 1080p | 3840x1080 | Ugoos and other Android/CoreELEC boxes, 3D TVs |
| Half side by side | 1920x1080 | everything |
| Full and half over-under | 1920x2160 and 1920x1080 | the same |
| MVC MKV | H.264 MVC, 1080p | CoreELEC (Kodi) frame packed |
| Blu-ray 3D | H.264 MVC in a disc structure | Blu-ray 3D players, from a burned disc or an ISO |

Each of these exists. The layouts are `--layout full`, `half`, `ou`,
`half-ou` and `mvc`, with `--scale` for 1080p. The Blu-ray 3D is
`--output film.iso`, a disc image with the film's MVC in it.

A disc image takes a few things. It needs BDMV with its index, movie
object and clip files. It needs a playlist with the 3D stream table. The
base and dependent views go in their own clips, interleaved into SSIF
extents, and a UDF 2.50 ISO wraps it all.

Much of that is reused. bdtools reads every one of these structures
already, in `internal/bdmv`. The MVC encoder is already tuned for disc
players' decoders; see
[mvc-player-compatibility.md](mvc-player-compatibility.md).

A disc needs a few things on top. The picture must be SDR at 8 bits,
1080p at 23.976 fps, and within Blu-ray's bitrate limits. An HDR source
is tone mapped for this output only.

## What is there to build on

Much of the conversion was already in bdtools before this plan:

- The decoding of every source, with tone mapping, scaling and cropping.
- Every output layout, and the MVC encoder.
- The 3D subtitle placement, and the GPU drivers from Go. NVENC and NVDEC
  are driven through purego.
- A neural network inference path in Go. `internal/ocr`'s CRNN runs on
  AVX2 GEMM kernels.
- The encoders' motion search, and VMAF, PSNR and SSIM in Go, in
  `internal/vmaf`.
- Native 3D discs to measure against, namely Avatar, Hugo and Life of Pi,
  and the stereo check.

## What has to be built

1. **A tensor engine in Go** (`internal/nn`). It needs these ops:
   - convolutions and transposed convolutions;
   - attention, layer norm and GELU;
   - resizing.

   Then it needs a safetensors reader, so that the published weights load
   as they are. They are downloaded on first use and are not in the
   repository. The CPU code builds on the OCR kernels, which are AVX2 and
   FMA; a NEON path comes later. A GPU backend comes after that. It uses
   the CUDA driver API through purego, as NVENC does, with cuBLAS for the
   matrix products and kernels of our own for the rest. Everything is
   checked against PyTorch outputs on fixed frames, to a tolerance.
2. **The depth network.** Video Depth Anything Small first. It is a DINOv2
   ViT-S encoder, a DPT head and its temporal blocks. Then Depth Anything
   3, to compare.
3. **Depth finishing and grading**, steps 4 and 5 above.
4. **View synthesis and filling**, steps 6 and 7. On the CPU first, then
   on the GPU.
5. **The command.** `--to-3d` on a 2D source, with every layout, subtitles
   placed by depth, and HDR handled.
6. **Blu-ray 3D authoring** (done). `--output film.iso` for MVC.

## How fast

The depth network dominates the cost. The small model at 518 pixels needs
about 100 GFLOP a frame, so a two-hour film needs about 17 PFLOP.

- **On the 24 cores**, an optimised AVX2 path should reach about 1
  TFLOP/s. That is 5 hours or so for a film.
- **On the RTX 5090**, with half precision and cuBLAS, the depth takes 10
  to 20 minutes. View synthesis and filling add a few minutes, and the
  encode takes what it takes today.

So the GPU backend is not optional if this is to be practical. A CPU-only
machine converts overnight.

Here is what we measured on the RTX 5090 with a 1080p film. The small
network takes 25 ms a picture in single precision, and 7.2 ms on the
tensor cores with attention in one pass. Base takes 49 and 14.2 ms, and
Large takes 122 ms, in single precision only. The whole conversion, with
NVENC, runs at 112 pictures a second refined and 124 not, once it is
started. That is a two-hour film in about 25 minutes.

The picture goes to the GPU as it is decoded and comes back as its two
eyes and their disparity. Everything in between is kernels. The networks'
inputs are made on the GPU: the picture resized and made RGB for the
network, and the half-size picture and standardised depth for the
refinement. The refined depth's renormalisation is a kernel, and so are
the disparity and the rendering of the eyes. The rendering runs a warp
per row. The row's targets are kept in shared memory, and each target's
winner is chosen by an atomic maximum of its depth and its segment's
order. That gives the CPU's arithmetic and choices to the bit. The
depth's mean and deviation are summed over the whole GPU, not over one
block of it. Of the disparity, only what the floating window and the
subtitles read comes back. That is its side strips and a grid of its
lower half, about a tenth of it. A picture's work is queued on the GPU,
and the next picture is decoded and copied meanwhile, before we wait for
it.

On the tensor cores, attention is one kernel per layer. This is flash
attention: the keys and values are taken a tile at a time, the softmax is
kept running, and the scores are never stored. It took attention from 6.1
ms a picture down to 1.3. Layer normalisation is a warp per row in single
precision, because double is slow on consumer GPUs; that went from 2.4 ms
down to 0.4. The matrix products read their rows four values at a time. A
product whose tiles of 128 would leave multiprocessors idle or half empty
runs in tiles of 64 instead. The GPU's work sets the pace, since the
CPU's is a third of it, and the network's matrix products are most of
the GPU's.

## How good: measured, not guessed

The native 3D discs are the ground truth. Their left eye is a 2D film
whose right eye is known. That gives three ways to measure a conversion.

- **Comparing eyes.** Convert the left eye, then compare the made right
  eye with the real one. VMAF, PSNR and SSIM are all here already.
- **Comparing depth.** Compare the made disparity with the one measured
  between the real eyes, using the stereo check's matcher.
- **On our clips.** Mono2Stereo's measures, run on our clips.

That gives a number for every change, whether a better depth network or
a better filler, the way the encoders have their BD-rate. Beside the
numbers there is viewing on the TV and the Ugoos, which is the only judge
of comfort.

## Measured so far

`tools/stereobench` runs on twelve 4-second clips, two from each of six
discs. Avatar: The Way of Water, Hugo and Life of Pi were shot in 3D.
Gravity, Titanic and Mad Max: Fury Road were converted by studios. Four
of Gravity's glass shots are added to those. The table scores the right
eye made from the left against the disc's:

| Right eye from | PSNR | VMAF | What it says |
|---|---|---|---|
| no depth (flat) | 27.4 | 34 | the floor |
| the disc's own depth | 36.0 | 80 | the renderer and filler, given perfect depth |
| our depth, scaled per picture to the disc's | 30.1 | 43 | the depth's shape |
| our depth, scaled once for the whole clip | 28.6 | 34 | what following shots alone could reach |
| our conversion (first defaults) | 24.1 | 13 | the shape and the grading |
| each picture's range normalised to the discs' | 25.3 | 16 | |
| the depth as it is, mapped as the discs map it | 26.9 | 26 | `--3d-refine none` |
| refined (`--3d-refine`), scaled per picture to the disc's | 31.0 | 49 | the refined depth's shape |
| refined, mapped as the discs map it | 27.5 | 30 | `--3d-tune none` |
| tuned (`--3d-tune`), scaled per picture to the disc's | 31.3 | 51 | the tuned depth's shape |
| tuned, mapped as the discs map it | 27.3 | 30 | the defaults now |

Here is what that teaches.

- **The depth is most of the gap, not the renderer.** Given the disc's
  depth, our right eye is 6 dB nearer the disc's.
- **The grading was too far behind the screen.** The discs spread their
  depth about the screen. Their 5th to 95th percentile runs from -21 to
  +16 px at 1920, with the median at -4. The first defaults ran from -40
  to +13, with the median at -24, and that cost 1.2 dB.
- **Normalising each picture's depth range only loses.** The network's
  depth is steady from picture to picture, and its scale is close to the
  discs'. Fitted over a whole clip, the discs' disparity comes to 0.06%
  to 0.19% of the width per unit of our depth, on every clip. Taken as it
  is, at 0.12% a unit with the farthest point 0.75% behind the screen,
  the made right eye gains 1.6 dB and 10 VMAF. On the four glass clips,
  which were held out of that choice, it gains 0.9 dB and 3 VMAF. What is
  left to the per-picture fit, 3 dB, is the depth's shape within each
  picture and the shots' own grading.
- **A curve fits better than a line.** Our depth fitted to the disc's by
  any rising curve is 2.9 px off. Fitted by a line it is 3.9 px off. So
  the discs' grading is not a scale and an offset. That is what milestone
  8 is to learn.
- **The made eye's errors are at edges.** There the depth's soft edges
  drag the background along. A guided filter with radius 8 did not help
  as tuned, so it is off.
- **Glass.** Clear glass with one thing behind it is fine. Gravity's visor
  close-up has a correlation of 0.85 and is 1.7 px off. A window showing
  two things at once is not fine. Gravity's porthole, with her face over
  the Earth, is the worst clip, at a correlation of 0.42 and 7 px off.
  One depth per pixel cannot put the face and the Earth at their two
  depths. Mirror-like visors match too, but only around the glass. On the
  glass itself the disc's eyes agree on no one depth, as if the
  reflections had a depth of their own. What fixes that is layered
  rendering. Such a region is split into what is seen through the glass
  and what is reflected. Each gets its own depth, each is rendered, and
  the two are added. Before that can be measured, the matcher needs to
  find two depths in a region, so that the benchmark can score it.
- **A grading learnt from the discs does not generalise.** We fitted the
  discs' grading of 27 more films by a ridge regression on each picture's
  features: its depth's percentiles and layout, and its brightness. The
  data was eight 3-second stretches of each film, every sixth picture,
  from `stereobench -frames` and `tools/gradefit`. Scored with each film
  left out, it seemed to win, at 6.5 px off against the fixed mapping's
  8.5. But on the benchmark's six films it lost, 10.8 against 8.5, and
  26.1 dB against 26.9. The gain was the training films' place behind the
  screen, not the features. A film's median sits anywhere from 18 px
  behind the screen, in Pacific Rim: Uprising, to 3 px in front, in Hugo,
  Life of Pi and Titanic. That is a choice of its makers, and its pictures
  do not tell it. With each film's own offset taken out, the features
  predict no better than the fixed mapping, 6.1 px held out for both.
  Two things are left of this. The screen's place is a setting,
  `--3d-screen`. And the grading within a film is the depth's shape, not
  its scale.
- **A learnt refinement corrects the depth's shape.** The refinement is a
  small U-Net of 300k weights, in `internal/stereo/refinenet.go`. It takes
  the picture and the network's depth at half size and gives a correction
  of the depth. It was trained in Go with `tools/depthtrain` on 3,774
  pictures of the 27 films; that is now 7,423 pictures of 53 films, see
  below. The loss is what is left of the disc's disparity after fitting
  it a picture at a time, so that each film's grading does not count. On
  the benchmark's six films, which it never saw, the depth's error after
  fitting went from 7.9 to 6.9 px, and its correlation with the disc's
  from 0.79 to 0.83. The right eye made from it and fitted per picture
  went from 30.2 to 31.0 dB, VMAF 44 to 49. With the discs' mapping
  re-found for it, 0.10% a unit instead of 0.12%, the conversion's own
  right eye went from 26.9 to 27.5 dB, VMAF 26 to 30. On the glass clips,
  held out of that choice, it went from 25.7 to 26.8, VMAF 27 to 33. The
  refinement costs about 10 ms a picture.

  We learnt it again from twice the films, that is 26 more official 3D
  releases and 7,423 pictures. The validation error is 6.80 px against
  6.85, and every score moves a little the right way. The benchmark's
  right eye is 27.46 dB either way, with VMAF 29.5 to 29.6. The glass
  clips go from 26.8 to 26.9, VMAF 33.4 to 34.6. Furiosa is 27.88 either
  way, with correlation 0.881 to 0.885. Avatar: Fire and Ash goes from
  24.39 to 24.40. Those weights are built in. More pictures of the same
  kind help little. What is left is not what the films show.
- **Tuning the depth network itself does more.** The network's last
  stages were learnt anew from the same 7,423 pictures. Those stages are
  the DPT head's two finest fusion blocks and its output convolutions, 36
  of its tensors; the encoder and the temporal modules were kept. The
  command is `tools/depthtrain -tail`, from the inputs that
  `tools/stereobench -samples` records, and it runs on the GPU in 25
  minutes. The loss is the refinement's plus a term that keeps each
  picture's depth's mean and spread at the network's. The validation
  error after fitting went from 7.9 to 6.35 px; the refinement's was
  6.80. The tuned depth wants its own mapping, `stereo.TunedShape`, with
  0.12% a unit and the farthest point 0.90% behind the screen. It was
  re-found on the twelve clips as before. The refinement that was learnt
  on the old depth only loses on the tuned one, so it stays off. Here is
  the right eye's PSNR / VMAF against the refined depth:

  | | refined | tuned |
  |---|---|---|
  | the twelve clips (the mapping chosen on them; one picture in 8) | 27.36 / 31.0 | 27.31 / 30.0 |
  | the four glass clips | 26.94 / 34.6 | 27.79 / 41.9 |
  | Furiosa | 27.88 / 31.3 | 28.57 / 34.9 |
  | Avatar: Fire and Ash | 24.40 / 25.3 | 24.46 / 25.5 |
  | depth correlation (twelve, glass, Furiosa, Avatar) | .834 .768 .885 .730 | .848 .791 .877 .752 |

  The tuned depth wins on every set it was not chosen on, and on the
  glass most. The conversion is faster too, since there is no
  refinement: about 120 pictures a second at 1080p on an RTX 5090. It is
  built in and on by default for Small.

  We learnt it again without the fans' conversions. See "Against other
  converters": 14 of the 53 films were left out, leaving 5,454 pictures.
  The recipe was the same, and so was the mapping, which was still the
  best of a 3 x 3 grid about it. The validation error went from 6.35 to
  6.30 px. One picture in 8:

  | | 53 films | 39 films |
  |---|---|---|
  | the twelve clips | 27.31 / 30.0 | 27.37 / 30.4 |
  | Avatar: Fire and Ash | 24.45 / 27.4 | 24.50 / 27.8 |
  | the four glass clips | 27.65 / 42.7 | 27.45 / 40.9 |
  | depth correlation (twelve, Avatar, glass) | .849 .747 .791 | .851 .745 .774 |

  Less data, and a little better where the films are many. The glass,
  which is four shots of one film, is a little worse. These weights are
  built in.
- **A wider benchmark, and the tail fitted a stretch at a time.** Fifty
  clips of 4 seconds, at 25 and 60 minutes, from twenty-five more
  official 3D releases were added beside the twelve. They are native,
  studio-converted and animated, and none is in any training set. They
  live in `~/mvc-scratch/3dbench/wide`. On them the depth's scale and
  place matter more than on the twelve. With the tail above and one
  picture in 8, the disc's own depth makes the right eye at 38.4 dB. Ours
  fitted to it a picture at a time gives 34.0, fitted once a clip 32.0,
  and as the fixed mapping places it 28.4, VMAF 34.6. That last is under
  a flat picture's 28.8 and 46.4. So the loss from a fixed scale and
  offset, 3.6 dB, is bigger than the loss within a clip, 2.0, and the
  depth's own errors cost the rest, 4.4. We tried these against it:
  - *More input resolution*, 700 and 924 on the short side instead of
    518. The twelve's correlation went from 0.851 to 0.847 and 0.839.
    Only the glass clips gained, 0.774 to 0.820. Not a lever.
  - *The network run on the mirrored picture too, the two depths
    averaged.* Fitted, +0.1 dB on every set; our own eye +0.03. That is
    at twice the network's cost. Not kept.
  - *The tail learnt again from 35 of the real releases now in hand*,
    5,172 pictures, the same recipe. The validation error went from 6.30
    to 6.29 px and every set stayed within 0.05 dB. The data was not the
    limit.
  - *The tail fitted to the disc a stretch at a time*, with
    `tools/depthtrain -fit stretch`. One scale and offset covers the
    2-second stretch's pictures, so the depth's scale has to hold across
    them. The picture-at-a-time validation error is worse, 6.39 px, as it
    must be. But the eye fitted once a clip is better on every set: the
    twelve 29.3 to 29.5 dB, the glass 28.5 to 29.0, the wide set 32.0 to
    32.1. Its jitter is lower too, 1.15 to 1.03 px on the wide set. With
    its mapping re-found, 0.13% a unit and the far end 0.90% behind the
    screen, the best of a 3 x 3 grid about the old on all three sets at
    once:

    | right eye PSNR / VMAF | picture-fitted tail | stretch-fitted tail |
    |---|---|---|
    | the twelve clips (one picture in 8) | 27.37 / 30.4 | 27.35 / 31.1 |
    | the four glass clips | 27.45 / 40.9 | 28.11 / 45.6 |
    | the wide set (fifty clips, twenty-five films) | 28.42 / 34.6 | 28.59 / 35.4 |
    | Avatar: Fire and Ash (25 clips) | 25.74 / 31.1 | 25.83 / 31.6 |
    | depth correlation (twelve, glass, wide, Avatar) | .851 .774 .826 .745 | .848 .776 .819 .747 |

    These weights are built in. Ten epochs instead of six left the twelve
    and the wide set the same within 0.05 dB and made the glass 0.2 dB
    better. That was not worth the pull towards one film. Avatar's row is
    both tails measured now. The picture-fitted tail's 24.50 / 27.8 above
    was measured in October, and the 10-bit source's path has changed
    since.
  - *The mapping's far end deeper*, as the discs are. Their 5th percentile
    is -23 px at 1920, and ours is pinned at the offset, -17. Every step
    of 0.15% deeper loses 0.3 to 1.5 dB on every set. The discs put their
    far end anywhere from -5 to -52 px a clip, a choice their pictures do
    not tell, as the grading above found. A fixed mapping that reaches
    for it misses more than it gains.

  Measuring the discs directly, rather than clips cut from them, found
  two faults in the bench. Both are fixed. A disc whose title is split
  into many clips, as Disney's are, was read only to the first clip's
  end. A disc whose base view is the right eye, Blade Runner 2049, was
  measured with its eyes swapped. Its depth correlation was -0.7 and is
  now 0.93.
- **The whole head learnt, not just its tail.** The network's head is the
  four fusion blocks and the four temporal modules, 164 tensors and 6.9M
  values, against the tail's 36 and 2.3M. It learnt from the same 37
  releases. Each picture came with the seven before it, as the network
  took them. `tools/stereobench -samples -window 8` records them, and
  `tools/depthtrain -head -learn head -fit stretch` learns. The frozen
  encoder runs on every frame, and the head runs on the current one with
  its gradients taped. It took 4 passes at a tenth of the tail's rate.
  The validation error went from 6.39 to 6.04 px. At the tail's mapping
  the eye was better on the twelve, 27.73 dB, and on the wide set, but
  worse on the glass clips, 27.56, and on Avatar, 25.61. The porthole's
  reflected Earth read as geometry, and the visor's bright Earth limb
  read as a near edge. Most of that loss was the mapping, not the depth.
  The depth fitted to the disc was as good or better on those sets,
  Avatar's error 4.13 to 3.99 px; its range was just wider. With the
  mapping re-found, 0.115% a unit and the far end 0.82% behind the
  screen, by a coarse and then a fine grid on all four sets:

  | right eye PSNR / VMAF | stretch-fitted tail | whole head |
  |---|---|---|
  | the twelve clips (one picture in 8) | 27.35 / 31.1 | 27.63 / 32.0 |
  | the four glass clips | 28.11 / 45.6 | 28.23 / 48.1 |
  | the wide set (fifty clips, twenty-five films) | 28.59 / 35.4 | 28.66 / 36.2 |
  | Avatar: Fire and Ash (25 clips) | 25.83 / 31.6 | 25.65 / 29.2 |
  | depth correlation (twelve, glass, wide, Avatar) | .848 .776 .819 .781 | .860 .708 .831 .776 |

  Avatar wants a shallower mapping than the rest, 0.10% and 0.60%. There
  it gives 25.90 / 32.8 with either set of weights, and the other sets
  lose a dB or more. One fixed mapping is a compromise between films, and
  Avatar is the film that loses by it. Before the mapping was found to be
  most of the glass loss, we tried three things against that loss.
  Learning the tail and the third fusion block only kept the porthole's
  order, but the visor clips still lost. Holding the depth to the
  network's own where the disc is unsure, or everywhere, is `-keep` and
  `-hold`. At a weight that matters they fight the fit nearly everywhere,
  since the matcher is unsure over much of each picture, and they lose on
  every set. Pulling the weights back towards the checkpoint's is
  `-anchor`. The learnt weights had moved only 6%, less than the tail's
  16%, so drift was not the cause. The head's weights are built in, and
  the binary is 25 MB bigger for them. We found one more thing on the
  way. The recorder kept a window's frames across a cut or a change of
  the bars, where the converter restarts the network, so 236 of 5,165
  windows held frames of two sizes. That is fixed.
- **The encoder's last blocks learnt too: a better depth, not a better
  eye.** With the encoder's gradients on the tape, `tools/depthtrain
  -learn blocksN`, the whole head and the last two blocks learnt as the
  head had. That is 194 tensors and 10.5M values, 4 passes at 1e-5, 0.35
  s a picture. The validation error went from 6.04 to 6.07 px, and was
  6.02 after three passes. The training loss was 2.13 against the head's
  2.49, so the blocks begin to learn the 37 films. The depth was better
  by every fitted measure where it was weakest. The glass clips'
  correlation went from 0.71 to 0.81 and their fit error from 3.38 to
  3.00 px; the wide set went from 0.83 to 0.84. But no mapping in a
  nine-point grid gives a better right eye than the head alone. The glass
  clips lose 0.8 dB at every point, and the rest stay within 0.1. Each
  retrain moves the depth's range, and a fixed mapping has to be re-found
  and compromised between films, as the head's was against Avatar. We
  tried two things against that:
  - *The mapping as the loss*, with `-fit fixed`. The depth is mapped as
    the converter maps it, and its residual to the disc is the loss, with
    no fit. The network cannot learn the per-shot part of the mapping,
    which is the stereographer's and which the probes above found
    unpredictable. So the gradient is noise, and it wrecks the depth. The
    validation error with the mapping went from 9.48 to 9.22 px, but
    after fitting it went from 6.04 to 10.26. The twelve clips went from
    27.63 to 26.27 dB, and the glass correlation from 0.71 to 0.44. With
    the encoder's blocks it was the same, 26.52 dB.
  - *The mapping's residual added to the fit's at a weight*, with `-map
    0.3`, the blocks, and 3 passes from the head's weights. This gave the
    best validation error of any run after fitting, 5.97 px, and nothing
    with the mapping, 9.55. The twelve clips 27.27 / 28.1, the glass
    26.90 / 38.8, the wide set 28.82 / 36.9, Avatar 25.56 / 29.8.

  So the head's weights of the row above stay built in. The round says
  three things. The depth network can still learn, since the fitted
  measures keep improving. The fixed mapping is the lid on what the eye
  gains from it. And the mapping's per-shot part is not the network's to
  learn. The gain the probes measured for one mapping a shot, half the
  mapping's residual, has to come from somewhere other than a picture's
  content.
- **Whole films the choices never saw.** Furiosa and Avatar: Fire and Ash
  were each converted whole from its 3D disc's left eye, beside the disc's
  own 3D, and scored on clips through the film, 24 and 25 of 4 seconds.
  Furiosa was taken for a studio conversion. It is not one; see below.
  Avatar: Fire and Ash was shot in 3D.

  | | Furiosa | Avatar: Fire and Ash |
  |---|---|---|
  | depth correlation, refined (not) | 0.88 (0.89) | 0.73 (0.70) |
  | right eye, ours, refined (not) | 27.9 / 31 (28.1 / 32) | 24.4 / 25 (24.4 / 25) |
  | fitted per picture, refined | 31.7 / 56 | 29.9 / 53 |
  | fitted per clip, refined | 30.2 / 47 | 28.1 / 40 |
  | flat; the disc's depth | 27.7 / 35; 37.9 / 87 | 26.7 / 42; 33.7 / 83 |

  The depth's shape is close to a studio's, at 0.88, and less close to
  two cameras', at 0.73. The refinement helps the one and costs the other
  a little. The larger loss is the grading. Both films spread their depth
  much wider than our mapping does. Their 5th to 95th percentile is 35
  and 43 px at 1920, and ours is 21 and 20. One fit a clip already gets
  most of that back, +2.3 and +3.7 dB.

  **Correction:** Furiosa never had an official 3D release. Its "3D disc"
  was an automatic 2D-to-3D conversion made by fans. On 5 of its 14
  clips, what each eye sees behind a nearer thing's edge is stretched in.
  Its detail across rows there is 0.03 to 0.15 of the rest's, where real
  pairs are at 0.64 to 1.13. And its depth range is the same on every
  clip, the nearest at -1.15% to -1.35% of the width, which is what a
  converter that normalises each picture gives. So every Furiosa number
  here measures the agreement with another converter, not with a film's
  3D. The training films were screened the same way; see "Against other
  converters". One more caution. A made eye scored against the disc's
  rewards less depth where the depth is unsure, so these scores alone do
  not say how much depth looks best.
- **Motion parallax adds little to the tuned depth.** The network sees the
  pictures before, through its temporal modules, but it measures nothing.
  We probed with `tools/stereobench -parallax 2,4,8`. Each picture's
  optical flow is taken against one 2, 4 or 8 before, by pyramidal
  Lucas-Kanade, and kept where forward and backward agree. The camera's
  rotation and the dominant plane's motion are taken out with a
  homography. What is left is read along the lines through the epipole as
  relative inverse depth. Where the camera moved most, 8 pictures apart,
  we fitted the disc's disparity by the network's depth and that parallax
  together, with the weight chosen per picture against the disc, so the
  result is an upper bound. Against the network's depth alone, the twelve
  clips went from 2.71 to 2.16 px, Furiosa from 2.07 to 1.98, and Avatar:
  Fire and Ash from 2.54 to 2.42. The parallax alone correlates 0.3 to
  0.46 with the disc's, where the network does 0.82 to 0.95. Moving
  people, water and smoke break its one assumption, and pans and still
  shots have none. As measured, it is not worth fusing. A learnt flow, or
  the parallax as an input the tail learns to weigh, might be.
- **Bigger networks are not better here.** Video Depth Anything Base and
  Large run in the same code, as `--depth-model base` and `large`; they
  are CC-BY-NC-4.0. On the twelve clips Base's depth correlates better,
  0.83 and refined 0.86, against Small's 0.79 and 0.83. With its own
  mapping, `stereo.BaseRefinedShape`, its right eye is 27.7 dB and VMAF
  30, against Small's 27.5 and 29.5. But on the glass clips and on both
  whole films it is worse. Furiosa is 27.4 / 29 against 27.9 / 31, and
  Avatar is 24.1 / 22 against 24.4 / 25. And it takes twice the time.
  Large's depth is no better than Base's, at 0.80, and it takes five
  times Small's time.
- **Depth Anything 3 is not better here either.** We scored other
  networks' depth of the clips' left eyes, from iw3's export through
  `stereobench -depth-dir`, each picture's fitted to the disc's. The
  table gives the correlation, the error left after fitting a curve, and
  the fitted right eye, on the twelve clips, with the glass clips in
  parentheses:

  | depth | correlation | error | fitted right eye PSNR / VMAF |
  |---|---|---|---|
  | ours (Small, tuned) | **0.850** (0.772) | **2.34** (2.76) | **31.29 / 50.7** (29.45 / 53.7) |
  | Depth Anything 3 Mono Large | 0.815 (0.599) | 2.57 (3.05) | 30.66 / 47.5 (28.95 / 55.0) |
  | Video Depth Anything Large | 0.795 (0.664) | 2.91 (3.22) | 30.68 / 48.5 (28.50 / 53.2) |
  | Video Depth Anything Small | 0.785 (0.790) | 2.96 (2.79) | 30.21 / 44.3 (29.06 / 53.4) |

  Untuned Small's depth agrees with ours before the tuning, at 0.79, so
  the export measures as ours does. DA3 leads the others on the films but
  not the tuned Small, and it loses the glass. It is a network of
  pictures, not of video, and it is 13 times Small's size.
- **Soft edges do not pay.** We blended the nearer side of each hole into
  its fill over one or two pixels, since an edge's pixels mix the nearer
  thing with what was behind it. Rendered with the disc's own depth, the
  right eye was 36.31 dB and VMAF 81.0 before, 36.18 / 80.4 blended over
  one pixel, and 36.02 / 79.1 over two. The glass clips went from 35.43 /
  86.2 to 35.26 / 85.5 and 35.04 / 84.2. With our depth, nothing changed.
  The discs' own edges are sharp.
- **The tensor cores lose nothing measurable.** The network's matrix
  products take their inputs in half precision and sum in single. The
  depth is 0.02% of its range off PyTorch's, where single precision is
  0.002% off. Every benchmark measure is the same to the second decimal,
  and the network is a third faster. Large's activations overflow half
  precision, giving NaN on glass, so it keeps single.
- **The matcher** trusts a match only where the picture has texture.
  Without that rule, space and night skies matched the grain, which two
  cameras do not share.

## Against other converters

`stereobench -made NAME=DIR` scores another converter's pairs beside
ours. Each pair is made from the clips' left eyes, with the left eye
kept. The made right eye is scored against the disc's by PSNR and VMAF.
Each pair is also measured as a pair, which no depth budget or screen
place favours. Three measures do that. The disparity measured between
its eyes is fitted to the disc's, which gives a correlation and the error
left. The detail the worse eye has where it sees past a nearer thing's
edge is compared with the rest of the picture. It is about 1 in a real
pair and low where the fill is stretched in, and it says little where
little is revealed. And the jitter of its fitted disparity from picture
to picture is compared with the disc's.

iw3 is part of nunif, and as of October 2026 it is the most used open
converter. We ran it on the sixteen clips at three settings. The first
is its defaults. The second is Video Depth Anything Large, which is 13
times our network and non-commercial, with its two-layer warping
(mlbw_l2), depth normalisation and scene detection. The third is Depth
Anything 3 with its default warping. In the table, `bdtools` is the
command as it runs, that is `--to-3d` with the depth at 1.5 and both eyes
made. `ours` is the benchmark's run, with the depth at 1 and the right
eye made.

| | right eye PSNR / VMAF | pair corr / error | detail (revealed) | jitter |
|---|---|---|---|---|
| ours | **27.49 / 31.9** | **0.838** / 3.15 | 0.99 (0.10%) | 1.19 |
| bdtools | 27.31 / 30.4 (both eyes moved) | 0.836 / **3.06** | 0.90 (0.31%) | 1.21 |
| iw3, Video Depth Anything Large | 26.79 / 26.7 | 0.804 / 3.19 | 0.71 (0.36%) | **1.12** |
| iw3, Depth Anything 3 | 27.06 / 28.7 | 0.788 / 3.24 | 0.84 (0.21%) | 1.20 |
| iw3, defaults | 24.15 / 11.4 | 0.785 / 3.87 | 0.99 (0.25%) | 1.19 |
| the disc's depth, our renderer | 36.09 / 82.3 | 0.966 / 0.64 | 0.36 (1.21%) | |

Ours leads on the depth's shape and on the made eye. iw3's Large setting
jitters a little less. The converters reveal too little here, 0.1% to
0.4% of the picture, for the detail to tell them apart. Where our
renderer is given the disc's depth it reveals 1.2%, and there its filling
shows what it is, a stretch: 0.36, where real pairs are 0.64 to 1.13.
iw3 converts at about 5 pictures a second here, and ours at about 110.
VisionDepth3D is no longer open.

StereoCrafter v2 does diffusion inpainting with 14B parameters. We ran it
on four of the clips. The depth was Video Depth Anything Large's, from
iw3's export, since its own DepthCrafter weights are gated. Then came its
splatting and its inpainting, with the transformer split between the
GPU's 14 GB and memory. It took 39 minutes for a clip of 7 seconds. It
does not keep the left eye; it is 41 dB against it.

| | right eye PSNR / VMAF | pair corr / error | detail (revealed) | jitter |
|---|---|---|---|---|
| ours | 26.52 / **26.9** | 0.806 / 3.93 | (0.10%) | 1.45 |
| iw3, Video Depth Anything Large | **26.69** / 25.2 | **0.831 / 3.35** | 0.84 (0.38%) | **1.41** |
| StereoCrafter | 26.22 / 24.4 | 0.759 / 3.83 | 0.51 (2.24%) | 1.63 |

Its inpainting fills more, 2.2% revealed, and better than a stretch, but
no better than a real pair's, at 0.51. On Mad Max it pulls the depth off
the disc's, 0.61 against 0.89, and its right eye is 32.9 dB against our
34.5. Only on Gravity does it beat ours, 28.0 dB against 26.9. It is
behind both on the made eye, at some 1,500 times our time.

**The training films screened.** Several films in the library are fans'
automatic conversions sold as 3D. We ran the detail test above on each
eye on its own, on eight excerpts a film. Fast X, Godzilla vs. Kong,
Godzilla x Kong: The New Empire and Trolls World Tour have an eye
stretched in on six to eight of the eight, with its detail at 0.003 to
0.04. They, three more from the same release group, and seven films no
longer in the library are left out of training from now on. That leaves
39 of the 53.

## The credits

The 3D discs put their credits on the screen, flat. We measured ten
discs' end credits with `stereobench`, 4 seconds at 150 and 400 seconds
before the end. Eight are at 0 disparity within a pixel, and Frozen II is
at a constant -8 px. Where there is something behind the text, such as
Force Awakens' stars or Life of Pi's drawings, it goes behind and the
text stays at 0. The depth network gives text no such treatment. The
letters come out at their own depths, the black around them at something
else, and the whole sits 3 to 5 px behind the screen, unevenly.

So a picture is taken for the credits by two tests on its active rows.
At least 80% must be black, meaning luma 40 or less. And at least 60% of
the rest must lie in runs across a row of at most 24 pixels with black
either side, which is how letters' strokes look. This is
`stereo.Credits`, and `-credit-features` prints the figures. The discs'
credits are 0.85 to 0.97 black and 0.69 to 1 thin. The benchmark's and
glass clips' pictures, Gravity's space among them, are at most 0.77 black
and 0.32 thin. Such a picture is made flat, on the screen. At a cut this
happens at once. Within a shot it happens once 24 pictures in a row say
so, eased over 12.

| the end credits, 18 clips | right eye PSNR / VMAF |
|---|---|
| ours, the credits as any picture | 18.51 / 22.8 |
| ours, the credits flat | **36.33 / 71.1** |
| flat at the disc's own median | 40.28 / 88.7 |

Eight discs' credits are matched exactly. Frozen II's lose, 19.0 to
15.0, because the disc has them at -8 px and we put them at 0. Pictures
framed by the credits, such as Hugo's room and Forky, and drawn credits,
such as Incredibles 2's, are not taken for credits and keep their depth.
Here that does worse than flat: 23.6 against 30.8, 28.0 against 32.0,
15.5 against 22.8. That is the network on pictures that are mostly black,
which is a matter for the depth, not for the credits. The benchmark's
films and the glass clips are unchanged to the last decimal.

## The screen's size

What is behind the screen is shown with the right eye's picture to the
right of the left's. Past the eyes' own separation, which is about 63 mm,
the eyes would have to turn outwards, and they cannot do that
comfortably. On a television that limit is far beyond anything made
here. A 65-inch screen is 1.44 m wide, so 63 mm is 4.4% of it, past the
4% of the width that all disparity is kept within. On a projector's
screen it is not. A screen ten metres wide puts 63 mm at 0.63%, which is
12 pixels of a 1920-wide picture, where the films' backgrounds sit at 15
to 25. `--3d-screen-size` takes the screen's diagonal in inches and keeps
everything behind the screen short of that limit. Past 70% of the limit
the depth is eased. What lies beyond that point, x, becomes x/(1+x/r),
with r the rest of the way to the limit. This leaves the slope unchanged
where the easing starts and never reaches the limit, so the background
keeps its order and shape instead of being flattened against a wall.

## Previews

`--3d-preview` makes no film. It makes stills of what `--to-3d` would
make, into `--output` as a directory, so that you can choose the depth
before hours of conversion. It picks eight places spread over the film,
or over `--start` to `--end`, found by their bytes as `--crop auto` finds
its places. At each place the pictures are decoded from a keyframe on,
then cropped, scaled and tone mapped as the conversion would do, and
given to the depth network one after another. The seventeenth picture is
the one used, because by then the network's view has settled over the
pictures before it. It is made a pair at `--3d-depth` 1, 1.5, 2 and 2.5,
and at the depth given if that is another. Each pair is written twice,
as JPEGs named by place, time and depth. One is full side by side,
`-sbs.jpg`, for a headset or a 3D screen. The other is a red-cyan
anaglyph, `-anaglyph.jpg`, for any screen and glasses. The window is
each still's own, as a cut's first picture's would be. A 4K Dolby Vision
film takes about 25 seconds on an RTX 5090, and a 1080p one about 15.

## The holes

Where the right eye looks past a nearer thing's edge, it sees what the
left eye never did. That is a hole. At a disc's depth the holes are about
6% of the picture, a few pixels wide at every edge. The renderer fills a
hole by carrying the farther side's last pixel across. That measured
better than mirroring the texture, 29.8 to 31.6 dB in the holes of a
disc-depth render, but it reads as a smear. A learnt filler does better.
It is `internal/stereo`'s `InpaintWeights`, a U-Net of the refinement's
shape at the picture's full size. It takes the rendered eye and the mask
of its holes and gives a correction, which is taken in the holes only.

Its training data are the discs' own right eyes. `stereobench -holes DIR`
renders the right eye from the left by the network's depth fitted to the
disc. That gives holes of the shape a conversion makes, where the disc's
geometry puts them. The disc's own disparity would not do, because it
opens holes a pixel wide everywhere, which are the matcher's noise, and
they taught nothing. The recorder drops holes a pixel wide and those at
the frame's sides, and writes 384-pixel crops around the densest holes,
with the disc's eye there. `tools/inpainttrain` learns the filler from
them, with a squared error in the holes and Adam, and prints the holes'
PSNR as rendered and as filled. `stereobench -fill FILE` scores it on the
benchmark's clips, and `--3d-fill FILE` uses it in a conversion. The
GPU's eyes kernels give the holes' mask.

**First weights (2026-11-13, 7917 crops of 37 films, 8 passes).** The
validation crops' holes went from 20.7 to 23.2 dB. On the benchmark
clips, which were held out, with `-every 8`, the holes of our own render
go from 20.91 to 21.75 dB, and those of the fitted-depth render from
22.36 to 23.94. On the glass clips ours lose half a dB, 19.71 to 19.21,
because behind a reflection the disc's truth is not to be had, while the
fitted gain 1.5. Holes are about 1% of the picture, so the whole
picture's PSNR and VMAF do not move. To the eye, the renderer's comb-like
streaks along edges become a soft fill near the disc's background. The
cost is the two eyes' passes at the picture's full size. With the
filler's input made and its corrections applied on the GPU beside the
network, so that nothing crosses the bus, a conversion runs at about 50
pictures a second against 100 without. So the filler is an option, not
the default yet. The next steps are crops of both geometries, ours and
the fitted, for its training, and the network's passes on the tensor
cores.

## Grading by hand

No automatic depth gets every shot right. `--3d-shots FILE` grades
stretches of the film by hand. Each line of the file is a stretch:

```
# START [END] SETTINGS...
0:12:30 0:13:05 depth=0.8      # a close-up that comes out too far
1:02:10.5 1:02:40 flat         # a screen within the picture
1:30:00 screen=-1              # to the next line's start, or the end
```

Times are the film's, as a player shows them, and that stays true when
the command has a `--start`. A line without an end
runs to the next line's start. `depth=`, `screen=` and `curve=` set
`--3d-depth`, `--3d-screen` and `--3d-curve` for the stretch. `flat`
puts the stretch on the screen, as the credits are. What a line does not
set stays at the command's value, and where lines overlap the later one
wins. The change happens at the picture the time names, so a stretch
should begin and end at cuts. A stretch that has to begin within a shot
takes `ramp=1`, in seconds. Its depth, screen and curve then ease in from
the stretch before's over that long, which the eye does not notice where
a jump would show. `--3d-preview` shows what each depth looks like
beforehand. The file is part of what an interrupted conversion must match
to resume.

A whole film may want its own grade. The discs' mappings differ between
films more than any picture tells, as the mapping probes above found,
and the built-in mapping is the compromise that scores best across the
sets. Avatar: Fire and Ash, say, scores best at 0.87 of the built-in
depth with the screen 0.17% of the width nearer, 25.90 dB against 25.65.
Against the defaults that is `--3d-depth 1.3 --3d-screen 0.17`. Where a
film has a 3D release of its own, that release is the way to find its
grade, as `tools/stereobench` does.

## Grading live

Numbers in a file are a poor way to grade by eye. `--3d-grade ADDR`
converts the film as it plays and serves it as a 3D stream for the
player at the 3D screen, with a web page to drive it.
[grading.md](grading.md) is the manual. It covers the process, the page
and the headset, control by control. This section explains what the
server does and why.

```sh
bdtools --input "Film (2020).mkv" --output "Film (2020).3d" --to-3d --3d-grade :8420
```

The stream is at `http://HOST:8420/stream.ts`. It is full side by side
at the source's size, 1080p an eye, as H.264 in a transport stream, which
Kodi, VLC and most players read live. On a box at the projector, a
`.strm` file holding that address plays it like any film. Name it with
the library's 3D tag, `Film 3D FSBS.strm`, so that the player picks the
mode.

The page is at `http://HOST:8420/`. It has the transport: play, pause, a
picture or ten seconds either way, the cut before or after, and a click
on the timeline to seek. It has the knobs. Depth and screen are
`--3d-depth`'s and `--3d-screen`'s. Curve is `--3d-curve`'s. It bends the
depth within the shot's range by raising its nearness to that power, so
a curve below 1 brings forward what lies between the shot's farthest and
nearest. That is for the shots a network stacks away, such as the rows
of a crowd seen from below, which the disc has side by side. And there
is `flat`, for a shot that belongs on the screen. A paused picture keeps
its depth, so a turn of a knob shows at the next picture's time. The
only wait is the player's own buffer. Kodi's is a few seconds by default
and under one with its cache turned down.

The page shows the stream itself above the knobs, as the left eye, both
eyes, or a red-cyan anaglyph for a depth check on a laptop. It runs a
tenth of a second behind. The stream also carries a readout drawn into
the bottom of both eyes, at the screen's depth. It shows the time, the
play state, the knobs, the stretch and the view, so that the projector
and a headset show the state while a hand turns the knobs. A box on the
page turns it off for clean viewing.

A turn of a knob grades a stretch. The stretch starts from the last cut
when one is within two seconds, because a hand reacts to a shot a moment
late. Otherwise it starts from the picture shown and eases in over a
second, which is `ramp=1`. It runs to the next stretch or the end. The
grade is written to `--output` as a `--3d-shots` file at every change,
and the conversion proper applies it with `--3d-shots`. `--3d-shots` on
the grading command starts from a file written before. The page also
shows where the picture's nearest and farthest come out, in percent of
the width and in pixels, the cuts seen so far on the timeline, and the
stretches.

A headset grades without a phone in hand. The same page is at
`https://HOST:8421/` in the Quest's browser. That is the server's HTTPS
side, on the next port, with a certificate of its own making that the
browser is told to accept once. WebXR runs on a secure page only, and a
plain HTTP page on a LAN address is not one. The HTTPS page shows an
Enter VR button. It puts the picture on a 4 m screen, each half to its
eye, with the page's controls on a panel to the right of it. The right
controller's ray puts a cursor on the panel and the trigger presses what
is under it: the transport, depth and screen by steps, flat, the views,
the stretch, save and the timeline. The panel's foot says what the
cursor is over and when to use it, as the page's controls do when
hovered.

In VR the page decodes the stream itself. It reads `/stream.raw`, the
frames with their times, through WebCodecs, and draws each through
WebGL, paced by their times. We do it this way because a video element
behind a VR session is composited at a few frames a second by the
browser, which starved both its media layer and a copy of it. The page
asks the headset for a 72 Hz display, where 24 pictures a second sit
evenly. Both grips held for a second leave VR. What the page sees goes to
the server's log through `POST /api/log`: the browser, each step into
VR, errors, and the frames decoded and shown. The controllers turn the
knobs too. The right stick up and down is the depth, and left and right
the screen. The right trigger plays and pauses when it is off the panel.
The right grip makes a shot flat. A and B step a picture. The left stick
jumps between the cuts, and X and Y jump ten seconds.

Where the film has a 3D release of its own, an MVC disc converted, say,
`--3d-reference FILE` puts it beside the conversion. The page's `disc`
button switches the stream to the reference's pair at the same moment,
and back. The key is `d`, or a headset's left trigger. The two files'
clocks differ when their discs do, so `align` finds the picture shown in
the reference, eight seconds either side, and keeps the offset. The page
says when the reference has no picture at a place. The reference must be
side by side or over-under at the conversion's size.

The sharpest test needs no second file. Give a 3D file itself, a
side-by-side or over-under conversion of an MVC disc, as the input of
`--3d-grade`. Its left eye is the picture converted and its own pair is
the reference, on one clock. So `disc` flips between the stereographer's
right eye and ours, made from the same left eye. A 3D disc image is not
taken directly. Give its conversion, or its base view from `--remux
--2d`. Bear in mind that the disc's right eye is a second camera. It
sees behind edges where ours cannot, so judge the depth of the scene,
not the edges.

Two more views make the comparison exact. `mix`, the key `m`, keeps the
left eye and blends the disc's right eye with ours half and half. Where
the two depths agree the picture fuses. Where they differ it doubles, by
exactly the difference, and you turn the knobs until the doubles merge.
`diff`, the key `x`, shows both eyes the difference of the two right
eyes, mid grey where they agree and an embossed edge where they do not.
It is the sharper tool for a small mismatch, and it shows at a glance
which of the near and the far is off. `o` goes back to ours.

The depth network runs as the film plays, since it is faster than the
film on a GPU. Nothing is precomputed, so any film opens at once. The
stream needs NVENC. The eyes are H.264 for the players' sake; the
conversion proper encodes as asked.

## Milestones

1. **Depth in Go** (done). `internal/nn` on the CPU, and Video Depth
   Anything Small from its published checkpoint. It gives the same depth
   as PyTorch, within 1e-3 on film frames.
2. **A first conversion** (done). `--to-3d`, with cut detection, the depth
   shaped into disparity, the eyes rendered nearer over farther, and the
   holes filled from the background. The filling carries the background's
   edge across, which measured better than mirroring its texture. Every
   3D layout comes out.
3. **The benchmark.** `bdtools 3d-bench` on clips of native 3D discs. The
   left eye is converted and its made right eye is compared with the
   disc's in three ways:
   - The disparity. The made one is compared with the one measured
     between the real eyes, by a semi-global matcher in Go, after fitting
     its scale and offset, since a conversion need not copy the disc's
     depth budget.
   - The picture. PSNR and VMAF of the made right eye, overall and where
     holes were filled.
   - Other tools, such as iw3, run on the same clips, for a yardstick.

   Every later milestone is kept only where this says it helps.
4. **The GPU backend** (first step done). The depth network runs on CUDA.
   `internal/cuda` is the driver API through purego, and `internal/nn`'s
   GPU backend has kernels.cu compiled to PTX ahead of time. It gives the
   same depth as PyTorch within 1e-3, at 25 ms a 1080p frame on an RTX
   5090, 50 times the CPU. Since then, the depth of one frame is made
   while the last is rendered. The tensor cores brought it to 16.5 ms.
   The networks' inputs are made on the GPU, attention runs in one pass,
   the eyes are rendered on the GPU, and tiles fitted to the GPU brought
   it to 7.2 ms. A film converts at about 110 frames a second. Still to
   do: the matrix products' tiles fitted to the GPU, since they run at a
   tenth of its rate.
5. **Depth edges.** The network's soft depth edges are snapped to the
   picture's by an edge-aware filter guided by the full-resolution
   picture. This is against halos and cardboard cut-outs.
6. **Shots planned whole.** A conversion is not live, so each shot can be
   read before it is made. Its depth is graded from the whole shot's
   statistics, smoothed forwards and backwards. Its holes are filled from
   the shot's other frames wherever they show the background, and only
   what no frame shows is inpainted.
7. **Like a stereographer.**
   - Glass and reflections in layers; see
     [Measured so far](#measured-so-far).
   - Floating windows (done). These are black masks moving at the
     picture's sides, where something in front of the screen meets the
     frame's edge. `--3d-window` makes the strip as wide as the disparity
     at that edge, widening 2 pixels a frame and narrowing 0.5.
   - Subtitles placed just in front of what they cover (done). The offset
     is the lower half's nearest point, followed at once nearer and slowly
     farther, as a disc's offset sequence, for `--subs-3d` and
     `--burn-subs`.
   - Screens, captions and graphics in the picture kept flat.
8. **Depth graded as the discs are** (tried; no gain). A regression of
   each picture's scale and offset on its features, learnt from 27 films,
   lost on the benchmark's held-out films; see
   [Measured so far](#measured-so-far). A film's place about the screen
   is its makers' choice, and within a film the features add nothing to
   the fixed mapping. What is left of this is a rising curve in place of
   the line, which is the depth's shape.
9. **A learnt refinement** (done for the depth, as `--3d-refine`; see
   [Measured so far](#measured-so-far)). A small network, trained in Go on
   the discs' real pairs, takes the picture and its depth and gives a
   corrected depth. It has since been outdone by tuning the depth
   network's last stages on the same pairs, `--3d-tune`, which is built
   in and the default; the refinement is then off. A wider refinement
   network, at 4 times the work, gained 0.3 VMAF and lost correlation on
   Furiosa, so it was not kept. Next: the refinement learnt again on the
   tuned depth, and more of the network tuned.
10. **Blu-ray 3D authoring** (first step done). `--output film.iso` writes
    a Blu-ray 3D image. Its playlist and clip infos are tsMuxeR's byte for
    byte for the same facts, in `internal/bdmv`. The clips' transport
    streams are sent earliest deadline first within their decoders'
    buffers. The two views are interleaved into an SSIF in a UDF 2.50 file
    system, `internal/udf`, laid out as a pressed disc's. libbluray, which
    Kodi uses, reads it whole, and the disc carries the encoded stream's
    every NAL unit. The subtitles' depth goes in as a disc's offset
    metadata, which is an MVC scalable nesting SEI in each GOP's first
    dependent view access unit, laid out as a commercial disc's byte for
    byte. Still to do: a check on a disc player, a 2D disc, and the offset
    metadata in an MVC Matroska file too.
11. **A 3D film's depth changed** (first step done). `--3d-depth` and
    `--3d-screen` work on a 3D source, through `stereo.Redepth`. The
    disparity is matched on the GPU, exactly as the CPU matches it, in
    8.7 ms a 1080p pair against 340, so a film converts at about 31
    pictures a second. On the benchmark's clips, halving the depth
    measures 0.46 to 0.50 of the disc's disparity afterwards, and a
    quarter less measures 0.74 to 0.75, with a median error of 0.4 px.
    The plan is this. A real 3D disc's depth is moved nearer or farther,
    by shifting one eye against the other, which is `--3d-screen` for 3D
    sources, or it is lessened. Everything else is kept: audio, subtitles,
    chapters. The disparity is measured between the disc's eyes and
    scaled, and the eyes are made again between the two cameras' views,
    so that both are blended and little is invented. The subtitles'
    offsets are scaled with it. More depth than the disc's means views
    outside the cameras', with `--3d-depth` up to 3, using the
    conversion's filling. We measured that against the disc itself with
    `redepthprobe -roundtrip`. Its pair is made 1/k as deep, then k times
    that, and the eyes are compared with the disc's, as the twelve clips'
    mean luma PSNR. At k 1.5 that is 47.8 dB, from 42.3 to 55.5, where
    the shallow pair's is 35.7. At k 2 it is 46.0 dB, from 40.4 to 53.9,
    where the shallow pair's is 33.2. The disparity is the matcher's both
    ways, so the trip is partly the tool against itself. What neither
    camera saw is the filling's, and it is small.
12. **Layered glass** (kept in mind, not now). A reflection and what is
    seen through the glass are separated by their motion, each with its
    own depth. The disc's own single depth already renders the glass
    clips far better than ours, so a second layer is not the main loss.

## Decisions for you

- **Licences.** Only the Apache-2.0 models: Video Depth Anything Small,
  and Depth Anything 3 Small and Base. The non-commercial larger ones,
  and DepthCrafter and StereoCrafter, would be opt-in downloads, if they
  are offered at all.
- **GPU first or CPU first.** The plan writes the CPU path first, to check
  correctness against PyTorch and for machines without NVIDIA. CUDA comes
  next. AMD and Intel GPUs through Vulkan, and Apple's through Metal,
  come later if wanted.
- **The default depth** (decided: 1.5). `--3d-depth 1` is the depth that
  scores best against the discs' right eyes. The discs' own disparity
  spans 1.7 to 2.2 times it, on Furiosa and Avatar: Fire and Ash, because
  a score punishes misplaced depth more than too little. The default is
  1.5 times it, and `--3d-depth 1` gives the scored best back.
- **Shipping refinement weights** (decided: built in). They are learnt
  from commercial 3D films. `--3d-refine none` leaves them out, and
  anyone may learn their own. The tuned network's weights are the same,
  with `--3d-tune none`.
- **Blu-ray authoring.** It is needed if a disc player is a target. It is
  a piece of its own, milestone 6, separate from the conversion.
