# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- `--to-3d` makes a 2D film 3D, out in any 3D layout (full or half side by
  side or over-under, MVC): each picture's depth estimated by Video Depth
  Anything Small (Apache-2.0), run in Go from its published checkpoint
  (downloaded once), the eyes made from it, the nearer covering the
  farther and what an eye sees behind an edge filled from the background
  side. `--3d-depth` and `--3d-screen` scale the depth and move it
  nearer or farther (by default 1.5 times the depth that scores best against the 3D discs', which spread theirs about twice as wide), `--depth-model` another
  checkpoint. Letterbox bars stay at the screen, cuts start the depth
  afresh, an HDR source is tone mapped. The depth runs on an NVIDIA GPU
  when there is one (`internal/cuda`: the CUDA driver API without cgo or
  the toolkit, kernels compiled to PTX ahead of time): a 1080p film
  converts at about 120 pictures a second on an RTX 5090 (110 with the
  refinement below; the GPU's depth of a picture beside the CPU's work on
  the one before, the network on the tensor cores with attention in one
  pass, the networks' inputs and the eyes made on the GPU); on the CPU,
  about one.
- `--to-3d` with `--subs-3d` or `--burn-subs`: the subtitles just in front
  of the made 3D picture beneath them (an offset sequence made from its
  depth), not at the screen behind what comes out of it.
- `--to-3d` puts a floating window where something in front of the screen
  meets the picture's left or right edge (`--3d-window`).
- `--to-3d`'s depth is corrected by a small network learnt from real 3D
  pairs (53 films; on held-out discs the made right eye 0.5 to 1 dB
  nearer theirs), its weights built in. `--3d-refine none` leaves it out,
  `--3d-refine FILE` uses weights of one's own: `tools/depthtrain` learns
  them from 3D films (`tools/stereobench -samples`), in Go on the CPU.
- `--to-3d`'s depth network has its last stages learnt anew from the same
  53 films' real 3D pairs (`--3d-tune`, on by default for the small
  network; in place of the refinement, which then stays off): on held-out
  discs the made right eye 0.1 to 0.9 dB nearer theirs and VMAF up to 7
  higher than the refined depth's, the glass shots most. `--3d-tune none`
  leaves the network as it is, `--3d-tune FILE` takes weights of one's
  own (`tools/depthtrain -tail`, on the GPU).

- `--3d-depth` and `--3d-screen` change a 3D film's depth (a disc, MV-HEVC
  or a side-by-side or over-under file), everything else kept: each
  pair's disparity measured between its eyes, each eye made again at
  the depth asked for from its own camera, what it then sees behind an
  edge taken from the other camera (so less depth than the film's
  invents little; more fills from the farther side, as `--to-3d` does),
  the whole moved nearer or farther by `--3d-screen`, the subtitles'
  offsets scaled and moved with it. On the CPU for now: about 3 pictures
  a second.

- `--output film.iso`: a Blu-ray 3D disc image of a 3D film (a 3D disc
  re-encoded, or `--to-3d`): its MVC video in a disc's two clips (the base
  view's with the audio and subtitles, the dependent view's), interleaved
  as an SSIF, with the clip infos, a playlist with the chapters and a
  title that plays it, in a UDF 2.50 file system. Players read it as a
  pressed disc (Kodi and CoreELEC as an ISO; a disc player burned to a
  BD-R). The subtitles' depth goes with them, as a disc's offset metadata
  in the dependent view (the made 3D picture's, or a 3D disc's own); the
  left eye's GOPs are a second at most, as a disc's must be.
- `--to-3d` fills what one eye sees and the other did not (behind an
  edge) with the background's edge carried across, not its texture
  mirrored: measured on the 3D discs' own depth, 1.8 dB nearer their
  right eyes in those places and VMAF 0.6 higher (no ghost of the edge).

- `--depth-model base` and `large`: Video Depth Anything's bigger
  networks (downloaded once; licensed CC-BY-NC-4.0, for non-commercial use
  only), each with its own mapping of depth to disparity. Measured, they
  are not better than the small one on whole films: see docs/2d-to-3d.md.

### Fixed

- `--to-3d` resumed an interrupted conversion with another
  `--depth-model`'s pictures: the network is part of what must match now.
- `--2d --remux` of a 3D Matroska file (both views in its one video track)
  copied the dependent view's NAL units too: the copy holds the base view
  alone now.

## [0.14.2] - 2026-10-19

### Fixed

- 3D subtitles: a window definition that claims more windows than it
  carries is read for the windows it has (Titanic 3D's disc failed its
  mux on one, 11 minutes in), and a display set the 3D rewrite cannot read
  is left out with a warning instead of failing the conversion.

## [0.14.1] - 2026-10-19

### Changed

