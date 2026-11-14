# Beyond monocular depth: unused information in 2D films

This is a menu of information that a 2D feature film carries and that
the conversion does not use yet. Each idea is written up far enough to
be picked and tried on its own. It was written on 2026-11-09, while the
depth head's fusion blocks and motion modules were being learnt from the
discs' windows. Nothing here starts before that work is finished and
benched.

**What the pipeline exploits today.** Video Depth Anything Small gives a
relative, single-surface depth for every pixel. It is consistent over a
32-frame window, which is about 1.3 s. Its tail is fitted to 35 real 3D
releases, a stretch at a time, and the head's fusion and temporal
modules are being fitted the same way. The depth is mapped to disparity
with defaults graded against the discs. The right eye is rendered from
that, and the disoccluded pixels are filled from the picture itself.

**How we measure.** `tools/stereobench` scores three things. It scores
the rendered right eye against the disc's real right eye, by PSNR and
VMAF. It scores the depth against the disc's measured disparity, as a
correlation and as an error in pixels after an affine fit. And it scores
frame-to-frame jitter. Every idea below names which of these it should
move, and on which clip set. The sets are the 12-clip bench, the glass
set, the wide set and Avatar 3. They are described under "Measured so
far" in [2d-to-3d.md](2d-to-3d.md).

**Why the field leaves these out.** Depth models are trained to be
invariant to scale and shift. They are trained on short clips and for a
single surface, and they are evaluated on depth benchmarks rather than
against a real second eye. A film converter needs the opposite on every
count. We have an asset that nobody publishing has. It is a corpus of
native 3D releases, decoded bit-exactly, with measured disparity, and a
bench that scores the actual second eye.

## 1. The stereographer's choice: predict scale and convergence per shot

The disparity scale of a real 3D shot is its interaxial, and its screen
plane is its convergence. Both are human decisions, and every depth
model in the MiDaS lineage is trained to discard exactly those two
numbers. Our per-stretch affine fit measures them for each training
shot. At conversion time, though, nothing predicts them. The depth
budget comes from fixed grading defaults.

**What the film carries.** The shot scale drives what a stereographer
does, whether the shot is a close-up, a medium shot or a wide one. So do
the focus plane, the amount of camera and subject motion, and the
brightness and contrast of the scene. The position in the cut matters
too, such as whether this is the first shot of a scene, or a shot and
its reverse shot. Dialogue is treated differently from action, and the
ratio of foreground to background counts as well. Close-ups get less
depth and a convergence on the face. Wide landscapes get more. Fast
action gets a shallower budget to stay comfortable. Depth is usually
ramped across a scene, not reset at each cut.

**What we already have.** Every recorded training picture has its affine
fit, which is the scale and shift from the network's depth to the disc's
disparity. It also has the per-stretch fit for whole shots. This covers
35 releases. That is a labelled dataset of stereographic decisions, with
the shot content beside it. Native releases and post-converted ones
should be kept apart. The inbox naming already does this.

**Experiment plan.**

1. Export the per-stretch scale and shift together with the stretch's
   encoder features and some simple shot statistics. The encoder
   features are the pooled DINOv2 tokens the recorder already computes.
   The statistics are depth histogram percentiles, motion magnitude,
   face size and shot length.
2. Fit the simplest predictor first, a ridge regression on the pooled
   features, then a small MLP on the tokens. Report the error against
   the fitted values, per release, with leave-one-release-out
   validation. The baseline is the fixed default.
3. Add the previous shot's prediction as an input, to model the ramping
   across cuts.
4. Plug the predictor into the disparity mapping and bench it. The
   metrics that should move are right-eye PSNR and VMAF on the 12-clip
   bench and the wide set. Depth correlation is already
   affine-invariant, so it will not move. Jitter must not rise.

**Cost.** Low. The data exists, the recorder already writes the
features, and the predictor is tiny. Two to three days to a first
number.

