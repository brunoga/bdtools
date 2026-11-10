# Beyond monocular depth: unused information in 2D films

A menu of information a 2D feature film carries that the conversion does
not use yet, each written up far enough to be picked and tried on its own.
Written 2026-11-09, while the depth head's fusion blocks and motion
modules were being learnt from the discs' windows; nothing here starts
before that is finished and benched.

**What the pipeline exploits today.** Video Depth Anything Small gives a
relative, single-surface depth per pixel, consistent over a 32-frame
window (about 1.3 s). Its tail is fitted to 35 real 3D releases a stretch
at a time, and the head's fusion and temporal modules are being fitted
the same way. The depth is mapped to disparity with defaults graded
against the discs, and the right eye is rendered from it, with disoccluded
pixels filled from the picture itself.

**How we measure.** `tools/stereobench` scores the rendered right eye
against the disc's real right eye (PSNR and VMAF), the depth against the
disc's measured disparity (correlation and error in pixels after an affine
fit), and frame-to-frame jitter. Every idea below names which of these it
should move and on which clip set: the 12-clip bench, the glass set, the
wide set, or Avatar 3 (see "Measured so far" in [2d-to-3d.md](2d-to-3d.md)).

**Why the field leaves these out.** Depth models are trained to be
scale-and-shift invariant, on short clips, for a single surface, and are
evaluated on depth benchmarks rather than against a real second eye. A
film converter needs the opposite on every count. The asset we have and
nobody publishing has is a corpus of native 3D releases, decoded
bit-exactly, with measured disparity and a bench that scores the actual
second eye.

## 1. The stereographer's choice: predict scale and convergence per shot

The disparity scale (interaxial) and the screen plane (convergence) of a
real 3D shot are human decisions, and every depth model in the MiDaS
lineage is trained to discard exactly those two numbers. Our per-stretch
affine fit measures them for each training shot, but at conversion time
nothing predicts them: the depth budget comes from fixed grading defaults.

**What the film carries.** Shot scale (close-up, medium, wide), the focus
plane, the amount of camera and subject motion, the brightness and
contrast of the scene, the position in the cut (first shot of a scene,
shot and reverse shot), dialogue versus action, and the ratio of
foreground to background all drive what a stereographer does. Close-ups
get less depth and a convergence on the face. Wide landscapes get more.
Fast action gets a shallower budget to stay comfortable. Depth is usually
ramped across a scene, not reset at each cut.

**What we already have.** Every recorded training picture has its affine
fit (scale and shift of network depth to disc disparity) and the
per-stretch fit for whole shots, over 35 releases. That is a labelled
dataset of stereographic decisions with the shot content beside it.
Native releases and post-converted ones should be separated; the inbox
naming already does this.

**Experiment plan.**

1. Export the per-stretch scale and shift with the stretch's encoder
   features (pooled DINOv2 tokens already computed by the recorder) and
   simple shot statistics: depth histogram percentiles, motion magnitude,
   face size, shot length.
2. Fit the simplest predictor first (ridge regression on pooled
   features), then a small MLP on the tokens. Report the error against
   the fitted values, per release, with leave-one-release-out validation.
   The baseline is the fixed default.
3. Add the previous shot's prediction as an input to model ramping across
   cuts.
4. Plug the predictor into the disparity mapping and bench: the metric
   that should move is right-eye PSNR and VMAF on the 12-clip bench and
   the wide set, since depth correlation is already affine-invariant.
   Jitter must not rise.

**Cost.** Low. The data exists, the recorder already writes the features,
and the predictor is tiny. Two to three days to a first number.