- purego (the GPU libraries' loader, no cgo) 0.11.2, was 0.11.1.

### Fixed

- The GPU decoder comparison test is skipped when VideoToolbox's media
  engine fails a frame (kIOReturnError), as macOS CI's virtual machine does
  now and then; conversions already hand such a stream to the decoders here.

## [0.14.0] - 2026-10-19

### Added

- The AV1 encoder codes a picture as one tile, the fewest the limits allow
  (was about one a 512x512 area, 2x2 at 1080p): 5.9% smaller on the
  120-picture clips for 1.31 times the time, +5.7% against SVT-AV1 preset 4
  (which codes 1080p as one tile too).
- The AV1 encoder's inter frames take their CDFs from the reference nearest
  their depth in the pyramid (was LAST_FRAME's), as libaom takes its own
  layer's: 0.5% smaller on the 120-picture clips at the same speed, +11.6%
  against SVT-AV1 preset 4.
- What a library re-encode needs, in bdtools itself: `--audio-best` keeps
  the best track of each `--audio-lang` language; `--no-commentary` leaves
  commentaries out; `--default-audio` and `--default-subs` set the default
  tracks (a film in its own language, English subtitles on);
  `--subs-lang none`; an untagged Matroska track whose name says its
  language has it; and `--list --json` gives the tracks (names, forced and
  commentary flags, picture size) and the running time to a script.
- The AV1 encoder's directional intra prediction splits its rows where
  the edge it reads changes, not testing each sample (the same output):
  1.3% less CPU.
- The AV1 encoder's transform type ranking divides only the coefficients
  not quantized to 0 (the same output): 1.4% less CPU.
- The AV1 encoder's self guided restoration A and B in AVX2 (the same
  output): 1.5% less CPU.
- The AV1 encoder's self guided restoration statistics as dot products of
  each strength's rows, in AVX2 (the same output): 2% less CPU.
- The AV1 encoder's sub-sample motion search filters the eight
  neighbours of a step through their three columns' horizontal passes
  (the same output): 3.6% less CPU.
- The AV1 encoder's deblocking wide filters slide their sums from one
  sample to the next (the same output): 4% less CPU.
- The AV1 encoder's self guided restoration filter in AVX2 (the same
  output): 3% less CPU.
- The AV1 encoder's inter frames filter with EIGHTTAP throughout (the
  filter was switchable by block): 0.3% smaller on the 120-picture clips
  and 1.23 times faster, +12.2% against SVT-AV1 preset 4.
- The AV1 encoder's inter frames code quarter sample vectors only (eighth
  sample below q 128 before): 0.2% smaller on the 120-picture clips and 3%
  faster, +12.7% against SVT-AV1 preset 4.
- The AV1 encoder's end of block search prices the zeros it drops by
  their base level's symbol (was 0.25 bits each): 0.3% smaller on the
  120-picture clips for 2% more time, +13.1% against SVT-AV1 preset 4.
- The AV1 encoder's lambda is retuned for the end of block search (1, was
  0.6): 1.4% smaller on the 120-picture clips and 1.14 times faster,
  +13.4% against SVT-AV1 preset 4; key frames 4.5% smaller than SVT-AV1
  preset 4's on the six disc frames.
- The AV1 encoder's quantization searches each transform's end of block:
  the levels after a nonzero one zeroed where their distortion is worth
  less than their bits and the end's: 2.1% smaller on the 120-picture
  clips at the same speed, +14.4% against SVT-AV1 preset 4.
- The AV1 encoder refines a NEW_NEWMV block's two vectors together,
  each searched again for the prediction that, averaged with the
  other's, matches the source (to `slow`): 0.7% smaller on the
  120-picture clips for 9% more time, +16.4% against SVT-AV1 preset 4.
- The AV1 encoder's motion search also starts from two rings of points
  around the block, 8 and 24 samples out: 0.5% smaller on the
  120-picture clips at the same speed, +17.2% against SVT-AV1 preset 4.
- The AV1 encoder's temporal filter takes key frames too, with the six
  pictures after them, the filtered picture shown: 0.7% smaller on the
  120-picture clips (a key frame each), +17.8% against SVT-AV1 preset 4.
- The AV1 encoder's temporal filter is on: each mini-GOP's anchor coded
  from a motion compensated average of its neighbours, shown by an
  overlay frame, at strength 2 (libaom's 5 filters the disc sources too
  hard): 2.1% smaller on the 120-picture clips and 4% faster, +18.6%
  against SVT-AV1 preset 4.
- The AV1 encoder's mini-GOPs 32 pictures (were 16), a level more of
  the pyramid: 0.8% smaller on the 120-picture clips and 1.2 times
  faster, +20.2% against SVT-AV1 preset 4 (which codes 32 too).
- The AV1 encoder gathers the Wiener filter's statistics with AVX2: the
  same stream, 1.5% less CPU.
- The AV1 encoder's transform type ranking makes each column pass once
  for the types that share it: the same stream, 2.2% less CPU.
- The AV1 encoder's inter transform trees stop searching a split once
  it costs more than the whole: the same stream, 1% faster.
- The AV1 encoder ranks a transform's types by an estimate from their
  forward transforms and searches the best three by rate and distortion:
  0.6% smaller on the 120-picture clips and 1.09 times faster, +22.5%
  against SVT-AV1 preset 4.
- The AV1 encoder averages compound predictions (plain and distance
  weighted) with AVX2: 1.7% less CPU, the same stream.
- The AV1 encoder's forward and inverse transforms call their 1D
  transforms directly, their buffers no longer allocated on the heap:
  2.5% less CPU, the same stream.