**First step done (2026-11-10, `tools/shapeprobe`).** We measured the
mean robust residual of the mapping, in half-size pixels, on the 5,176
training pictures. Those are 37 films and 875 two-second stretches, with
the network's depth as recorded. One shape for all leaves 7.01. One
shape a film leaves 6.08, and that is an oracle. One shape a stretch
leaves 3.36, which is the oracle again and 52% less. The stretch's
offset alone, with the global scale, leaves 4.52, and its scale alone
leaves 5.13. So the screen plane's place is the bigger half. Shots vary
more within a film than films do between them. The offset's standard
deviation is 0.0085 of the width within a film and 0.0056 between
films. We then tried to predict the stretch's shape with a film held
out. Ridge regression on cheap statistics, which are depth percentiles
and moments, luma and bars, leaves 6.87. Adding the encoder's pooled
tokens, which are 1,536 values from DINOv2's four intermediate layers,
leaves 6.61. That is 6% better than one shape for all, and a tenth of
the oracle's gain. A picture at a time, with six times the samples,
leaves 6.58, which is the same. Rules that anchor one depth statistic of
the shot to a fixed disparity are all worse than one shape for all, at
7.5 to 8.9, whether the statistic is the farthest, the median, the
nearest or the centre. Per-picture normalisation was worse in the same
way in October. So a shot's own content, pooled, tells little of the
stereographer's choice. The gain the oracle shows is real, but it needs
something else. That could be the scene's context across cuts (idea 2),
a metric scale that removes the network's own per-shot ambiguity
(idea 4), or a learnt head on the spatial features rather than on their
mean, which is untried.

**Risks.** Stereographers differ between films, so the predictable part
may be small. If the leave-one-release-out error is not clearly better
than the fixed default, this stops there. Post-converted titles may
follow different conventions from native ones. They should be tested as
a separate group.

## 2. Film-level consistency: one 3D world per scene

A scene returns to the same set, the same actors and the same framings
across dozens of cuts. But the model's memory is 32 frames, and each
shot's depth scale is independent. A native release keeps the same world
across those cuts. An actor does not get closer to the viewer because
the cut went from a wide shot to a medium one. Converters today leak
this kind of error into every dialogue scene.

**What the film carries.** Shot and reverse shot pairs see the same two
people from opposite sides. Establishing shots show the set's geometry
that later close-ups crop into. The same actor appears at many
distances, so their apparent size fixes the scale of every shot they are
in. Re-identifying the set and the actors is cheap with the DINOv2
features we already compute.

**Experiment plan.**

1. Segment the shots and group them into scenes by feature similarity
   and time. Cut detection already exists in the pipeline's clip
   handling, but confirm that.
2. Measure the problem first. On the training discs, compare the spread
   of the fitted per-shot scale and shift within a scene with the spread
   between scenes. If the disc's own parameters spread little within a
   scene while our independent shot fits vary a lot, there is a
   consistency term to win.
3. The cheapest fix is a per-scene memory of the fitted scale and shift.
   The predictor from idea 1 reads and updates it, and it smooths across
   cuts the way a stereographer ramps depth.
4. The deeper fix is a per-scene set of anchors that constrains each
   shot's affine parameters. The anchors are actor heights from face and
   body detections, and set planes from the establishing shot.
5. Bench it. The wide set carries whole scenes with cuts, so its fitted
   PSNR and VMAF and its jitter are the measures. A new bench row for
   the depth-scale discontinuity across cuts would make the effect
   visible. It would be the jump in median disparity at each cut,
   against the disc's jump.

**Cost.** Medium. Step 2 is a day of analysis on the recorded fits.
Steps 3 to 4 are a week or two, mostly in the mapping stage and not in
the network.

**Risks.** Scene grouping errors would propagate a wrong scale to an
unrelated shot. Smoothing must give way quickly at real scene changes.
Prior art on shot grouping is mature. The stereo use of it is the new
part.

## 3. Camera motion as geometry

Dolly, crane, handheld and Steadicam shots triangulate every static
surface, up to a global scale. The model uses motion only to smooth
depth over time. It never does geometry. Structure from motion gives
sharp, geometrically grounded depth on backgrounds and props, which is
exactly where learnt depth is soft. It is also consistent across the
whole shot rather than over 32 frames.

**What the film carries.** Most films have camera motion in a large
fraction of shots. Within a scene, moving shots and locked-off shots see
the same set, so the geometry recovered in the moving ones can calibrate
the static ones. Those are idea 2's anchors. Subject motion is the usual
enemy of SfM. In a film it is mostly actors, and actors can be
segmented.

