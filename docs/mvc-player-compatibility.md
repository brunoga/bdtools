# MVC output and the Amlogic MVC decoder

What was learnt, in October 2026, making `--layout mvc` play on a
Blu-ray-era hardware MVC decoder: an Ugoos AM6B+ (Amlogic S922X) running
CoreELEC, Kodi playing MKV files with the dependent view in a `mvcC`
BlockAddition. The decoder is the kernel's `vh264_mvc` driver and its
firmware. The driver parses nothing: the firmware reports each decoded
picture with a POC and a buffer id, the driver pairs view 0 and view 1 by
POC equality (view 0 first), and an unmatched picture leaks its buffer for
good, which starves the decoder into a frozen image.

The findings decided bdtools' defaults; this records them, with what was
excluded, so the work need not be redone.

## Decisions

- `--layout mvc` codes the left eye with the encoder in this program
  (`--encoder builtin`, the default) or x264 (`--encoder software`). A GPU
  encoder is refused.
- The built-in encoder writes **one slice per picture**, in both views
  (the right eye had eight until 2026-10-15). A picture's macroblock
  rows are decided in parallel anyway (a wavefront,
  `codeSliceWavefront` in `mvc/depmb.go`), and the B pictures of a group
  and the next reference are coded at once, so the one slice costs
  nothing in speed. Confirmed on the player: a 58 s mid-film clip with
  one slice in both views plays flawlessly (X1, 2026-10-15).
- With an x264 left eye, the right eye is coded with P slices only. So
  is it with a copied one (`--encoder copy`: the disc's left eye kept as
  it is, four slices and all). Confirmed on the player: the disc's left
  eye with a re-coded right eye plays flawlessly (X2, 2026-10-15).

## Method

Clips of Gravity (2013), the first 581 frames (24 s, video only) made with
`cmd/mvcenc`, or the first 59 s with audio made with `bdtools`, copied to
a share the player reads. The player reports play / stutter / distortion /
static image, and the kernel log when asked:

```
echo 0x141 > /sys/module/amvdec_h264mvc/parameters/dbg_mode
# play the clip to the end, then
dmesg > ~/mvc-clip.log
```

`dbg_mode` bits: 0x1 per-picture prints, 0x40 drop prints, 0x80 PTS,
0x100 buffer recycling. `tools/amlogic-mvc-log.py LOG` summarises such a
log: pictures per view, pairing, the gap between pictures (a player
feeding stall shows as a gap), buffer-id divergence between the views,
fatal errors and firmware reloads.

Every stream tested was first verified bit-exact: our decoder against
edge264 on both views (`tools/refdump`), and ffmpeg against our decoder on
the base view. Nothing below is a stream the spec would call wrong.

## Finding 1: an NVENC left eye fails, whatever its structure

An NVENC-coded base view with a real dependent view shows a static image
(or a little movement, then static) and ends early. The log shows the
firmware decoding at a uniform pace with every view 0 picture paired, the
right-eye buffer ids diverging from the left-eye ones after ~25-35
pictures (view 1's free list releases buffers in a different order), then
`fatal error !!!`, a firmware reload, a replay from POC 0 and the player
feeding a few frames more and stopping.

Excluded, one at a time, each by a clip that still failed or a control
that played:

- B frames (NVENC `-bf 0` fails), CABAC vs CAVLC (CAVLC fails).
- frame_num / POC counter widths, nal_ref_idc conventions, the number of
  reference frames (1 to 4), MMCO (NVENC writes none).
- SEI NAL units (stripped: fails), the dependent view's slice count (1 or
  8), the dependent view's size, its temporal references (inter-view
  prediction only: fails).
- Picture structure: x264 told to mimic NVENC (3 B frames, no adaptive
  placement, 3 refs, temporal direct) plays.
- Motion vector reach: NVENC references blocks up to 368 px beyond the
  right picture edge, x264 keeps within ~108 px. Clips with the dependent
  view's vectors forced 96, 160 and 400 px out play; an x264 base view
  with its last column's vectors forced 400 px out plays.
- NVENC in 2D plays, but the 2D path uses a different firmware (`vh264`),
  so it proves nothing about the MVC one.

An NVENC base view with an all-skip dependent view plays, with periodic
stutter that the log shows as ~250 ms player feeding stalls every ~66
frames, not decoder drops.

Conclusion: the trigger is inside NVENC's slice data and NVENC gives no
control over it. Cause unknown.

## Finding 2: the left eye's slice count makes the picture stutter

With our own left-eye encoder, everything else equal:

| Slices per picture | Result |
|---|---|
| 8 | minor stutter (two clips, and the full-length bdtools one) |
| 4 | stutter |
| 1 | plays cleanly |

