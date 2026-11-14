# MVC output and the Amlogic MVC decoder

This document records what we learnt in October 2026 while making
`--layout mvc` play on a hardware MVC decoder from the Blu-ray era. The
player was an Ugoos AM6B+, which is an Amlogic S922X, running CoreELEC.
Kodi played MKV files with the dependent view in a `mvcC` BlockAddition.
The decoder is the kernel's `vh264_mvc` driver and its firmware. The
driver parses nothing. The firmware reports each decoded picture with a
POC and a buffer id, and the driver pairs view 0 and view 1 by POC
equality, view 0 first. An unmatched picture leaks its buffer for good,
and that starves the decoder into a frozen image.

The findings decided bdtools' defaults. We record them here, together
with what was excluded, so that the work need not be redone.

## Decisions

- `--layout mvc` codes the left eye with the encoder in this program,
  which is `--encoder builtin` and the default, or with x264, which is
  `--encoder software`. A GPU encoder is refused.
- The built-in encoder writes **one slice per picture**, in both views.
  The right eye had eight until 2026-10-15. The one slice costs nothing
  in speed. A picture's macroblock rows are decided in parallel anyway,
  on a wavefront, in `codeSliceWavefront` in `mvc/depmb.go`, and the B
  pictures of a group and the next reference are coded at once. This
  was confirmed on the player: a 58 s mid-film clip with one slice in
  both views plays flawlessly. That is clip X1, 2026-10-15.
- With an x264 left eye, the right eye is coded with P slices only. The
  same holds for a copied left eye, which is `--encoder copy`. There the
  disc's left eye is kept as it is, four slices and all. This too was
  confirmed on the player: the disc's left eye with a re-coded right eye
  plays flawlessly. That is clip X2, 2026-10-15.

## Method

The test material was clips of Gravity (2013). Some were the first 581
frames, which is 24 s of video only, made with `cmd/mvcenc`. Others were
the first 59 s with audio, made with `bdtools`. They were copied to a
share the player reads. For each clip the player reports one of play,
stutter, distortion or static image, and when asked, the kernel log:

```
echo 0x141 > /sys/module/amvdec_h264mvc/parameters/dbg_mode
# play the clip to the end, then
dmesg > ~/mvc-clip.log
```

The `dbg_mode` bits are these. 0x1 prints per picture, 0x40 prints
drops, 0x80 prints PTS and 0x100 prints buffer recycling.
`tools/amlogic-mvc-log.py LOG` summarises such a log. It reports the
pictures per view, the pairing, the gap between pictures, the buffer-id
divergence between the views, fatal errors and firmware reloads. A
player feeding stall shows as a gap.

Every stream tested was first verified bit-exact. Our decoder was
checked against edge264 on both views with `tools/refdump`, and ffmpeg
against our decoder on the base view. So nothing below is a stream the
spec would call wrong.

## Finding 1: an NVENC left eye fails, whatever its structure

An NVENC-coded base view with a real dependent view shows a static image
and ends early. Sometimes there is a little movement first, then the
static image. The log tells the story. The firmware decodes at a uniform
pace and every view 0 picture is paired. After about 25-35 pictures the
right-eye buffer ids diverge from the left-eye ones, because view 1's
free list releases buffers in a different order. Then comes
`fatal error !!!`, a firmware reload and a replay from POC 0. The player
feeds a few frames more and stops.

We excluded the following causes one at a time. Each was ruled out
either by a clip that still failed or by a control that played.

- B frames: NVENC with `-bf 0` fails. CABAC against CAVLC: CAVLC fails.
- The frame_num and POC counter widths, the nal_ref_idc conventions,
  the number of reference frames from 1 to 4, and MMCO. NVENC writes no
  MMCO.
- SEI NAL units: with them stripped, it still fails. The dependent
  view's slice count, 1 or 8, the dependent view's size, and its
  temporal references: with inter-view prediction only, it still fails.
- The picture structure. x264 told to mimic NVENC, with 3 B frames, no
  adaptive placement, 3 refs and temporal direct, plays.
- The reach of the motion vectors. NVENC references blocks up to 368 px
  beyond the right picture edge, where x264 keeps within about 108 px.
  Clips with the dependent view's vectors forced 96, 160 and 400 px out
  play, and so does an x264 base view with its last column's vectors
  forced 400 px out.
- NVENC in 2D plays. But the 2D path uses a different firmware,
  `vh264`, so this proves nothing about the MVC one.

An NVENC base view with an all-skip dependent view plays, with a
periodic stutter. The log shows that stutter as player feeding stalls
of about 250 ms every 66 frames or so, not as decoder drops.

The conclusion is that the trigger is inside NVENC's slice data, and
NVENC gives no control over it. The cause is unknown.

## Finding 2: the left eye's slice count makes the picture stutter

With our own left-eye encoder and everything else equal, the number of
slices decides whether the picture stutters:

| Slices per picture | Result |
|---|---|
| 8 | minor stutter (two clips, and the full-length bdtools one) |
| 4 | stutter |
| 1 | plays cleanly |