**Prior art.** Consistent Video Depth Estimation (Luo 2020) and Robust
CVD fine-tune a monocular network per video against SfM constraints. The
feed-forward multi-view models of 2024 to 2025, which are DUSt3R,
MASt3R, MonST3R and VGGT, give pointmaps for a set of frames in one pass
and handle moving objects to a degree. None of this has been applied to
film conversion scored against a real second eye, and none of it uses
cross-shot calibration within a scene.

**Experiment plan.**

1. Pick the moving-camera clips in the 12-clip bench and the wide set.
   Run an off-the-shelf multi-view model on each, VGGT or MonST3R, in
   Python and offline, and fit its depth to the disc's disparity.
   Compare its correlation and pixel error with ours on those clips.
   This tells us how much geometry is worth before we write any Go.
2. If it wins by a margin on backgrounds, the shipping form is a fusion.
   The learnt depth provides the dense prior and the handling of moving
   objects, and the triangulated depth corrects the static regions. A
   first version fits a per-shot, spatially varying correction, which is
   a small network or even a guided filter.
3. Then the cross-shot step. Use the recovered set geometry to constrain
   the static shots of the same scene.
4. Bench depth correlation and pixel error on the moving-camera clips,
   right-eye PSNR on their backgrounds, and jitter.

**Cost.** High for a shipping version, which means a Go implementation
of a multi-view model or of SfM. Low for step 1, which is a few days
with the Python tools. Decide after step 1.

**Risks.** Zoom and rack focus break the static-world assumption. Many
3D titles are heavy on effects, and there the backgrounds are composited
and triangulation means nothing. The scale is still unknown, so idea 1
or idea 4 is needed on top.

## 4. Metric scale from priors and lens physics

Real disparity is a function of metric depth, a fixed interaxial and a
convergence distance. For a point at depth Z, with focal length f and
interaxial b, the disparity is f·b/Z minus a constant. Our relative
depth plus an affine fit is a weaker model than that physics. It lets
the fit absorb errors that a metric model could not.

**What the film carries.** Faces, bodies, doors, cars and furniture have
known sizes. Perspective lines give the focal length. Anamorphic
releases carry a 2x squeeze, with oval bokeh and horizontal flares that
identify the lens family. Metric depth models such as Depth Pro,
Metric3D v2 and UniDepth estimate the focal length and the absolute
depth from a single frame. They were trained on data with camera
intrinsics. Their per-frame estimates are noisy, but a shot has hundreds
of frames to agree on one focal length.

**Experiment plan.**

1. Replace the affine fit in the bench analysis with a physical fit,
   disparity = a / depth_metric + c, using a metric model's depth.
   Compare its residual with our affine fit's residual on the same
   pictures. A lower residual means the physics explains the disc better
   than the affine form does.
2. If it does, the shipping version maps the network's relative depth to
   metric depth with a per-shot scale derived from priors. The priors
   are face sizes from a detector, or a metric model's own focal
   estimate. It then applies the interaxial and convergence predicted by
   idea 1, in metric units. That should generalise better across shots
   than a scale and shift in disparity space.
3. Bench the residual of the fit itself first. Then bench right-eye PSNR
   and VMAF on the 12-clip bench after swapping the mapping.

**Cost.** Medium. Step 1 is analysis with an offline Python model. Step 2
depends on whether we port a metric head or only a face-size estimator.

**First step done (2026-11-10, Depth Pro on the 5,165 training
pictures, `tools/shapeprobe`).** We fitted the disc's disparity as the
interaxial times the focal length over Depth Pro's metric distance, plus
a convergence. The mapping is no more consistent across shots than it is
with our relative depth. One mapping for all leaves 8.33 px, where ours
leaves 7.01. One a film leaves 7.58, where ours leaves 6.07. One a
stretch leaves 3.85, where ours leaves 3.36. The fitted interaxial
varies wildly between stretches, with a mean of 29 mm and a deviation of
31 mm, and so does the convergence, with a deviation of 15 px. So the
per-shot variation of the mapping is not the network's own scale
ambiguity, which a metric depth would remove. It is the stereographer's
choice, or it is beyond what a single-frame metric model can tell on
film frames, with their animation, effects and anamorphic lenses. A
metric scale is not the lever for the mapping.

**Risks.** Metric models are trained mostly on real photographs. They
fail on effects shots, animation and miniatures, which are common in 3D
titles. Animation needs its own scale rule, which is the disc's own
convention, so the training set should be split.

## 5. More than one depth per pixel

