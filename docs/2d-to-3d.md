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

All but the last exist as layouts today (`--layout full`, `half`, `ou`,
`half-ou`, `mvc`), with `--scale` for 1080p. The Blu-ray 3D one needs a
new piece: writing a disc, not only reading one.

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
6. **Blu-ray 3D authoring**: `--output film.iso` (or a folder) for MVC.

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

## Milestones

1. **Depth in Go** (done): `internal/nn` on the CPU; Video Depth Anything
   Small from its published checkpoint, the same depth as PyTorch's
   (within 1e-3 on film frames).
2. **A first conversion** (done): `--to-3d`, cut detection, depth shaped
   into disparity, the eyes rendered nearer over farther, holes mirrored
   from the background, every 3D layout out.
3. **The benchmark**: `bdtools 3d-bench` on clips of native 3D discs. The
   left eye converted, its made right eye compared with the disc's:
   - disparity: the made one against the one measured between the real
     eyes (a semi-global matcher in Go), after fitting its scale and
     offset, since a conversion need not copy the disc's depth budget;
   - the picture: PSNR and VMAF of the made right eye, overall and where
     holes were filled;
   - other tools (iw3, ...) run on the same clips, for a yardstick.

   Every later milestone is kept only where this says it helps.
4. **The GPU backend**: the depth network on CUDA (through purego, as
   NVENC), a two-hour film's depth in under half an hour.
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
   - floating windows (black masks moving at the picture's sides), where
     something in front of the screen meets the frame's edge;
   - subtitles placed just in front of what they cover;
   - screens, captions and graphics in the picture kept flat.
8. **Depth graded as the discs are**: a small model, learnt from the
   native and studio-converted discs, of the depth budget, the screen's
   place and its moves within a shot and across cuts, in place of fixed
   numbers.
9. **A learnt refinement**: a small network, trained on the discs' real
   pairs (in PyTorch, offline; run here as the depth network is), from the
   picture and its depth to the right eye: depth errors corrected, holes
   filled. The training tool ships; weights learnt from commercial films
   may not (their law is unsettled), so each user trains on their own.
10. **Blu-ray 3D authoring**: `--output film.iso` (or a folder) for MVC,
    checked on a disc player.

## Decisions for you

- **Licences.** Only the Apache-2.0 models: Video Depth Anything Small,
  Depth Anything 3 Small and Base. The non-commercial larger ones (and
  DepthCrafter, StereoCrafter) would be opt-in downloads, if at all.
- **GPU first or CPU first.** The plan writes the CPU path first (to check
  correctness against PyTorch, and for machines without NVIDIA), then
  CUDA. AMD/Intel (Vulkan) and Apple (Metal) GPUs come later, if wanted.
- **The default depth.** Conservative (behind the screen, little out of it)
  or bolder; it can be a preset (`--3d-depth gentle|normal|strong`).
- **Blu-ray authoring.** Needed if a disc player is a target; it is a
  piece of its own (milestone 6), separate from the conversion.