**First step done (2026-11-10, `tools/shapeprobe`).** On the 5,176
training pictures (37 films, 875 two-second stretches, the network's
depth as recorded), the mean robust residual of the mapping in half-size
pixels: one shape for all 7.01; one a film 6.08 (an oracle); one a
stretch 3.36 (the oracle, 52% less); the stretch's offset alone with
the global scale 4.52, its scale alone 5.13: the screen plane's place is
the bigger half. Shots vary more within a film (offset sd 0.0085 of the
width) than films do between them (0.0056). Predicting the stretch's
shape with a film held out: ridge regression on cheap statistics
(depth percentiles and moments, luma, bars) 6.87; on the encoder's
pooled tokens (DINOv2's four intermediate layers, 1,536 values) with
them 6.61, 6% better than one shape for all, a tenth of the oracle's
gain (a picture at a time, six times the samples: 6.58, the same). Rules anchoring one depth statistic of the shot to a fixed
disparity (the farthest, the median, the nearest, the centre) are all
worse than one shape for all (7.5 to 8.9), as per-picture normalisation
was in October. So a shot's own content, pooled, tells little of the
stereographer's choice; the gain the oracle shows is real but needs
something else: the scene's context across cuts (idea 2), a metric
scale that removes the network's own per-shot ambiguity (idea 4), or
a learnt head on the spatial features rather than their mean (untried).

**Risks.** Stereographers differ between films, so the predictable part
may be small. If the leave-one-release-out error is not clearly better
than the fixed default, this stops there. Post-converted titles may follow
different conventions from native ones and should be tested as a separate
group.

## 2. Film-level consistency: one 3D world per scene

A scene returns to the same set, the same actors and the same framings
across dozens of cuts, but the model's memory is 32 frames and each shot's
depth scale is independent. A native release keeps the same world across
those cuts: an actor does not get closer to the viewer because the cut
went from a wide to a medium shot. Converters today leak this kind of
error into every dialogue scene.

**What the film carries.** Shot and reverse shot pairs see the same two
people from opposite sides. Establishing shots show the set's geometry
that later close-ups crop into. The same actor appears at many distances,
so their apparent size fixes the scale of every shot they are in.
Re-identifying the set and the actors is cheap with the DINOv2 features we
already compute.

**Experiment plan.**

1. Shot segmentation (cut detection already exists in the pipeline's clip
   handling; confirm) and grouping of shots into scenes by feature
   similarity and time.
2. Measure the problem first: on the training discs, the spread of the
   fitted per-shot scale and shift within a scene versus between scenes.
   If within-scene spread of the disc's own parameters is small while our
   independent shot fits vary a lot, there is a consistency term to win.
3. Cheapest fix: a per-scene memory of fitted scale and shift that the
   predictor from idea 1 reads and updates, smoothing across cuts like a
   stereographer ramps depth.
4. Deeper fix: a per-scene anchor set (actor heights from face and body
   detections, set planes from the establishing shot) that constrains
   each shot's affine parameters.
5. Bench: the wide set carries whole scenes with cuts, so its fitted PSNR
   and VMAF and its jitter are the measures. A new bench row for
   depth-scale discontinuity across cuts (the jump in median disparity at
   each cut, against the disc's jump) would make the effect visible.

**Cost.** Medium. Step 2 is a day of analysis on the recorded fits. Steps
3 to 4 are a week or two, mostly in the mapping stage, not in the network.

**Risks.** Scene grouping errors would propagate a wrong scale to an
unrelated shot. Smoothing must give way quickly at real scene changes.
Prior art on shot grouping is mature; the stereo use of it is the new
part.

## 3. Camera motion as geometry

Dolly, crane, handheld and Steadicam shots triangulate every static
surface up to a global scale. The model uses motion only to smooth depth
over time; it never does geometry. Structure from motion gives sharp,
geometrically grounded depth on backgrounds and props, exactly where
learnt depth is soft, and it is consistent across the whole shot rather
than 32 frames.

**What the film carries.** Most films have camera motion in a large
fraction of shots. Within a scene, moving shots and locked-off shots see
the same set, so geometry recovered in the moving ones can calibrate the
static ones (idea 2's anchors). Subject motion, the usual enemy of SfM, is
mostly actors, who are segmentable.

**Prior art.** Consistent Video Depth Estimation (Luo 2020) and Robust
CVD fine-tune a monocular network per video against SfM constraints.
Feed-forward multi-view models (DUSt3R, MASt3R, MonST3R, VGGT, 2024 to
2025) give pointmaps for a set of frames in one pass and handle moving
objects to a degree. None of this is applied to film conversion scored
against a real second eye, and none uses cross-shot calibration within a
scene.

**Experiment plan.**

1. Pick the moving-camera clips in the 12-clip bench and the wide set.
   Run an off-the-shelf multi-view model (VGGT or MonST3R, Python,
   offline) on each and fit its depth to the disc's disparity. Compare
   its correlation and pixel error with ours on those clips. This tells
   us how much geometry is worth before writing any Go.
2. If it wins by a margin on backgrounds, the shipping form is a fusion:
   the learnt depth provides the dense prior and the moving-object
   handling, the triangulated depth corrects the static regions. A first
   version fits a per-shot spatially varying correction, which is a small
   network or even a guided filter.
3. Then the cross-shot step: use the recovered set geometry to constrain
   static shots of the same scene.
4. Bench: depth correlation and pixel error on the moving-camera clips;
   right-eye PSNR on their backgrounds; jitter.

**Cost.** High for a shipping version (a Go implementation of a
multi-view model or of SfM). Low for step 1, a few days with the Python
tools. Decide after step 1.

**Risks.** Zoom and rack focus break the static-world assumption. Many 3D
titles are effects-heavy, where backgrounds are composited and
triangulation is meaningless. Scale is still unknown, so idea 1 or 4 is
needed on top.

## 4. Metric scale from priors and lens physics

Real disparity is a function of metric depth, a fixed interaxial and a
convergence distance: for a point at depth Z with focal length f and
interaxial b, the disparity is f·b/Z minus a constant. Our relative depth
plus affine fit is a weaker model than that physics, and it lets the fit
absorb errors that a metric model could not.

**What the film carries.** Faces, bodies, doors, cars and furniture have
known sizes. Perspective lines give the focal length, and anamorphic
releases carry a 2x squeeze with oval bokeh and horizontal flares that
identify the lens family. Metric depth models (Depth Pro, Metric3D v2,
UniDepth) estimate focal length and absolute depth from a single frame,
and were trained on data with camera intrinsics. Their per-frame estimates
are noisy but a shot has hundreds of frames to agree on one focal length.

**Experiment plan.**

1. Replace the affine fit in the bench analysis with a physical fit:
   disparity = a / depth_metric + c, using a metric model's depth, and
   compare the residual with our affine fit's residual on the same
   pictures. A lower residual means the physics explains the disc better
   than the affine form.
2. If it does, the shipping version maps the network's relative depth to
   metric depth with a per-shot scale derived from priors (face sizes
   from a detector, or a metric model's own focal estimate), then applies
   the predicted interaxial and convergence from idea 1 in metric units,
   which should generalise better across shots than disparity-space scale
   and shift.
3. Bench: the residual of the fit itself first, then right-eye PSNR and
   VMAF on the 12-clip bench after swapping the mapping.

**Cost.** Medium. Step 1 is analysis with an offline Python model. Step 2
depends on whether we port a metric head or only a face-size estimator.

**First step done (2026-11-10, Depth Pro on the 5,165 training
pictures, `tools/shapeprobe`).** With the disc's disparity fitted as
interaxial times focal length over Depth Pro's metric distance, plus a
convergence, the mapping is no more consistent across shots than with
our relative depth: one mapping for all leaves 8.33 px (ours 7.01), one
a film 7.58 (ours 6.07), one a stretch 3.85 (ours 3.36). The fitted
interaxial varies wildly between stretches (mean 29 mm, deviation 31 mm)
and the convergence too (deviation 15 px), so the per-shot variation of
the mapping is not the network's own scale ambiguity that a metric
depth would remove; it is the stereographer's choice, or beyond what a
single-frame metric model can tell on film frames (animation, effects,
anamorphic lenses). A metric scale is not the lever for the mapping.

**Risks.** Metric models are trained mostly on real photographs and fail
on effects shots, animation and miniatures, which are common in 3D
titles. Animation needs its own scale rule (the disc's own convention), so
the training set should be split.

## 5. More than one depth per pixel

Glass, mirrors, water, smoke, rain, snow, dust, lens flares, hair edges
and motion blur all have two or more depths at one pixel in a native
release. A reflection sits behind the mirror's surface; a window shows the
room behind it and the reflection in front; a flare is at the camera; hair
is semi-transparent over the background. A single depth map cannot
represent any of this, which is why the glass set has the lowest depth
correlation of our benches (0.776) and why edges of hair and blur look cut
out in conversions.

**What the film carries.** Reflections and transmissions move differently
from the surface under camera motion (two-layer motion), which separates
them. Hair and blur have soft alpha against the background that a matting
model recovers. Particles and flares are recognisable by appearance and
belong at a known depth (the camera or the light).

**Prior art.** Layered depth images and multi-plane images (Stereo
Magnification, 2018; 3D Photography with layered depth inpainting, Shih
2020; 3D Ken Burns, Niklaus 2019) render soft, layered scenes for view
synthesis of photographs. Reflection separation from motion is older
computer vision (Xue 2015). None is used in automatic video conversion
scored against a real right eye.

**What we already know.** A probe in October 2026 (`tools/glassprobe`,
scratch) found the porthole clip's disc depth is a single layer that
would score 34.2 dB against our 25.7, so on that clip the single depth's
error dominates and layers are not the main win: the reflection's depth
has to be right first. Feathering the hole's near side by 1 to 2 px was
tried on 2026-10-26 and lost 0.1 to 0.3 dB against the disc, so a soft
edge has to come from a real matte, not a blur. The fusion-path weights
learnt on 2026-11-09 read the reflected Earth as geometry and inverted
the porthole's depth order (correlation 0.42 to -0.12): reflections are
where a learnt head drifts first, because the matcher is unsure there and
the discs give no supervision.

**Experiment plan.**

1. Measure where the loss is: on the glass set, segment each picture into
   surface, reflection or transmission, and edge regions (hand labels on
   a few dozen frames are enough) and report the right-eye PSNR per
   region. That tells us whether reflections, edges or both carry the
   gap.
2. Edges first, since they are everywhere: render the right eye with a
   soft alpha at depth discontinuities (a matting pass on the depth
   edges) instead of a hard boundary, and bench right-eye PSNR and VMAF
   on all sets. Low cost, in the renderer only. A plain feather already
   lost (above); the matte has to follow the picture's own edge.
3. Reflections second: a two-layer depth for glass and mirror regions,
   with the second layer's depth from the head trained on the discs'
   disparity where the disc shows a reflection depth. Needs a segmenter
   for reflective regions and a renderer that composes two layers.
4. Particles and flares third: detect, place at the camera or screen
   plane, composite over the rendered pair.

**Cost.** Low for step 2, high for step 3 (a new output of the network
and a new renderer path), medium for step 4.

**Risks.** Two-layer supervision from the discs is sparse: the disparity
measurement itself is unsure in exactly these regions. Step 1 may show
the edges, not the reflections, carry most of the loss, in which case
step 3 is not worth it.

## 6. Disocclusion content from neighbouring frames

When the right eye looks past a foreground edge it sees background the
left eye never saw. We fill those pixels from the picture itself; the film
often already has them, in the frames before or after, where the
foreground has moved or the camera has panned. A native release shows the
true background there, so right-eye PSNR and VMAF measure this directly.

**What the film carries.** Actor and camera motion expose background
strips a few pixels wide at every foreground edge, within a second either
side of the picture. Static shots with moving actors and pans past
standing people are the common cases. The same content is also what idea
3 reconstructs geometrically.

**Prior art.** Video inpainting with flow-guided propagation (E2FGVI
2022, ProPainter 2023) fills holes from neighbouring frames and is state
of the art for object removal. StereoCrafter (2024) converts 2D video to
stereo with a diffusion inpainter for the disoccluded regions, from a
single frame at a time. Nobody measures against a real second eye.

**Experiment plan.**

1. Measure the ceiling: on the 12-clip bench, compute the right-eye error
   inside the disoccluded regions only, versus the rest of the picture.
   If disocclusions are a small share of the total error, stop.
2. Build a background buffer per shot: warp neighbouring frames' depth
   and colour into the current view by depth and motion (the decoder's
   motion vectors are a free first approximation of optical flow; a flow
   estimator would be a new dependency), keep the pixels that are farther
   than the current foreground, and read holes from that buffer before
   falling back to the current filler.
3. Bench right-eye PSNR and VMAF on all sets; watch jitter, since a
   temporal filler can flicker.

**Cost.** Medium. Step 1 is a day. Step 2 is a renderer feature with a
flow dependency, a week or two.

**Risks.** Flow errors put the wrong content in the hole, which looks
worse than a blurred fill. Lighting changes between frames show as seams.
A confidence gate and a blend with the current filler are needed from the
start.

## 7. Defocus and rack focus

Cinema uses shallow depth of field on purpose, so blur radius encodes
distance from the focal plane, and a rack focus sweeps that plane through
the scene and reveals depth ordering over time. The network learns this
only implicitly. The focal plane also tends to be where the stereographer
puts the screen plane, so it is a direct cue for idea 1's convergence.

**What the film carries.** Per-pixel blur radius (depth from defocus), the
sign ambiguity of which (in front of or behind focus) the depth network
can resolve. The timing of a rack focus marks which subject the scene is
about, which the stereographer converges on.

**Experiment plan.**

1. On the training pictures, estimate a blur map (a local sharpness
   measure is enough for a first look) and correlate the in-focus region's
   disc disparity with the disc's screen plane (zero disparity). If the
   focused subject sits near zero disparity on most shots, the convergence
   predictor gets a cheap, strong feature.
2. Feed the blur map as an extra input channel to the head (the fusion
   path accepts extra channels with a small change) and train on the
   discs; bench depth correlation and pixel error.

**Cost.** Low for step 1, medium for step 2.

**First step done (2026-11-10, `tools/shapeprobe`).** The depth where the
picture is sharpest (weighted by the luma's local contrast) anchored to
one fitted disparity, with the global scale: 7.26 px against one shape
for all's 7.01. The best of the anchoring rules (the farthest 7.48, the
median 7.91, the nearest 8.86), but still worse than a constant, so the
focused subject is not where the stereographer puts the screen, or not
consistently enough to use. Step 2 (the blur map as an input to the head)
is untried.

**Risks.** Deep-focus films and animation have no defocus; the feature is
simply absent there. Compression softens fine detail and confuses
sharpness measures in dark regions.

## 8. Weaker cues worth a look

Each of these is cheap to test and unlikely to be decisive on its own.

- **Ground plane and shadow contact.** Most shots have a floor, and
  anything standing on it has its depth fixed by where its feet or shadow
  meet the floor. Fitting a ground plane to the depth and snapping
  contact points to it is a classic correction that monocular networks
  often get inconsistent. Test: depth pixel error on pictures with
  detected people standing.
- **Decoder motion vectors.** The H.264 and HEVC decoders already produce
  per-block motion for free. They are a coarse optical flow for the
  temporal module, for disocclusion filling (idea 6) and for cut
  detection. Test: agreement with a real flow estimator on the bench
  clips.
- **Shot grammar.** Shot type (close-up to wide), shot length and
  position in a scene are learnable from the frames and are exactly the
  variables stereographers reason about. They are features for idea 1
  rather than an idea of their own.
- **Surround audio.** Dialogue panning and reverb localise the speaker
  left to right and roughly in distance. Object-based tracks (Atmos)
  carry explicit positions. The depth gain is small, but no converter has
  looked at the soundtrack at all, which makes it novel if nothing else.
  Test: correlation of speaker pan with the detected speaking face's
  position and disc disparity.
- **Film grain.** A native release has grain at the screen plane or
  independent per eye. Rendering a grain layer at screen depth over the
  stereo pair, instead of warping the left eye's grain with the depth, is
  a small renderer change that right-eye VMAF should notice on grainy
  titles.

## Prior art and what is still open

The field has used 3D films as data and has studied each cue in
isolation. What nobody has done is combine them for conversion and score
against the real second eye. Approximate from memory; verify before
citing.

| Work | What it did | What it leaves open |
| --- | --- | --- |
| Deep3D (Xie, 2016) | End-to-end right-eye synthesis trained on 3D films | Low resolution, no temporal model, no stereographic parameters |
| MiDaS 3D Movies dataset (Ranftl, 2020) | Trained monocular depth on 3D film frames with flow-derived disparity | Scale and shift discarded by design; single frames |
| Video Depth Anything (2025) | Temporal consistency over 32 frames with motion modules | Relative depth, no geometry, no scene memory |
| Consistent Video Depth (Luo, 2020), Robust CVD | Per-video fine-tuning against SfM constraints | Minutes per clip; single shots; not stereo |
| DUSt3R, MASt3R, MonST3R, VGGT (2024 to 2025) | Feed-forward multi-view geometry, moving objects to a degree | Not film-scale, no cross-shot use, unknown scale |
| Depth Pro, Metric3D v2, UniDepth (2024) | Metric depth and focal length from one frame | Real photographs; fails on effects and animation |
| Stereo Magnification (2018), LDI inpainting (Shih, 2020), 3D Ken Burns (2019) | Layered and multi-plane rendering for view synthesis | Photographs, not video; no stereo ground truth |
| StereoCrafter (2024) | 2D-to-stereo video with a diffusion inpainter for disocclusions | Single-frame depth, no disc evaluation, slow |
| E2FGVI (2022), ProPainter (2023) | Flow-guided video inpainting from neighbouring frames | Object removal, not disocclusion; never stereo-scored |

**Open, as far as we know:** predicting stereographic parameters from
content (idea 1), scene-level depth consistency across cuts (idea 2),
layered rendering for video conversion scored against native stereo (idea
5), and any use of the soundtrack (idea 8). Ideas 3, 4, 6 and 7 are known
cues with an open application to conversion.

## Suggested order

Start with the ideas whose first step is analysis of data we already
have, and let each first step decide whether the shipping step is worth
building. Every one is compared against the recorded reference numbers in
[2d-to-3d.md](2d-to-3d.md) ("Measured so far") and shipped only if it
wins without a hidden trade.

| Order | Idea | First step | Cost to first number | Cost to ship | Metric that must move | Clip set |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 1. Stereographer's choice | Regress fitted scale and shift from pooled features | 2 to 3 days | 1 week | Right-eye PSNR, VMAF | 12-clip bench, wide |
| 2 | 2. Scene consistency | Within- versus between-scene spread of fitted parameters | 1 day | 1 to 2 weeks | Fitted PSNR, jitter, new cut-jump row | wide |
| 3 | 5. Layers, edges first | Right-eye error per region on the glass set | 2 days | 1 week (edges), 1 month (reflections) | Right-eye PSNR, VMAF | glass, 12-clip bench |
| 4 | 6. Temporal disocclusion | Right-eye error inside disoccluded regions | 1 day | 1 to 2 weeks | Right-eye PSNR, VMAF, jitter | 12-clip bench |
| 5 | 7. Defocus | Focused region versus disc screen plane | 1 day | 1 week | Depth error; feeds idea 1 | 12-clip bench |
| 6 | 4. Metric scale | Physical fit residual versus affine residual | 2 days | 2 to 4 weeks | Fit residual, then PSNR | 12-clip bench |
| 7 | 3. Camera motion | Offline multi-view model on moving-camera clips | 3 to 5 days | 1 to 2 months | Depth correlation and error on backgrounds | 12-clip bench, wide |
| 8 | 8. Weak cues | Ground plane and motion vectors first | 1 day each | varies | Depth error; VMAF for grain | all |