Glass, mirrors, water, smoke, rain, snow, dust, lens flares, hair edges
and motion blur all have two or more depths at one pixel in a native
release. A reflection sits behind the mirror's surface. A window shows
the room behind it and the reflection in front of it. A flare is at the
camera. Hair is semi-transparent over the background. A single depth map
cannot represent any of this. That is why the glass set has the lowest
depth correlation of our benches, at 0.776, and why the edges of hair
and blur look cut out in conversions.

**What the film carries.** Under camera motion, reflections and
transmissions move differently from the surface. This is two-layer
motion, and it separates them. Hair and blur have a soft alpha against
the background, which a matting model recovers. Particles and flares are
recognisable by their appearance, and they belong at a known depth,
either the camera or the light.

**Prior art.** Layered depth images and multi-plane images render soft,
layered scenes for the view synthesis of photographs. Stereo
Magnification (2018), 3D Photography with layered depth inpainting (Shih
2020) and 3D Ken Burns (Niklaus 2019) are the examples. Reflection
separation from motion is older computer vision (Xue 2015). None of it
is used in automatic video conversion scored against a real right eye.

**What we already know.** A probe in October 2026, `tools/glassprobe` in
scratch, found that the porthole clip's disc depth is a single layer,
and that this layer would score 34.2 dB against our 25.7. So on that
clip the single depth's error dominates, and layers are not the main
win. The reflection's depth has to be right first. Feathering the hole's
near side by 1 to 2 px was tried on 2026-10-26. It lost 0.1 to 0.3 dB
against the disc, so a soft edge has to come from a real matte, not from
a blur. The fusion-path weights learnt on 2026-11-09 read the reflected
Earth as geometry and inverted the porthole's depth order. The
correlation went from 0.42 to -0.12. Reflections are where a learnt head
drifts first, because the matcher is unsure there and the discs give no
supervision.

**Experiment plan.**

1. Measure where the loss is. On the glass set, segment each picture
   into surface, reflection or transmission, and edge regions. Hand
   labels on a few dozen frames are enough. Report the right-eye PSNR
   per region. That tells us whether reflections, edges or both carry
   the gap.
2. Edges first, since they are everywhere. Render the right eye with a
   soft alpha at the depth discontinuities instead of a hard boundary,
   with a matting pass on the depth edges, and bench right-eye PSNR and
   VMAF on all sets. The cost is low, and the change is in the renderer
   only. A plain feather already lost, as noted above, so the matte has
   to follow the picture's own edge.
3. Reflections second. A two-layer depth for glass and mirror regions,
   where the second layer's depth comes from the head trained on the
   discs' disparity wherever the disc shows a reflection depth. This
   needs a segmenter for reflective regions and a renderer that composes
   two layers.
4. Particles and flares third. Detect them, place them at the camera or
   the screen plane, and composite them over the rendered pair.

**Cost.** Low for step 2. High for step 3, which is a new output of the
network and a new renderer path. Medium for step 4.