The container timestamps were checked and are regular. The SPS, PPS and
VUI were checked field by field against x264's, and only the level
differs, 41 against 40. The B-frame structure is innocent, because 8
slices without B frames stutters too.

We have a guess, which is not verified. The driver looks up each
picture's PTS by the stream offset the firmware reports, within a 64 KB
window, in `pts_lookup_offset_us64(..., 0x10000, ...)`. If the firmware
reports a later slice's offset, a large multi-slice access unit falls
outside the window and the picture gets no PTS. Against this guess,
Blu-ray discs carry four slices per 1080p picture and play, though they
go through a different feed path. A log with `dbg_mode` bit 0x80, the
PTS prints, during an 8-slice clip would settle it.

## Finding 3: the right eye's B slices work on our left eye, not on x264's

The dependent-view coder gained B slices, 8x8 transforms, partitions and
intra modes. Its B slices predict from a past and a future temporal
reference and from the base view, and have skip and direct modes. How
that plays depends on the left eye:

| Left eye | Right eye | Result |
|---|---|---|
| ours, 1 slice | B slices, everything on | plays cleanly |
| x264 | B slices, everything on | stutter and distortion |
| x264 | P slices only, everything else on | plays; faint artifacts suspected, not confirmed |
| x264 | the older P-only coder | plays cleanly (earlier rounds) |

The x264 stream has no B-pyramid and no MMCO, and its B pictures are
placed adaptively, one or three between references. Our left eye has a
fixed three. Which of these differences matters is not known. So the
x264 path keeps the right eye in P slices, and the built-in encoder is
the default.

## Closed: the two micro-stutters

The default of 2026-10-14 was a built-in left eye with one slice, B
slices in both views, and the right eye still in eight slices. It showed
about two micro-stutters in the first half of a 59 s clip. With the
right eye in one slice too, a 58 s mid-film clip (X1) and a copy-mode
clip (X2) played flawlessly on 2026-10-15. The right eye's slice count
is the likely cause, which fits finding 2. It was not isolated further.

## Reproducing

`cmd/mvcenc` codes both views from raw YUV. It has the switches the
clips above used:

```
mvcenc -base left.yuv -dep right.yuv -o out.264 -mkv out.mkv -mvcc \
    [-bslices N] [-bframes N] [-bqp QP] [-qp QP] [-no-b] [-interview-only] [-slices N]
mvcenc -dep right.yuv -o out.264 -mkv out.mkv -mvcc base.264   # a given left eye
```

`-bslices` and `-bframes` shape the left eye. `-no-b`,
`-interview-only` and `-slices` shape the right eye. `-mkv` with `-mvcc`
writes what bdtools writes, an MKV with the dependent view in a `mvcC`
BlockAddition. That is the file to put in front of the player.

## MV-HEVC on the Meta Quest (2026-10-16)

We tried `--layout mvhevc` files on a Quest with 4XVR Video Player, the
one Quest player that advertises MV-HEVC. The headset's own player is no
use here. It shows the base layer alone for any local MV-HEVC file, a
known-good iPhone-style sample included. Its 3D modes are side by side
and top/bottom. Meta's documented route for MV-HEVC is to upload iPhone
recordings through the phone app, which are converted on Meta's
servers.

4XVR decodes MV-HEVC with a decoder of its own. It calls this
"soft-decoding" and has had it since its version 1.2.10 of January
2024, before ffmpeg could decode MV-HEVC. The decoder was written for
iPhone 15 Pro files, and it plays the iPhone-style sample. On our files
it refuses 10-bit, citing "performance". On 8-bit it shows the second
layer as blocks, and it puts that layer on the left eye. A diagnostic
file with a different pattern in each layer showed both eyes with the
base layer's picture, the left one smeared. So the second layer comes
out as its inter-view prediction with the residual mangled. The result
was the same with no B-frames, with every picture a reference, with
BT.709 stated, and in a QuickTime container. x265's multiview output,
muxed by the same writer, does not play in it at all. ffmpeg 8 decodes
every one of these files cleanly in both views, and they follow Apple's
HEVC Stereo Video profile point by point. 4XVR is not a reference for
MV-HEVC.

SKYBOX plays them. Its MV-HEVC support is in the 2.0.2 beta, build 3854
of October 2026. The store build 1.2.1 shows the base layer only. The
beta is reached through Quest Settings > Privacy & safety > Installed
apps > SKYBOX > Release Channels > "2.0.2 Preview". It played the
iPhone-style sample, the NVENC file (Q6, with the fixes below) and an
x265 multiview encode of the same minute, all correctly in 3D. It has
one quirk. It decodes the two views into a side-by-side picture, and
the 3D mode has to be set to side by side by hand. Until it is, the
eyes show next to each other.

Two defects of the writer were found and fixed on the way. The camera
baseline and comfort boxes belong inside the stereo view box, `eyes`.
The edit list must start at the first picture shown, after the B-frame
lead. The stereo NVENC path now codes every B-frame as a reference, as
Apple's own files do.
