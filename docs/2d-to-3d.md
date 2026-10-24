# 2D to 3D conversion: a plan

A 2D film in, a stereoscopic film out: full or half side by side, full or
half over-under (at the source's resolution, or 1080p a view for players
such as the Ugoos), and MVC, both as the MKV CoreELEC plays and as a
Blu-ray 3D a disc player plays. As with everything else here, in Go: the
depth network, the view synthesis and the encoders, with the GPU driven from
Go as NVENC already is.

## Can a conversion look like a 3D film?

Two kinds of conversion are on the market, and they are not alike.

- **A studio conversion** (Titanic, Gravity's actors, Mad Max: Fury Road) is
  made by hand: artists cut every object out of every shot (rotoscoping),
  give each its depth and shape, and paint by hand what the second eye sees
  behind them. It looks like a 3D film: viewers rarely tell it from one shot
  in 3D, and the stereo check in `~/reencode/tools/stereocheck` could not
  either.
- **An automatic conversion** (what the fan releases are: JFC's, iFA's
  "AI3D", iw3's) estimates a depth map for each frame with a network and
  shifts the pixels by it. Today's video depth networks give a convincing
  depth for most shots: people and things stand out as they should, with
  their roundness, and the depth holds still from frame to frame. Where they
  fall short is what a hand conversion is paid for: hair and fur, glass,
  smoke, water and reflections get one depth where they need two; thin
  things (railings, wires) melt into what is behind them; the strips one eye
  sees behind an edge must be made up, and look smeared when they are; and
  a depth that is right but flat ("cardboard") or too strong tires the
  eyes.

So: yes, within limits. An automatic conversion can look like a good 3D
film for most of its running time and clearly converted in its hardest
shots. With a conservative depth (most of the picture behind the screen,
little coming out of it) the failures are mild; that is how the better fan
releases are made, and what this plan aims to beat. Hand-conversion quality
is out of reach for any automatic method.

## The state of the art (open source, late 2026)

- **Video depth networks.** Video Depth Anything (CVPR 2025) adds a temporal
  head to Depth Anything V2 and gives a depth that does not flicker over
  films of any length; its small model is Apache-2.0 (the base and large ones
  CC-BY-NC). Depth Anything 3 (November 2025) is newer; its small and base
  models are Apache-2.0. DepthCrafter (Tencent) is a video diffusion model,
  better on fine detail but far slower, and under a non-commercial licence
  inherited from Stable Video Diffusion. Apple's Depth Pro is per frame.
- **View synthesis and filling.** The pixels are shifted by the depth
  (depth-image-based rendering, "splatting"), and the holes this opens are
  filled. StereoCrafter (Tencent, weights released in 2026) fills them with a
  video diffusion model: the best-looking results published, but minutes of
  GPU time a second of film and a licence that is not free. iw3 (nunif) is
  the most used open-source converter: Depth Anything plus small networks of
  its own for the warping and the filling; fan releases are mostly made with
  it or with Owl3D (commercial).
- **Measuring it.** Mono2Stereo (2025) is a benchmark of stereo conversion
  against true stereo footage; recent work on "artistic disparity" (2026)
  grades the depth the way a stereographer does, not only by geometry.

The plan builds on the free models (Video Depth Anything Small first, Depth
Anything 3 Small/Base to compare) and on classical, fast hole filling with a
small learned filler as a later step, not on diffusion: a two-hour film must
convert in hours, not days.

## How it works

1. **Decode** the 2D source, as today (any source bdtools reads, HDR
   included).
2. **Cut detection.** Shots are found (the histogram and motion breaks the
   encoders already see); depth is never smoothed across a cut.
3. **Depth.** Each frame, scaled to the network's size (518 pixels on the
   short side), goes through the depth network, in windows of 32 frames for
   the temporal head. HDR sources are tone mapped for the network only (the
   tone mapper exists); the views keep the source's pixels.
4. **Depth finishing.** The depth is brought to the picture's resolution
   along its edges (a guided filter, so an edge in the depth sits on the
   edge in the picture), steadied in time where the network still wavers
   (motion-compensated, with the motion search the encoders have), and
   normalised per shot.
5. **Depth grading.** The relative depth becomes a disparity in pixels: how
   much depth in all (a percentage of the width, about 2 to 3% by default),
   where the screen sits (most of the picture behind it), how much may come
   out of it and how fast that may change from shot to shot, and floating
   windows where something in front of the screen meets the frame's edge.
   Options: `--3d-depth`, `--3d-screen`, `--3d-popout`.
6. **Views.** Each eye is made from the source shifted half the disparity
   each way (so each eye's holes are half as wide), by forward splatting
   with a depth test at a quarter of a pixel.
7. **Filling.** What an eye sees and the source does not: first from the
   neighbouring frames (behind a moving object, the background was visible a
   moment before or after: motion-compensated), then by depth-aware
   extension from the background side only, with the texture continued, not
   stretched. A small learned filler (iw3-like, trained here as the OCR
   network was) is a later step if the classical one is not good enough.
8. **Subtitles.** Placed in front of what is under them: the depth under
   each subtitle's area gives its offset, as the disc's offset metadata does
   for a 3D disc (the 3D subtitle code exists).
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

Each is there: the layouts as `--layout full`, `half`, `ou`, `half-ou`
and `mvc`, with `--scale` for 1080p; the Blu-ray 3D as `--output
film.iso`, a disc image with the film's MVC in it.

- **What that takes:** BDMV with its index, movie object and clip files; a
  playlist with the 3D stream table; the base and dependent views in their
  own clips, interleaved into SSIF extents; and a UDF 2.50 ISO around it.
- **What is reused:** bdtools reads every one of these structures already
  (`internal/bdmv`). The MVC encoder is already tuned for disc players'
  decoders (see [mvc-player-compatibility.md](mvc-player-compatibility.md)).
- **What a disc needs on top:** SDR at 8 bits, 1080p at 23.976 fps, and
  Blu-ray's bitrate limits. An HDR source is tone mapped for this output
  only.

## What is there to build on

- Decoding of every source, tone mapping, scaling and cropping.
- Every output layout, and the MVC encoder.
- The 3D subtitle placement, and the GPU drivers from Go (NVENC/NVDEC
  through purego).
- A neural network inference path in Go (`internal/ocr`'s CRNN, with AVX2
  GEMM kernels).
- The encoders' motion search, and VMAF, PSNR and SSIM in Go
  (`internal/vmaf`).
- Native 3D discs to measure against (Avatar, Hugo, Life of Pi) and the
  stereo check.

## What has to be built

1. **A tensor engine in Go** (`internal/nn`). It needs these ops:
   - convolutions and transposed convolutions;
   - attention, layer norm and GELU;
   - resizing.

   Then a safetensors reader, so the published weights load as they are
   (downloaded on first use, not in the repository). The CPU code builds on
   the OCR kernels (AVX2/FMA, an NEON path later). A GPU backend comes
   after: the CUDA driver API through purego, as NVENC is, with cuBLAS for
   the matrix products and kernels of our own for the rest. Checked against
   PyTorch outputs on fixed frames, to a tolerance.
2. **The depth network**: Video Depth Anything Small (a DINOv2 ViT-S encoder,
   a DPT head and its temporal blocks), then Depth Anything 3 to compare.
3. **Depth finishing and grading** (step 4 and 5 above).
4. **View synthesis and filling** (6 and 7), on the CPU first, then on the
   GPU.
5. **The command**: `--to-3d` on a 2D source, with every layout; subtitles
   placed by depth; HDR handled.
6. **Blu-ray 3D authoring** (done): `--output film.iso` for MVC.

## How fast

The depth network dominates: about 100 GFLOP a frame for the small model at
518 pixels, so about 17 PFLOP for a two-hour film.

- **On the 24 cores:** an optimised AVX2 path should reach about 1 TFLOP/s,
  so 5 hours or so a film.
- **On the RTX 5090:** with half precision and cuBLAS, 10 to 20 minutes for
  the depth. View synthesis and filling add a few minutes, and the encode
  takes what it takes today.

So the GPU backend is not optional for this to be practical, and a CPU-only
machine converts overnight.

Measured on the RTX 5090, a 1080p film: the small network 25 ms a picture
in single precision, 7.2 on the tensor cores with attention in one pass
(Base 49 and 14.2; Large 122, single precision only); the whole conversion
(NVENC, its pace once started) 112 pictures a second refined, 124 not: a
two-hour film in about 25 minutes. The picture goes to the GPU as it is
decoded and comes back as its two eyes and their disparity: the
networks' inputs (the picture resized and made RGB for the network, the
half-size picture and standardised depth for the refinement), the
refined depth's renormalisation, the disparity and the eyes' rendering
(a warp a row, the row's targets in shared memory, each one's winner by
an atomic maximum of its depth and its segment's order: the CPU's
arithmetic and choices to the bit) are kernels, and the depth's mean and
deviation are summed over the whole GPU, not one block of it. Of the
disparity only what the floating window and the subtitles read comes
back (its side strips and a grid of its lower half: a tenth of it). A
picture's work is queued on the GPU and the next picture decoded and
copied meanwhile, before waiting for it. On the
tensor cores attention is one kernel a layer (flash attention: the keys
and values a tile at a time, the softmax kept running, the scores never
stored: 6.1 ms a picture down to 1.3) and layer normalisation a warp a
row in single precision (double is slow on consumer GPUs: 2.4 ms down to
0.4); the matrix products read their rows four values at a time, and one
whose tiles of 128 would leave multiprocessors idle or half empty runs in
tiles of 64. The GPU's work sets the pace (the CPU's is a third of
it), the network's matrix products most of it.