The container timestamps were checked regular, and the SPS, PPS and VUI
were checked field by field against x264's (only the level differs, 41
against 40). The B-frame structure is innocent (8 slices without B frames
stutters too).

A guess, not verified: the driver looks up each picture's PTS by the
stream offset the firmware reports, within a 64 KB window
(`pts_lookup_offset_us64(..., 0x10000, ...)`); if the firmware reports a
later slice's offset, a large multi-slice access unit falls outside the
window and the picture gets no PTS. Against it: Blu-ray discs carry four
slices per 1080p picture and play, though through a different feed path.
A log with `dbg_mode` bit 0x80 (PTS prints) during an 8-slice clip would
settle it.

## Finding 3: the right eye's B slices work on our left eye, not on x264's

The dependent-view coder gained B slices (prediction from a past and a
future temporal reference and from the base view, skip and direct modes),
8x8 transforms, partitions and intra modes.

| Left eye | Right eye | Result |
|---|---|---|
| ours, 1 slice | B slices, everything on | plays cleanly |
| x264 | B slices, everything on | stutter and distortion |
| x264 | P slices only, everything else on | plays; faint artifacts suspected, not confirmed |
| x264 | the older P-only coder | plays cleanly (earlier rounds) |

The x264 stream has no B-pyramid and no MMCO; its B pictures are placed
adaptively (one or three between references). Our left eye has a fixed
three. Which difference matters is not known. The x264 path keeps the
right eye in P slices; the built-in encoder is the default.

## Closed: the two micro-stutters

The default of 2026-10-14 (built-in left eye with one slice, B slices in
both views, the right eye still in eight slices) showed about two
micro-stutters in the first half of a 59 s clip. With the right eye in
one slice too, a 58 s mid-film clip (X1) and a copy-mode clip (X2)
played flawlessly on 2026-10-15. The right eye's slice count is the
likely cause, in line with finding 2; it was not isolated further.

## Reproducing

`cmd/mvcenc` codes both views from raw YUV, with the switches the clips
above used:

```
mvcenc -base left.yuv -dep right.yuv -o out.264 -mkv out.mkv -mvcc \
    [-bslices N] [-bframes N] [-bqp QP] [-qp QP] [-no-b] [-interview-only] [-slices N]
mvcenc -dep right.yuv -o out.264 -mkv out.mkv -mvcc base.264   # a given left eye
```

`-bslices` and `-bframes` shape the left eye; `-no-b`, `-interview-only`
and `-slices` the right eye. `-mkv` with `-mvcc` writes what bdtools
writes: an MKV with the dependent view in a `mvcC` BlockAddition, the file
to put in front of the player.

## MV-HEVC on the Meta Quest (2026-10-16)

`--layout mvhevc` files were tried on a Quest with 4XVR Video Player, the
one Quest player that advertises MV-HEVC. The headset's own player shows
the base layer alone for any local MV-HEVC file, a known-good iPhone-style
sample included: its 3D modes are side by side and top/bottom, and Meta's
documented route for MV-HEVC is an upload of iPhone recordings through
the phone app, converted on Meta's servers.

4XVR decodes MV-HEVC with a decoder of its own ("soft-decoding", since
its version 1.2.10 of January 2024, before ffmpeg could decode MV-HEVC)
written for iPhone 15 Pro files. It plays the iPhone-style sample. On our
files it refuses 10-bit ("performance"), and on 8-bit it shows the second
layer, which it puts on the left eye, as blocks: a diagnostic file with a
different pattern in each layer showed both eyes with the base layer's
picture, the left one smeared, so the second layer comes out as its
inter-view prediction with the residual mangled. The same with no
B-frames, with every picture a reference, with BT.709 stated, in a
QuickTime container; x265's multiview output (muxed by the same writer)
does not play in it at all. ffmpeg 8 decodes every one of these files
cleanly in both views, and they follow Apple's HEVC Stereo Video profile
point by point. 4XVR is not a reference for MV-HEVC.

SKYBOX plays them. Its MV-HEVC support is in the 2.0.2 beta (build 3854,
October 2026; the store build 1.2.1 shows the base layer only), reached
through Quest Settings > Privacy & safety > Installed apps > SKYBOX >
Release Channels > "2.0.2 Preview". It played the iPhone-style sample,
the NVENC file (Q6, with the fixes below) and an x265 multiview encode
of the same minute, all correctly in 3D. One quirk: it decodes the two
views into a side-by-side picture and the 3D mode has to be set to side
by side by hand, as the eyes show next to each other until it is.

Two defects of the writer were found and fixed on the way: the camera
baseline and comfort boxes belong inside the stereo view box (`eyes`),
and the edit list must start at the first picture shown, after the
B-frame lead. The stereo NVENC path now codes every B-frame as a
reference, as Apple's own files do.