**First step done (2026-11-10, `tools/stereobench`'s unsure columns).**
We took the disc's own unsure match as the proxy for glass, reflections
and the textureless. It covers 49% of the glass pictures and 61% of the
twelve's. Our right eye scores better there than over the whole picture.
The glass clips score 33.6 dB there against 28.2 overall, the twelve
31.6 against 27.6, and the porthole 28.6 against 24.9. Those regions are
smooth, and a wrong disparity costs little in them. The loss sits in the
textured, sure regions, such as the porthole's face and ring. The disc's
own depth gets 37.6 there against our 33.6, so there is a gap, but it is
not the main one. Layered rendering is not the lever for the bench. The
porthole's loss is the depth order of what is behind the glass, which is
a single-depth question, as noted above.

**Risks.** Two-layer supervision from the discs is sparse, because the
disparity measurement itself is unsure in exactly these regions. Step 1
may show that the edges carry most of the loss and not the reflections.
In that case step 3 is not worth it.

## 6. Disocclusion content from neighbouring frames

When the right eye looks past a foreground edge, it sees background that
the left eye never saw. We fill those pixels from the picture itself.
But the film often already has them, in the frames before or after,
where the foreground has moved or the camera has panned. A native
release shows the true background there, so right-eye PSNR and VMAF
measure this directly.

**What the film carries.** Actor and camera motion expose background
strips a few pixels wide at every foreground edge, within a second
either side of the picture. Static shots with moving actors and pans
past standing people are the common cases. The same content is also
what idea 3 reconstructs geometrically.

**Prior art.** Video inpainting with flow-guided propagation fills holes
from neighbouring frames. E2FGVI (2022) and ProPainter (2023) do this,
and they are the state of the art for object removal. StereoCrafter
(2024) converts 2D video to stereo with a diffusion inpainter for the
disoccluded regions, from a single frame at a time. Nobody measures
against a real second eye.

**Experiment plan.**

1. Measure the ceiling. On the 12-clip bench, compute the right-eye
   error inside the disoccluded regions only, and compare it with the
   rest of the picture. If disocclusions are a small share of the total
   error, stop.
2. Build a background buffer per shot. Warp the neighbouring frames'
   depth and colour into the current view by depth and motion. The
   decoder's motion vectors are a free first approximation of optical
   flow, and a flow estimator would be a new dependency. Keep the pixels
   that are farther than the current foreground. Read holes from that
   buffer before falling back to the current filler.
3. Bench right-eye PSNR and VMAF on all sets. Watch jitter, since a
   temporal filler can flicker.

**Cost.** Medium. Step 1 is a day. Step 2 is a renderer feature with a
flow dependency, a week or two.

**Risks.** Flow errors put the wrong content in the hole, which looks
worse than a blurred fill. Lighting changes between frames show as
seams. A confidence gate and a blend with the current filler are needed
from the start.

## 7. Defocus and rack focus

Cinema uses a shallow depth of field on purpose. The blur radius
therefore encodes the distance from the focal plane, and a rack focus
sweeps that plane through the scene and reveals the depth ordering over
time. The network learns this only implicitly. The focal plane also
tends to be where the stereographer puts the screen plane, so it is a
direct cue for idea 1's convergence.

**What the film carries.** A per-pixel blur radius, which is depth from
defocus. Its sign, in front of or behind focus, is ambiguous, but the
depth network can resolve that. The timing of a rack focus marks which
subject the scene is about, and that is the subject the stereographer
converges on.

**Experiment plan.**

1. On the training pictures, estimate a blur map. A local sharpness
   measure is enough for a first look. Correlate the in-focus region's
   disc disparity with the disc's screen plane, which is zero disparity.
   If the focused subject sits near zero disparity on most shots, the
   convergence predictor gets a cheap, strong feature.
2. Feed the blur map to the head as an extra input channel and train on
   the discs. The fusion path accepts extra channels with a small
   change. Bench depth correlation and pixel error.

**Cost.** Low for step 1, medium for step 2.

**First step done (2026-11-10, `tools/shapeprobe`).** We anchored the
depth where the picture is sharpest, weighted by the luma's local
contrast, to one fitted disparity, with the global scale. That leaves
7.26 px against 7.01 for one shape for all. It is the best of the
anchoring rules, where the farthest leaves 7.48, the median 7.91 and the
nearest 8.86, but it is still worse than a constant. So the focused
subject is not where the stereographer puts the screen, or not
consistently enough to use. Step 2, the blur map as an input to the
head, is untried.

**Risks.** Deep-focus films and animation have no defocus, so the
feature is simply absent there. Compression softens fine detail and
confuses sharpness measures in dark regions.

## 8. Weaker cues worth a look

Each of these is cheap to test and unlikely to be decisive on its own.

- **Ground plane and shadow contact.** Most shots have a floor. Anything
  standing on it has its depth fixed by where its feet or its shadow
  meet the floor. Fitting a ground plane to the depth and snapping the
  contact points to it is a classic correction for something monocular
  networks often get inconsistent. The test is the depth pixel error on
  pictures with detected people standing.
- **Decoder motion vectors.** The H.264 and HEVC decoders already
  produce per-block motion for free. It is a coarse optical flow for the
  temporal module, for disocclusion filling (idea 6) and for cut
  detection. The test is its agreement with a real flow estimator on
  the bench clips.
- **Shot grammar.** The shot type, from close-up to wide, the shot
  length and the position in a scene can be learnt from the frames.
  They are exactly the variables stereographers reason about. They are
  features for idea 1 rather than an idea of their own.
- **Surround audio.** Dialogue panning and reverb place the speaker left
  to right, and roughly in distance. Object-based tracks such as Atmos
  carry explicit positions. The depth gain is small, but no converter
  has looked at the soundtrack at all, which makes it novel if nothing
  else. The test is the correlation of the speaker's pan with the
  detected speaking face's position and with the disc's disparity.
- **Film grain.** A native release has its grain at the screen plane, or
  independent in each eye. We warp the left eye's grain with the depth.
  Rendering a grain layer at screen depth over the stereo pair instead
  is a small renderer change, and right-eye VMAF should notice it on
  grainy titles.

## Prior art and what is still open

The field has used 3D films as data, and it has studied each cue in
isolation. What nobody has done is combine them for conversion and score
the result against the real second eye. The table is approximate and
from memory. Verify before citing.

| Work | What it did | What it leaves open |
| --- | --- | --- |
| Deep3D (Xie, 2016) | End-to-end right-eye synthesis trained on 3D films | Low resolution, no temporal model, no stereographic parameters |
| MiDaS 3D Movies dataset (Ranftl, 2020) | Trained monocular depth on 3D film frames with flow-derived disparity | Scale and shift discarded by design. Single frames |
| Video Depth Anything (2025) | Temporal consistency over 32 frames with motion modules | Relative depth, no geometry, no scene memory |
| Consistent Video Depth (Luo, 2020), Robust CVD | Per-video fine-tuning against SfM constraints | Minutes per clip. Single shots. Not stereo |
| DUSt3R, MASt3R, MonST3R, VGGT (2024 to 2025) | Feed-forward multi-view geometry, moving objects to a degree | Not film-scale, no cross-shot use, unknown scale |
| Depth Pro, Metric3D v2, UniDepth (2024) | Metric depth and focal length from one frame | Real photographs. Fails on effects and animation |
| Stereo Magnification (2018), LDI inpainting (Shih, 2020), 3D Ken Burns (2019) | Layered and multi-plane rendering for view synthesis | Photographs, not video. No stereo ground truth |
| StereoCrafter (2024) | 2D-to-stereo video with a diffusion inpainter for disocclusions | Single-frame depth, no disc evaluation, slow |
| E2FGVI (2022), ProPainter (2023) | Flow-guided video inpainting from neighbouring frames | Object removal, not disocclusion. Never scored against stereo |

**Open, as far as we know.** Four things seem not to have been done.
Predicting stereographic parameters from content is idea 1. Scene-level
depth consistency across cuts is idea 2. Layered rendering for video
conversion scored against native stereo is idea 5. Any use of the
soundtrack is idea 8. Ideas 3, 4, 6 and 7 are known cues whose
application to conversion is open.

## Suggested order

Start with the ideas whose first step is an analysis of data we already
have. Let each first step decide whether the shipping step is worth
building. Every idea is compared against the recorded reference numbers
under "Measured so far" in [2d-to-3d.md](2d-to-3d.md), and it ships only
if it wins without a hidden trade.

| Order | Idea | First step | Cost to first number | Cost to ship | Metric that must move | Clip set |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 1. Stereographer's choice | Regress fitted scale and shift from pooled features | 2 to 3 days | 1 week | Right-eye PSNR, VMAF | 12-clip bench, wide |
| 2 | 2. Scene consistency | Within- versus between-scene spread of fitted parameters | 1 day | 1 to 2 weeks | Fitted PSNR, jitter, new cut-jump row | wide |
| 3 | 5. Layers, edges first | Right-eye error per region on the glass set | 2 days | 1 week (edges), 1 month (reflections) | Right-eye PSNR, VMAF | glass, 12-clip bench |
| 4 | 6. Temporal disocclusion | Right-eye error inside disoccluded regions | 1 day | 1 to 2 weeks | Right-eye PSNR, VMAF, jitter | 12-clip bench |
| 5 | 7. Defocus | Focused region versus disc screen plane | 1 day | 1 week | Depth error. Feeds idea 1 | 12-clip bench |
| 6 | 4. Metric scale | Physical fit residual versus affine residual | 2 days | 2 to 4 weeks | Fit residual, then PSNR | 12-clip bench |
| 7 | 3. Camera motion | Offline multi-view model on moving-camera clips | 3 to 5 days | 1 to 2 months | Depth correlation and error on backgrounds | 12-clip bench, wide |
| 8 | 8. Weak cues | Ground plane and motion vectors first | 1 day each | varies | Depth error. VMAF for grain | all |