## How good: measured, not guessed

The native 3D discs are the ground truth: their left eye is a 2D film whose
right eye is known.

- **Comparing eyes:** convert the left eye, then compare the made right eye
  with the real one (VMAF, PSNR, SSIM, all here already).
- **Comparing depth:** compare the made disparity with the one measured
  between the real eyes (the stereo check's matcher).
- **On our clips:** Mono2Stereo's measures, on our clips.

That gives a number for every change (a better depth network, a better
filler), as the encoders have their BD-rate. Beside it, viewing on the TV
and the Ugoos, the only judge of comfort.

## Measured so far

`tools/stereobench` on twelve 4-second clips, two from each of six discs
(Avatar: The Way of Water, Hugo and Life of Pi shot in 3D; Gravity,
Titanic and Mad Max: Fury Road converted by studios), plus four of
Gravity's glass shots. The right eye made from the left against the
disc's:

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
| refined, mapped as the discs map it | 27.5 | 30 | the defaults now |

What that teaches:

- **The depth is most of the gap**, not the renderer: with the disc's
  depth our right eye is 6 dB nearer the disc's.
- **The grading was too far behind the screen.** The discs spread their
  depth about the screen (5th to 95th percentile -21 to +16 px at 1920,
  the median -4); the first defaults (-40 to +13, median -24) cost 1.2 dB.
- **Normalising each picture's depth range only loses.** The network's
  depth is steady from picture to picture and its scale close to the
  discs': fitted over a whole clip, the discs' disparity is 0.06% to 0.19%
  of the width a unit of it on every clip. Taken as it is (0.12% a unit,
  the farthest 0.75% behind the screen), the made right eye gains 1.6 dB
  and 10 VMAF; on the four glass clips, held out of that choice, 0.9 dB
  and 3 VMAF. What is left to the per-picture fit (3 dB) is the depth's
  shape within each picture, and the shots' own grading.
- **A curve fits better than a line.** Our depth fitted to the disc's by
  any rising curve is 2.9 px off, by a line 3.9: the discs' grading is
  not a scale and an offset, which is what milestone 8 is to learn.
- **The made eye's errors are at edges**, where the depth's soft edges
  drag the background along. A guided filter (radius 8) did not help as
  tuned; it is off.
- **Glass:** clear glass with one thing behind it is fine (Gravity's
  visor close-up: correlation 0.85, 1.7 px). A window showing two things
  at once is not: Gravity's porthole, her face over the Earth, is the
  worst clip (correlation 0.42, 7 px off), because one depth a pixel
  cannot put the face and the Earth at their two depths. Mirror-like
  visors match too, but only around the glass: on it the disc's eyes
  agree on no one depth, as if the reflections had a depth of their own.
  What fixes that is layered rendering: such regions split into what is
  seen through the glass and what is reflected, each its own depth, each
  rendered and the two added. The matcher first needs to find two depths
  in a region, so the benchmark can measure it.
- **A grading learnt from the discs does not generalise.** The discs'
  grading of 27 more films (eight 3-second stretches of each, every sixth
  picture: `stereobench -frames`, `tools/gradefit`) was fitted by a ridge
  regression on each picture's features (its depth's percentiles and
  layout, its brightness). Scored with each film left out, it seemed to
  win (6.5 px off against the fixed mapping's 8.5), but on the
  benchmark's six films it lost (10.8 against 8.5; 26.1 dB against 26.9).
  The gain was the training films' place behind the screen, not the
  features: a film's median sits anywhere from 18 px behind the screen
  (Pacific Rim: Uprising) to 3 px in front (Hugo, Life of Pi, Titanic),
  a choice of its makers that its pictures do not tell. With each film's
  own offset taken out, the features predict no better than the fixed
  mapping (6.1 px held out, both). What is left: the screen's place is a
  setting (`--3d-screen`), and the grading within a film is the depth's
  shape, not its scale.
- **A learnt refinement corrects the depth's shape.** A small U-Net (300k
  weights: `internal/stereo/refinenet.go`), from the picture and the
  network's depth at half size to a correction of the depth, trained in
  Go (`tools/depthtrain`) on 3,774 pictures of the 27 films (now 7,423
  of 53: see below), the loss
  what is left of the disc's disparity after fitting it a picture at a
  time (so each film's grading does not count). On the benchmark's six
  films, which it never saw: the depth's error after fitting 7.9 to 6.9
  px, its correlation with the disc's 0.79 to 0.83, the right eye from it
  fitted per picture 30.2 to 31.0 dB (VMAF 44 to 49). Mapped by the
  discs' mapping re-found for it (0.10% a unit, not 0.12%), the
  conversion's own right eye 26.9 to 27.5 dB (VMAF 26 to 30), and on the
  glass clips, held out of that choice, 25.7 to 26.8 (VMAF 27 to 33).
  It costs about 10 ms a picture. Learnt again from twice the films (26
  more official 3D releases; 7,423 pictures), the validation error is
  6.80 px (6.85), and every score moves a little the right way: the
  benchmark's right eye 27.46 dB either way (VMAF 29.5 to 29.6), the
  glass clips 26.8 to 26.9 (VMAF 33.4 to 34.6), Furiosa 27.88 either
  way (correlation 0.881 to 0.885), Avatar: Fire and Ash 24.39 to 24.40.
  Those weights are built in. More pictures of the same kind help
  little: what is left is not what the films show.
- **Whole films the choices never saw.** Furiosa (converted by the
  studio) and Avatar: Fire and Ash (shot in 3D), each converted whole from
  its 3D disc's left eye beside the disc's own 3D, and scored on clips
  through the film (24 and 25 of 4 seconds):

  | | Furiosa | Avatar: Fire and Ash |
  |---|---|---|
  | depth correlation, refined (not) | 0.88 (0.89) | 0.73 (0.70) |
  | right eye, ours, refined (not) | 27.9 / 31 (28.1 / 32) | 24.4 / 25 (24.4 / 25) |
  | fitted per picture, refined | 31.7 / 56 | 29.9 / 53 |
  | fitted per clip, refined | 30.2 / 47 | 28.1 / 40 |
  | flat; the disc's depth | 27.7 / 35; 37.9 / 87 | 26.7 / 42; 33.7 / 83 |

  The depth's shape is close to a studio's (0.88), less to two cameras'
  (0.73), and the refinement helps the one, costs the other a little. The
  larger loss is the grading: both films spread their depth much wider
  than our mapping (5th to 95th percentile 35 and 43 px at 1920, ours 21
  and 20), which one fit a clip already gets most of (+2.3 and +3.7 dB).
  A made eye scored against the disc's rewards less depth where the depth
  is unsure, so these scores alone do not say how much depth looks best.
- **Bigger networks are not better here.** Video Depth Anything Base and
  Large (`--depth-model base`, `large`; CC-BY-NC-4.0) run in the same
  code. On the twelve clips Base's depth correlates better (0.83, refined
  0.86, against Small's 0.79 and 0.83) and, with its own mapping
  (`stereo.BaseRefinedShape`), its right eye is 27.7 dB and VMAF 30 (Small
  27.5, 29.5); but on the glass clips and both whole films it is worse
  (Furiosa 27.4 / 29 against 27.9 / 31; Avatar 24.1 / 22 against 24.4 /
  25), at twice the time. Large's depth is no better than Base's (0.80),
  at five times Small's.
- **The tensor cores lose nothing measurable.** The network's matrix
  products with their inputs in half precision (sums in single): the
  depth 0.02% of its range off PyTorch's (single precision 0.002%), every
  benchmark measure the same to the second decimal, the network a third
  faster. Large's activations overflow half precision (NaN on glass), so
  it keeps single.
- **The matcher** trusts a match only where the picture has texture:
  without that, space and night skies matched the grain, which two
  cameras do not share.

## Milestones

1. **Depth in Go** (done): `internal/nn` on the CPU; Video Depth Anything
   Small from its published checkpoint, the same depth as PyTorch's
   (within 1e-3 on film frames).
2. **A first conversion** (done): `--to-3d`, cut detection, depth shaped
   into disparity, the eyes rendered nearer over farther, holes filled
   from the background (its edge carried across: measured better than its
   texture mirrored), every 3D layout out.
3. **The benchmark**: `bdtools 3d-bench` on clips of native 3D discs. The
   left eye converted, its made right eye compared with the disc's:
   - disparity: the made one against the one measured between the real
     eyes (a semi-global matcher in Go), after fitting its scale and
     offset, since a conversion need not copy the disc's depth budget;
   - the picture: PSNR and VMAF of the made right eye, overall and where
     holes were filled;
   - other tools (iw3, ...) run on the same clips, for a yardstick.

   Every later milestone is kept only where this says it helps.
4. **The GPU backend** (first step done): the depth network on CUDA
   (`internal/cuda`, the driver API through purego; `internal/nn`'s GPU
   backend, kernels.cu compiled to PTX ahead of time), the same depth as
   PyTorch's within 1e-3: 25 ms a 1080p frame on an RTX 5090, 50 times
   the CPU. Since: the depth of one frame made while the last is
   rendered, the tensor cores (16.5 ms), the networks' inputs made on the
   GPU, attention in one pass, the eyes rendered on the GPU, tiles fitted
   to the GPU (7.2 ms); a film converts at about 110 frames a second. Still to do: the matrix
   products' tiles fitted to the GPU (they run at a tenth of its rate).
5. **Depth edges**: the network's soft depth edges snapped to the
   picture's (an edge-aware filter guided by the full-resolution picture),
   against halos and cardboard cut-outs.
6. **Shots planned whole**: a conversion is not live, so each shot is read
   before it is made:
   - its depth graded from the whole shot's statistics, smoothed forwards
     and backwards;
   - holes filled from the shot's other frames, wherever they show the
     background; only what no frame shows inpainted.
7. **Like a stereographer**:
   - glass and reflections in layers (see [Measured so far](#measured-so-far));
   - floating windows (black masks moving at the picture's sides), where
     something in front of the screen meets the frame's edge (done:
     `--3d-window`, the strip as wide as the disparity at that edge,
     widening 2 pixels a frame and narrowing 0.5);
   - subtitles placed just in front of what they cover (done: the lower
     half's nearest, followed at once nearer and slowly farther, as a
     disc's offset sequence for `--subs-3d` and `--burn-subs`);
   - screens, captions and graphics in the picture kept flat.
8. **Depth graded as the discs are** (tried; no gain): a regression of
   each picture's scale and offset on its features, learnt from 27 films,
   lost on the benchmark's held-out films (see [Measured so
   far](#measured-so-far)): a film's place about the screen is its
   makers' choice, and within a film the features add nothing to the
   fixed mapping. A rising curve in place of the line (the depth's shape)
   is what is left of this.
9. **A learnt refinement** (done for the depth: `--3d-refine`, see
   [Measured so far](#measured-so-far)): a small network, trained in Go
   on the discs' real pairs, from the picture and its depth to a
   corrected depth. The weights learnt from 53 films are built in;
   the training tool ships, for weights of one's own. Next: filling the holes by it too, and a larger network
   on the GPU.
10. **Blu-ray 3D authoring** (first step done): `--output film.iso`, a
    Blu-ray 3D image: the playlist and clip infos tsMuxeR's byte for byte
    for the same facts (`internal/bdmv`), the clips' transport streams
    sent earliest deadline first within their decoders' buffers, the two
    views interleaved into an SSIF in a UDF 2.50 file system
    (`internal/udf`) laid out as a pressed disc's; read by libbluray
    (Kodi's) whole, the disc carrying the encoded stream's every NAL unit.
    The subtitles' depth as a disc's offset metadata (an MVC scalable
    nesting SEI in each GOP's first dependent view access unit, its layout
    a commercial disc's byte for byte). Still to do: checked on a disc
    player, a 2D disc, the offset metadata in an MVC Matroska file too.

## Decisions for you

- **Licences.** Only the Apache-2.0 models: Video Depth Anything Small,
  Depth Anything 3 Small and Base. The non-commercial larger ones (and
  DepthCrafter, StereoCrafter) would be opt-in downloads, if at all.
- **GPU first or CPU first.** The plan writes the CPU path first (to check
  correctness against PyTorch, and for machines without NVIDIA), then
  CUDA. AMD/Intel (Vulkan) and Apple (Metal) GPUs come later, if wanted.
- **The default depth** (decided: 1.5). `--3d-depth 1` is the depth that
  scores best against the discs' right eyes; the discs' own disparity
  spans 1.7 to 2.2 times it (Furiosa, Avatar: Fire and Ash), as a score
  punishes misplaced depth more than too little. The default is 1.5 times
  it; `--3d-depth 1` gives the scored best back.
- **Shipping refinement weights** (decided: built in). They are learnt
  from commercial 3D films; `--3d-refine none` leaves them out, and
  anyone may learn their own.
- **Blu-ray authoring.** Needed if a disc player is a target; it is a
  piece of its own (milestone 6), separate from the conversion.