- The AV1 encoder's compound blocks can take two frames on the same
  side of the frame (the unidirectional pairs, and the bidirectional
  syntax's with both frames earlier), in the frames with no later
  reference (the mini-GOPs' anchors), to `slow`: 0.1% smaller on the
  120-picture clips for 3% more time.
- The AV1 encoder codes in full only the inter candidates whose
  estimate is within 15% of the best one's (5% from `medium`): 1.08
  times faster on the 120-picture clips, 0.1% smaller.
- The AV1 encoder's inter prediction clips and widens with AVX2 and
  copies the reference's edges by rows: 5% less CPU, the same stream.
- The AV1 encoder skips a block's intra search in inter pictures when
  intra's estimate is well above inter's (from `medium`, nearer): 1.08
  times faster for 0.1% on the 120-picture clips.
- The AV1 encoder codes inter-intra blocks: a single reference block's
  prediction blended with a DC, V, H or smooth intra one, by a fading
  or a wedge mask, decoded bit-exact by libdav1d: 0.4% smaller on the
  120-picture clips at the same speed, +23.1% against SVT-AV1 preset 4.
- `--preset` sets the built-in AV1 encoder's speed: every tool searched
  to `slow`; at `medium` the transform trees decided with DCT_DCT,
  no warped motion or masked compound (1.51 times faster, 2.5% larger);
  from `fast` also no OBMC, four references and two candidates coded in
  full (1.86 times faster, 3.7% larger); at `slower` the partitions after a skipped whole block searched too, and every
  inter candidate coded in full (2.8 times slower, 2.3% smaller), from
  `veryslow` none pruned (5.3 times slower, 3.0% smaller, +2.8%
  against SVT-AV1 preset 4).
- The AV1 encoder's mini-GOPs 16 pictures (were 8): 1.1% smaller and a
  fifth faster on the 120-picture clips, +23.7% against SVT-AV1 preset 4.
- The AV1 encoder's temporal model sets each mini-GOP's anchor q (and
  its depth 2 frames') by how much the others take from it: 0.6%
  smaller (Frozen II 1.1%), +25.9% against SVT-AV1 preset 4.
- The AV1 encoder's temporal model on: it scales each superblock's
  lambda by how much later pictures take from it (nothing more to code,
  the rows still coded at once): 0.4% smaller (Frozen II 0.9%), +26.7%
  against SVT-AV1 preset 4.
- The AV1 encoder's bottom pictures 8 coarser than the run's q, a
  smaller lambda (0.6): 0.5% smaller, +27.2% against SVT-AV1 preset 4.
- The AV1 encoder's anchors and the frames just below them code finer
  (q index offsets 52, 52, 36, 20 by depth, were 40, 40, 28, 16): 0.9%
  smaller on the 120-picture clips (Frozen II 1.8%), +27.7% against
  SVT-AV1 preset 4. The temporal filter can take pictures after the
  anchor (a lookahead); it stays off (the disc sources have little noise
  for it to take out).
- Masked compound in the AV1 encoder (comp_group_idx: a wedge, one of
  sixteen with a sign, or the predictions' difference), the two best
  compound candidates estimated masked: 0.1% smaller, +29.1% against
  SVT-AV1 preset 4.
- The AV1 encoder's sub-sample filters in AVX2: 18% less CPU, 12% less
  time, the same stream. (A legacy SSE move left in AVX2 code made the
  first try slower than Go on the P-cores; the OCR kernel had one too.)
- The AV1 encoder 1.19 times faster again: an inter transform whose
  DCT_DCT quantizes to all zero tries no other type (+0.2% BD-rate),
  and the quantizer divides only for the nonzero levels (the same
  stream).
- The AV1 encoder codes the next mini-GOP's anchor while the frames
  below this one's are coded, and the frames of a depth at once (a frame
  refers only to frames above it in the pyramid; an anchor also to the
  last mini-GOP's frames to depth 2): 1.37 times faster on the two
  120-picture clips, for 0.2% BD-rate (+29.5% against SVT-AV1 preset 4).
- Local warped motion in the AV1 encoder (motion_mode LOCALWARP: an
  affine model fitted to the neighbours' motion): 0.2% smaller on the
  120-picture clips, +29.1% against SVT-AV1 preset 4.
- Temporal motion vectors in the AV1 encoder (use_ref_frame_mvs: the
  references' motion projected to give candidates): 1.7% smaller on the
  120-picture clips, +29.5% against SVT-AV1 preset 4. Distance weighted
  compound is there too, off (it gained nothing).
- The AV1 encoder searches all seven references (LAST2, LAST3 and
  ALTREF2 too) and six compound pairs: 0.9% smaller on the 120-picture
  clips, +31.7% against SVT-AV1 preset 4, for a tenth more time.
- More inter tools in the AV1 encoder: the compound modes with one NEW
  vector, switchable interpolation filters chosen per block by rate and
  distortion, and OBMC (overlapped block motion compensation): 1.3%
  smaller on the two 120-picture clips, +32.7% against SVT-AV1 preset 4
  (was +34%), for about a quarter more time.
- Loop restoration in the AV1 encoder (Wiener, self-guided or switchable,
  chosen per unit after CDEF, the unit size by rate and distortion), a
  smaller lambda and finer key frames: 3.5% smaller on two 120-picture
  1080p disc clips, +34% against SVT-AV1 preset 4 there (the 30-picture
  clips flattered the encoder, their key frame a quarter of the bits).
- The AV1 encoder faster and smaller: a tile's superblock rows coded at
  once (a wavefront: each row decides from the CDFs of the row above, the
  stream still exact and the same however scheduled), the HEVC encoder's
  AVX2 SAD and SATD, four inter candidates coded in full; about twice as
  fast, and +26.4% against SVT-AV1 preset 4 on the two clips (was +28%).
  A q index a superblock (delta_q) is supported; a temporal dependency
  model to set it is there but off (it cost 1.5% here).
- An AV1 encoder in Go (internal/av1): `--encoder builtin --codec av1`,
  the fallback when there is no GPU encoder and no SVT-AV1. Every stream
  the tests code is decoded by libdav1d to the encoder's own
  reconstruction, sample for sample.
  - Key frames: every intra tool (the directional modes with their
    angles, smooth and Paeth, filter intra, chroma from luma, the edge
    filter), all partitions (to 4x4, the extended ones), transform
    depths and types, rate-distortion quantisation, the deblocking
    filter and CDEF chosen by search; +0.3% BD-rate (PSNR-Y) against
    SVT-AV1 preset 4 `tune=1` on six 1080p disc frames.
  - Inter frames: mini-GOP pyramids of 8 (the anchor coded first and
    shown later), references before and after, compound prediction,
    skip mode, the motion vector stack and every single-reference mode,
    a motion search to 1/8 sample, inter transform trees, the CDFs
    carried from frame to frame, a q index by depth; on two 30-picture
    1080p clips +28% against SVT-AV1 preset 4.
  - Parallel: tiles, and a mini-GOP's frames that do not depend on each
    other, at once (the stream the same as one at a time). Still slow:
    1-3 s a 1080p picture.
  - libaom's temporal filter for the anchors is there but off: with only
    the pictures before an anchor in its window it cost 7% here.

- The Go H.264 encoder much closer to x264. Measured by BD-rate (PSNR-Y)
  against x264 medium `--tune psnr` on 240-picture clips from Blu-ray
  discs (live action and animation):
  - trellis quantisation: every residual block's levels (luma 4x4 and
    8x8, inter and intra, chroma AC) chosen for the least error plus
    lambda times their CABAC bits;
  - a B-pyramid: with two B pictures or more, the middle one is a
    reference for the others (a list modification keeps P pictures'
    nearest reference first);
  - intra prediction tried in P and B pictures wherever I_16x16 alone
    would come near the motion's cost (a cheap estimate gates it);
  - the 8x8 transform tried on every candidate near the best, not only
    on the winner;
  - B pictures decided with a larger lambda;
  - a lookahead of 40 frames at half resolution: a macroblock tree (each
    macroblock's quantiser lowered by how much later pictures predict
    from it), scene cut detection (a new scene starts with an IDR
    picture, x264's test), and its vectors as starting points for the
    motion search, which also goes coarse to fine.
  On Gravity, two Frozen II scenes and Blade Runner 2049 the mean goes
  from +41% to +8%; with Godzilla vs. Kong (fast action) added, the
  five's is +9%. It takes 4 times x264 medium's CPU.

- Interlaced H.264 in the Go decoder: field pictures (PAFF) and MBAFF
  frames, alone or mixed, CAVLC and CABAC, every slice type, with the
  MBAFF deblocking filter. 79 of the 80 interlaced JVT conformance
  streams decode as ffmpeg decodes them, frame for frame (13 are in the
  tests; CAMACI3_Sony_C, constrained intra prediction in MBAFF, is the
  one left), and x264's MBAFF output does too. Interlaced H.264 sources
  (1080i discs) are decoded here and deinterlaced (yadif) when no GPU
  decoder takes them, instead of going to ffmpeg.

- Dolby Vision profile 7 out with the Go HEVC encoder: `--dv-fel keep`
  and `--dv-fel reencode` take `--encoder builtin`, both layers coded
  here with the same picture structure, so a two-layer conversion needs
  no NVENC and no x265. On a 20-second 4K clip of a FEL disc it took
  0.7 times x265's time (`--dv-fel keep`, `--crf 16`).

- `--crop TOP:BOTTOM:LEFT:RIGHT` and `--scale 1080p` (or 2160p, 1440p,
  720p, 576p, 480p, WIDTHxHEIGHT) for 2D conversions: the picture cropped,
  then fitted in the box keeping its shape (never enlarged) by a Lanczos-3
  filter here, chroma kept at 4:2:0's siting. Every encoder takes the
  result. The PGS subtitle tracks are cut with the picture (a line in a
  black bar moved up into it), and a Dolby Vision film's RPUs say the
  active area they now have (level 5). A 4K FEL film cropped and scaled to
  1080p converts at 21 fps with the encoder here.
- `--crop auto`: the black bars found before the conversion. The
  conversion's own decoder decodes three pictures from a keyframe at each
  of twelve places in the video (discs, Matroska and MP4 read in places,
  without reading what lies between), and what is black in all that show
  anything is cropped (a fade does not count; a film whose shape changes
  keeps its largest picture). A few seconds for a disc: 138 rows above and
  below on Godzilla vs. Kong's 2.39:1, 15 columns at the sides of Hugo.
- `--tonemap`: an HDR picture (HDR10, HLG, Dolby Vision composed, HDR10+)
  made SDR, BT.709 with BT.1886's gamma. BT.2390's EETF on each pixel's
  max(R,G,B) in the PQ domain (hue and saturation kept), SDR white at
  HDR's reference white (203 cd/m²), bending from each shot's peak where
  the source gives one (Dolby Vision level 1, HDR10+'s MaxSCL), else
  MaxCLL or the mastering peak; out-of-gamut colours desaturated toward
  their luminance, hue kept; chroma resited to 4:2:0. No HDR metadata,
  RPU or enhancement layer is carried. A 4K Dolby Vision film cropped,
  scaled to 1080p and tone mapped converts at 16.6 fps with the encoder
  here.

- Text subtitles: a Matroska or MP4 source's SRT, ASS, SSA, WebVTT and
  timed text (tx3g, WebVTT in MP4) tracks are carried, as they are into
  Matroska (MP4's as SRT) and as tx3g into MP4 and MOV, plain text there.
  `--subs-file` adds SRT, ASS, SSA and WebVTT files as tracks (the
  language and the forced flag from the file name, or given). Matroska
  blocks now carry their durations (BlockGroups with BlockDuration).

- `--subs-ocr`: PGS subtitles read as text by a network trained here
  (internal/ocr, docs/ocr.md; no OCR tool): an SRT track beside each PGS
  track in Matroska, the tx3g track of an MP4. Italics are marked. On
  seven discs' tracks against their films' SRTs, 1.2% of characters
  differ, much of it the SRTs' own.

- `--start`, `--end` and `--duration`: part of a title or file converted,
  the sound (within half an audio frame), subtitles and chapters cut with
  the picture. A disc title is windowed as its playlist windows its clips,
  the clips outside not read; reading stops past the end.

- `--for appletv|coreelec|kodi|plex|quest|visionpro`: a player's settings
  (codec, depth, Dolby Vision form, 3D layout, subtitles as text), each
  only where the command line gives none.

- `--video-size 8G`: the CRF found for a video size before the conversion,
  from two seconds encoded at ten places with the conversion's own
  encoder at a few CRFs (a GOP's IDR picture counted apart): within about
  a tenth of the size on a disc's film, usually under.

- Batches: `--input` a directory converts every source in it into
  `--output` a directory, under the same paths, noting what is done;
  `--watch` goes on converting what arrives.

- `--compare OUTPUT` (with `--input` the source): PSNR, SSIM and VMAF
  measured here, picture by picture (internal/vmaf: libvmaf's integer VIF,
  ADM and motion extractors and its v0.6.1 models ported to Go, and its
  float SSIM), the same numbers its `vmaf` tool gives, to the digit; both
  videos decoded by the conversion's own decoders. Means, harmonic means,
  1st percentiles and worst pictures, or every picture to a CSV.

### Fixed

- A GPU decoder failing mid-stream (a virtual machine's media engine, as
  macOS CI's VideoToolbox does now and then) hands H.264 and HEVC to the
  decoder here from the last IDR, the pictures already out not repeated;
  it was only replaced before its first picture, the conversion failing
  after.
- A resumed conversion now starts over when `--crop`, `--scale`,
  `--tonemap` or the trim changed, instead of joining segments made with
  other settings.

- A 2D Matroska source no longer warns that its pictures had no
  dependent view.

- The Go HEVC encoder's output could change from run to run at 4K: its
  motion search waited for the reference's rows around the block, but
  searched around the predicted vector, which can be further down, and
  so read rows still being filtered. It now waits for the rows it
  searches.
- Less memory and CPU in the Go HEVC encoder: the fractional reference
  planes (every reference's luma at sixteen phases) are gone. They held
  280 MB a reference at 4K (37 GB at once, which ran a 4K Dolby Vision
  conversion out of memory); without them 1080p codes 5-18% faster and
  4K within 3%, the output within noise.

- Monochrome (4:0:0) H.264 came out with black chroma planes; they are
  grey now, as other decoders give it.

- The Go H.264 encoder's output depended on how its concurrent pictures
  were scheduled (a coder's search starting points were the vectors of
  whatever picture it coded last); they now come from the reference
  pictures, and the same frames always code to the same stream.
- Less CPU in the Go H.264 encoder's intra 4x4 mode ranking (an
  unrolled 4x4 SATD), about 5% overall.
- Less CPU in the encoder's wavefront: a worker waiting for the row
  above sleeps on that row instead of yielding to the scheduler, which
  cost a fifth of the CPU in its lock.

- An AAC-LC encoder in Go (`internal/aac`): mono to 5.1, mid/side
  stereo, a psychoacoustic model (Bark spreading, the threshold of
  hearing), a rate loop with a bit reservoir. By a noise-to-mask measure
  independent of its model it is 2 to 3.5 dB ahead of FFmpeg's AAC
  encoder at 96 to 448 kb/s on a film soundtrack (its SNR is lower: the
  noise goes under the masking threshold). `--audio-transcode aac` codes
  an MP4 output's DTS and PCM tracks with it (96 kb/s a channel) instead
  of AC-3. Its tables come from FFmpeg (LGPL); see NOTICE.
- AAC tracks are carried as they are: from MP4 and Matroska sources into
  Matroska and MP4 outputs (they were left out).
- An AC-3 encoder in Go (`internal/ac3`): mono to 5.1, every A/52 rate,
  rematrixing, the bits a frame has left spent on its channels. On a film
  soundtrack it measures 0.2 to 0.45 dB above ffmpeg's AC-3 encoder at
  640, 448, 320 and 192 kb/s (behind at 128 kb/s stereo, where ffmpeg
  couples the channels), at 80 times real time for 5.1.
- PCM tracks go into an MP4 as AC-3 from that encoder, no ffmpeg needed:
  7.1 and 7.0 folded to 5.1 and 5.0, 96 and 192 kHz brought to 48.
- A DTS decoder in Go (`internal/dts`): the core of DTS and DTS-HD
  streams, fixed point, bit for bit what FFmpeg's reference-exact mode
  decodes (on Blu-ray DTS and DTS-HD MA, and on FFmpeg's own streams).
  DTS tracks go into an MP4 as AC-3 through it and the encoder here, at
  about 40 times real time: audio needs no ffmpeg at all. Two of its
  tables come from FFmpeg (LGPL); see NOTICE.

## [0.13.0] - 2026-10-16

### Added

- Over-under output: `--layout ou` (the eyes stacked at full size, the
  left on top) and `--layout half-ou` (squeezed to the source's height),
  flagged top-bottom in the file, 3D subtitles drawn in both eyes.
- 3D sources in one frame: a side-by-side or over-under file (full or
  half) converts as a 3D source, to any 3D output or to 2D (the left
  eye). A Matroska file's stereo mode says so; `--input-layout` says it
  for the rest, or (`2d`) takes the frame as one picture.
- MP4 and QuickTime sources (`.mp4`, `.mov`, `.m4v`), read in Go: H.264,
  HEVC and AV1 video, AC-3, E-AC-3 and PCM audio, edit lists kept. An
  MV-HEVC file (Apple spatial video) is a 3D source, both views decoded
  here; a side-by-side or over-under one (st3d) is too.
- `probe.MP4` identifies an MP4 or QuickTime file from a sample: its
  tracks, sizes, languages and duration, and whether it is 3D: MV-HEVC
  (`LayoutMVHEVC`, unless Apple's `vexu` names one eye) or a frame holding
  both eyes (`st3d`). The movie box is at the file's start or end, so the
  first and last pieces of a download suffice; a read the sample lacks is
  a `*MissingDataError`. `probe.Path` takes `.mp4`, `.mov` and `.m4v`;
  `KindMP4` is new.
- MP4 and QuickTime output for every layout but MVC: H.264 (avc1), HEVC
  and AV1 (av01), a frame holding both eyes flagged (st3d) and shaped as
  in Matroska. `--burn-subs` draws a subtitle track into any MP4 output,
  in each eye's part of a 3D frame.
- `--encoder builtin` codes H.264 (8-bit) for every layout, not only
  MVC, and is the default for H.264 when no GPU encoder works and x264
  is missing.

## [0.12.0] - 2026-10-16

### Added

- `--encoder builtin` codes HEVC (`--codec h265`) with the encoder in this
  program: every layout but Dolby Vision's kept layers, 8 or
  10 bits, no x265 or GPU needed. `--crf` N codes at QP N-5, which matched
  x265 medium's CRF N in size and PSNR on a film minute. It is the
  automatic choice for HEVC when no GPU encoder works and x265 is
  missing. Picture sizes that are not multiples of 8 are padded and
  cropped by the conformance window.
- `--layout mvhevc` without NVIDIA: `--encoder builtin` codes MV-HEVC
  with the encoder in this program, the right eye a second layer
  predicted from the left (on a film slice it took 44% of the bytes of
  the right eye coded alone, at the same PSNR). It is the default for
  the layout when NVENC does not work here.
- `--burn-subs ID` draws a subtitle track into both eyes of an MV-HEVC
  picture, at the depth the disc gives it: the .mp4's players show no
  Blu-ray subtitles.
- MV-HEVC output keeps DTS and PCM tracks: ffmpeg codes them as AC-3
  (5.1 at most, 640 kb/s), timed from the source, gaps and late starts
  included. They were left out.
- The HEVC decoder decodes MV-HEVC's second view (`SetLayers(2)`): NVENC's,
  x265's and the encoder's own streams decode bit for bit as ffmpeg
  decodes them.

### Changed

- The HEVC encoder codes with sign data hiding, SAO and rate-distortion
  optimised quantisation: 9% smaller than x265 medium at equal PSNR on
  the film minute (1% before), at 51 pictures a second against 63.

## [0.11.0] - 2026-10-17

### Added

- An HEVC encoder in Go (`internal/hevc`), the groundwork for coding H.265
  without x265 or a GPU: I, P and hierarchical B pictures (groups of
  seven by default), rate-distortion decisions over the CU quadtree,
  transform trees, intra modes, merge, AMVP and motion search, WPP
  substreams coded in parallel and pictures in a pipeline that waits on
  reference rows, AVX2 for its kernels, 8 and 10 bits. Every picture is
  decoded back in the background and checked against the encoder's own,
  in display order. On a 1080p film minute it codes 63 pictures a second
  on a 24-core machine, about 1% smaller than x265 medium at equal PSNR
  (x265 medium: 93). bdtools does not use it yet; `cmd/hevcenc` codes raw
  video with it for development.

### Changed

- The HEVC decoder's 8-, 16- and 32-point inverse transforms run in AVX2
  (bit for bit the Go ones).

## [0.10.0] - 2026-10-16

### Added

- `--layout mvhevc` writes MV-HEVC in an MP4 or QuickTime file for Apple
  devices (Vision Pro, iPhone) and the Meta Quest: the left eye as an
  HEVC base layer, the right as a layer predicted from it, coded by NVENC
  in process, with Apple's stereo signalling (`vexu`, `hfov`, `lhvC`) and
  `--spatial-fov`, `--spatial-baseline` and `--spatial-disparity` for
  what the file states. Audio goes in as AC-3 or E-AC-3 (a TrueHD track's
  AC-3 core); subtitles are left out. The file states the disc's colour
  signalling, or BT.709 for an HD disc whose stream states none. The
  output is 8-bit unless `--bit-depth 10` asks otherwise. Verified on
  Apple's profile, with ffmpeg 8, and on a Meta Quest with SKYBOX 2.0.2.

### Changed

- The NVENC binding speaks NVENC API 13 (driver 570 and later), which
  MV-HEVC needs.

## [0.9.0] - 2026-10-15

### Added

- `--layout mvc` writes a frame-packed 3D MKV (the dependent view in a
  `mvcC` BlockAddition) for players with an MVC decoder, and codes both
  eyes with the H.264 encoder here (`--encoder builtin`, the default
  for this layout): the right eye from the left and from its own past
  and future pictures, the left as High profile with B pictures. One
  slice per picture in both views, as players' MVC decoders stutter on
  more; a picture's macroblock rows are decided in parallel instead (a
  wavefront), and the B pictures of a group and the next reference are
  coded at once, in both views. The transforms, quantiser and block
  matching run in AVX2. A feature film codes at about 30 frames a
  second on a 24-core machine. `--encoder software` uses x264 for the
  left eye instead; a GPU encoder is refused, as players' MVC decoders
  fail on NVENC's output. What was learnt on the Amlogic decoder is in
  `docs/mvc-player-compatibility.md`.
- `--layout mvc --encoder copy` keeps a 3D source's left eye exactly as
  it is (no generation loss on the 2D picture, nothing to encode for it)
  and codes only the right eye, smaller.
- `--aq N` (`--layout mvc`, the built-in encoder): adaptive quantisation,
  each macroblock's quantiser moved by its energy. Off by default: on
  Gravity it costs about a VMAF point at equal size, whichever way the
  offsets are centred.
- `qcmp -every N` scores every Nth picture, for a film-length comparison
  in a fraction of the time; `qcmp` reads MVC MKV files and scores both
  eyes.
- `BDTOOLS_CPUPROFILE=file` writes a CPU profile of a run.

### Fixed

- `--remux` of a 3D disc with nothing else given was refused ("cannot
  change the bit depth"): the 10-bit default meant for encodes was applied
  to it. A remux keeps the disc's depth.
- `mvc.NewAUReader` on a pipe returned nothing until a megabyte had
  arrived, which deadlocked an encoder feeding it small pictures (the
  dark opening of a film) while waiting for its first access unit back.

## [0.8.6] - 2026-10-12

### Fixed

- `--dv-fel keep` on a Mac (VideoToolbox decoding) could rebuild a
  picture's enhancement layer against another picture's base layer, and
  come out no nearer the source than `reencode`: the base layer it decodes
  again went to the decoder with timestamps in decoding order, often all
  the same, and VideoToolbox gives pictures back in timestamp order. They
  now carry their place in display order, and a picture out of it stops
  the encode rather than spoil it. This was also the macOS CI's flaky
  `TestRunnerKeepsFELLayers`.
- Encoding a Matroska file whose video stream states one frame rate and
  whose timestamps run at another followed the stream, and the picture
  drifted from the sound: a UHD disc remux stating 24 in its HEVC stream
  but playing at 24000/1001 came out 7 s short over two hours. The file's
  own timing now wins, with a warning. A frame duration also now reads as
  the nearest standard rate, so 24 is no longer taken for 24000/1001.

### Added

- `tools/qcmp -cambi` scores banding too, with Netflix's CAMBI (through PQ
  for an HDR source), in the encode and in the source, and tells where the
  encode bands most, for a look by eye.

## [0.8.5] - 2026-10-10

### Fixed

- `--name-details` names an NVENC H.264 or HEVC encode by the constant
  quality it was made at, `CQ26` for `--crf 20`, not `QP20`: since 0.8.3
  NVENC encodes at a constant quality, not a constant QP, and the name
  claimed an encode that did not happen (and matched the older file's).

### Added

- `--name-details` names the high dynamic range the file carries, after
  the resolution: `DV`, `HDR10+`, `HDR10`, `HLG` (`2160p DV HDR10 HEVC
  10bit CQ24 NVENC`).

## [0.8.4] - 2026-10-10

### Fixed

- A conversion starting over because an earlier run left segments made
  with other settings (or an older bdtools) deleted the audio and subtitles
  this run had begun demuxing, and failed at the mux after encoding the
  whole film ("open ….thd: no such file or directory"). Only the old
  segments are removed now.

## [0.8.3] - 2026-10-10

### Changed

- The defaults suit the source: with no `--crf`, a 3D disc encodes at 20,
  Ultra HD (HDR10, HDR10+, Dolby Vision or SDR) at 18, HD at 16, each
  measured with VMAF against its source to keep its worst pictures about as
  near it. `--codec` defaults to `h265` (was `h264`), `--bit-depth` to 10
  whenever the codec and encoder can (was the source's), and `--subs-3d` to
  `both` (was `off`; `off` still with `--remux`). The run says which
  settings it chose and for what kind of source.
- NVENC encodes H.264 and HEVC at a constant quality (ffmpeg's
  `-rc vbr -cq`, at `--crf` plus 6, under 50 Mbit/s for HD and 100 above)
  rather than a constant QP. At the same size it is nearer the source on
  every clip measured: +0.1 to +1.0 VMAF on the mean, +0.2 to +2.3 on the
  worst 1% of pictures. A clean film comes out about the size it did at
  the same `--crf`; a grainy one 13–58% smaller, at a lower score on the
  grainiest. A conversion begun with the old one starts over rather than
  resuming.

### Added

- `tools/qcmp`, which measures encodes against their source: PSNR and VMAF
  (mean, 1st percentile, worst), for Matroska sources (a Dolby Vision FEL
  source's layers composed) and 3D Blu-ray streams.

## [0.8.2] - 2026-10-10

### Changed

- `--dv-fel keep` and `reencode` quantise the enhancement layer at `--crf`
  plus 12 by default, not `--crf` less 6. Measured on a 4K FEL feature,
  the finer layer made the file about 3.5 times as large as `compose`'s for a
  picture `compose` comes within a dB of at half that size; now the two
  layers come out the size of `compose` and as near the disc. The
  documentation has the measurements, and recommends `compose` for keeping
  a film near the disc at a fraction of its size. A conversion begun with
  the old default starts over rather than resuming at the new one.

### Fixed

- `--dv-fel keep` and `reencode` held every access unit of the film in
  memory (each kept RPU was a slice of its access unit, about 1 MB a frame
  at 4K), until the system ran out of memory partway through a feature.
  They now hold about 4.5 GB throughout, as `compose` does.
- `--dv-fel keep` and `reencode` on a source whose first pictures have no
  enhancement layer (a clip cut from a film) wrote profile 8.1 without the
  layer, while saying they kept it. They keep it, a neutral enhancement
  layer for those pictures.
- With x265 (`--encoder software`), the enhancement layer could be coded
  with a run of B-frames shorter than the base layer's where its pictures
  went from flat to detailed, and the mux refused the layers. x265 is now
  given every picture's type.
- `--audio-codec truehd` (and other spellings, such as `TRUE-HD`) did not
  find a Matroska source's TrueHD track.

### Added

- `BDTOOLS_PPROF=localhost:6060` serves Go's profiles while bdtools runs,
  for finding where its memory or time goes.
- `tools/felrd` measures Dolby Vision FEL encodes (any `--dv-fel`) against
  the picture the source's layers compose to: size per layer and PSNR.

## [0.8.1] - 2026-10-09

### Fixed

- Kodi playing a 3D conversion drew the flat subtitle tracks' wide lines
  (over 960 pixels, as two-line subtitles often are) larger, shifted right
  and cut off: it takes any subtitle wider than half its plane for a
  side-by-side one. They are written as two pieces side by side now, drawn
  as before by every player. `tools/pgskodi` fixes a `.sup` extracted from
  a film converted earlier.

### Changed

- The MKV's writing application says which bdtools made it (`bdtools
  v0.8.1`), not only that bdtools did.

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

[Unreleased]: https://github.com/brunoga/bdtools/compare/v0.14.2...HEAD
[0.14.2]: https://github.com/brunoga/bdtools/compare/v0.14.1...v0.14.2
[0.14.1]: https://github.com/brunoga/bdtools/compare/v0.14.0...v0.14.1
[0.14.0]: https://github.com/brunoga/bdtools/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/brunoga/bdtools/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/brunoga/bdtools/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/brunoga/bdtools/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/brunoga/bdtools/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/brunoga/bdtools/compare/v0.8.6...v0.9.0
[0.8.6]: https://github.com/brunoga/bdtools/compare/v0.8.5...v0.8.6
[0.8.5]: https://github.com/brunoga/bdtools/compare/v0.8.4...v0.8.5
[0.8.4]: https://github.com/brunoga/bdtools/compare/v0.8.3...v0.8.4
[0.8.3]: https://github.com/brunoga/bdtools/compare/v0.8.2...v0.8.3
[0.8.2]: https://github.com/brunoga/bdtools/compare/v0.8.1...v0.8.2
[0.8.1]: https://github.com/brunoga/bdtools/compare/v0.8.0...v0.8.1
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
