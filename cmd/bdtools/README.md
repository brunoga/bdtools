# bdtools

Converts a frame-packed Blu-ray 3D source (MVC) into a side-by-side MKV that an
ordinary decoder can play, or remuxes it with the tracks you want and nothing
else.

MVC stores the second eye as a *dependent view* of an AVC base view. Very few
players decode it — **Plex does not**, and neither does libavcodec, which drops
the dependent view outright — so a 3D Blu-ray sits in a library unwatchable
despite carrying a full 1080p image per eye. Side-by-side puts both eyes into a
single frame any H.264/HEVC decoder handles, at the cost of a re-encode.

## Status

Complete, and tested end to end against a real MVC source — see
[Testing without a disc](#testing-without-a-disc).

## The pipeline

```
demuxer    read the disc in place; the video goes to the decoder, audio,    ─┐
           subtitles and chapters to the work directory — built in          │
decoder    decode both eyes and stack them side by side — built in          ├ one pass
encoder    a GPU in process (NVENC, VAAPI, VideoToolbox, Media Foundation), │
           drawn straight into its buffers; or x264 / x265 / SVT-AV1 fed    │
           Y4M through a pipe; in segments, so a stopped run can resume    ─┘
muxer      write the MKV: video, audio, subtitles (flat, or drawn in 3D),
           chapters — built in
```

With a GPU nothing outside the process runs at all. ffmpeg is used only for
software half-SBS (its scaler), for software AV1 when SvtAv1EncApp is not
installed (its libsvtav1), and as a fallback for a GPU whose library is
missing — see [Hardware encoding](#hardware-encoding).

The disc is read **once and in place**: a `.iso` straight out of the image, a
folder straight from its files. Nothing is extracted and neither view is
demuxed to disk — the access units go from the disc to this repository's MVC
decoder, and the stacked frames straight into the encoder. The only files
written are the audio and subtitle tracks the mux needs, and the encoded
video. For a feature film that is the difference between a scratch directory
the size of the disc (twice over: the extracted stream, then the demuxed
views) and one the size of its soundtrack.

A Matroska remux of a 3D disc (MakeMKV's, or mkvmerge's from a disc) is
read the same way: its video track carries each MVC access unit whole, both
views, and goes to the decoder as it is read; the audio and subtitle tracks
keep their names and forced flags. MP4 and VOB sources are not read: MVC
does not travel in them (a VOB is a DVD's, which is never 3D).

tsMuxeR and mkvmerge were used for the demux and the mux before these were
built in, and are not used any more. The muxer writes what mkvmerge writes
from the same streams: on a whole film (The Wild Robot, re-muxed from an
mkvmerge-made MKV) every one of the 146,237 video packets is identical —
timestamp, size, keyframe flag and content — and so are the subtitles; the
TrueHD packets are identical but for sub-millisecond timestamp rounding. It
reads the encoder's raw output (an H.264 or HEVC stream has no timestamps, so
each frame's display time comes from its picture order count, B-frames and
open GOPs included) and the audio and subtitle files frame by frame, and a
whole film takes about a minute and a half.

## Usage

```sh
bdtools --check                       # preflight: what is installed, what is not
bdtools --dry-run --input 00800.m2ts --output "Life of Pi (2012).mkv"
bdtools --input 00800.m2ts --output "Life of Pi (2012).mkv"
```

### What you can point it at

| You have | Point at | Who picks the title |
|---|---|---|
| A `.iso` disc image | the `.iso` | **it does** |
| A ripped BDMV folder | the folder, or its `BDMV` | **it does** |
| An AVCHD 3D recording | the folder holding `PRIVATE/AVCHD` | **it does** |
| A specific playlist | `BDMV/PLAYLIST/00800.mpls` | you |
| Loose streams | the feature's `.m2ts`, or its `STREAM/SSIF/*.ssif` | you |
| An MKV remux (MakeMKV, mkvmerge) | the `.mkv` | you |

**A disc image needs no mounting and no extraction.** Mounting one requires
root, which rules it out for an unattended conversion, so the image is read
directly — a Blu-ray is a UDF 2.50 filesystem and a pure-Go reader handles it on
every platform — and the feature's stream is decoded straight out of it.

### Choosing the title

Given an image or a folder, the playlists themselves are read — instantly, and
without touching a stream — and the 3D one holding **the most distinct content**
wins. A disc holds a playlist per title: the feature, its trailers, the menus,
and often several near-duplicates of the feature. Some also carry playlists
that loop one short clip a hundred times (menus, demo loops, decoys meant to
confuse rippers) and so run *longer* than the film; counting each stretch of a
clip once sees through those. Between copies of the feature the one with
chapters wins, then the one in fewer pieces.

```
bdtools: reading the disc image in place (no mount, no extraction)
bdtools: chose 00800.mpls (1h28m3s) from 46 playlists, 24 of them 3D
```

**The eye order comes from the disc too.** The playlist says whether the base
view is the left or the right eye, and the decoder stacks the views the other
way round when it is the right — most discs are left, some are not, and the
difference is the difference between 3D and a headache. `--swap-lr` overrides
it when given explicitly.

It reports as it goes, because a feature film takes hours:

```
bdtools: probing STREAM/SSIF/00272.ssif
bdtools: source: base view track 4113, dependent view track 4114, 1 audio, 1 subtitle
bdtools: audio: TRUE-HD 8ch (eng) 9612kbps lossless
bdtools: decoding and encoding (h265, nvenc on the GPU, in process)
bdtools: frame rate 24000/1001
bdtools: about 116664 frames to encode (1h21m6s at 23.976 fps)
bdtools: 7939 of 116664 frames encoded (6.8%), 264.6 fps, 6m51s left
bdtools: encoded 116642 frames (260.3 fps)
bdtools: muxing /media/3dmovies/Toy Story (1995).mkv
bdtools: muxed 10m0s
bdtools: timeline: picture 0.898-4865.800 s, A_TRUEHD 0.000-4865.841 s
bdtools: done: /media/3dmovies/Toy Story (1995).mkv
```

A non-zero exit means the conversion did not happen, which is what lets a
scheduler such as pipeliner retry it.

| Flag | Default | Description |
|---|---|---|
| `--check` | — | Report which external tools are present and which are missing, then exit |
| `--dry-run` | — | Print the commands that would run, without running them |
| `--version` | — | Print the version and exit |
| `--keep-temp` | — | Leave the work directory's files behind instead of deleting them |
| `--restart` | — | Encode from the start, ignoring the video an interrupted run left — see [Resuming](#resuming-an-interrupted-conversion) |
| `--quiet` | — | Only report errors |
| `--input` | — | A `.iso`, a BDMV folder, a `.mpls` playlist, an `.m2ts`, or an MKV remux — see [What you can point it at](#what-you-can-point-it-at) |
| `--output` | — | Destination `.mkv` |
| `--playlist` | chosen from the playlists | The title to read from a disc image or folder, by playlist number (`00800` or `00800.mpls`) |
| `--temp` | beside the output | Where the work directory goes: the audio and subtitle tracks and the encoded video, until they are muxed |
| `--layout` | `full` | `full` (1080p per eye) or `half` (960p per eye, roughly half the size) |
| `--encoder` | `auto` | `auto`, `software`, `vaapi`, `videotoolbox`, `nvenc`, `mediafoundation` (Windows; `mf` for short; `x264` is still accepted for `software`) |
| `--codec` | `h264` | `h264`, `h265` or `av1` — see [Codec](#codec) |
| `--swap-lr` | — | Exchange the eyes, for a disc whose base view is the right one |
| `--list` | — | Print the source's tracks and exit — see [Choosing tracks](#choosing-tracks) |
| `--audio-lang` | — | Keep only audio in these languages, e.g. `eng` or `eng,fra` |
| `--audio-codec` | — | Keep only audio matching these codecs, e.g. `truehd` or `dts,ac3` |
| `--audio-best` | — | Of the audio that matches, keep only the highest-quality track |
| `--subs-lang` | — | Keep only subtitles in these languages, e.g. `eng,pt-br` |
| `--subs-codec` | — | Keep only subtitles matching these codecs |
| `--subs-3d` | `off` | `off`, `on` or `both` — see [3D subtitles](#3d-subtitles) |
| `--keep-fallback` | — | Keep the lossy core embedded in a lossless track instead of dropping it |
| `--name-audio-codec` | — | Append the kept audio codec to the output filename |
| `--name-details` | — | Append the layout, resolution, codec, quality, encoder and main audio track to the output filename — see [Naming the output](#naming-the-output-after-the-audio) |
| `--remux` | — | Copy the disc's MVC video out with no re-encoding — see [Remuxing](#remuxing-instead-of-converting) |
| `--crf` | `18` | Quality target, 0–51; lower is better. **Not comparable between codecs** |
| `--bit-depth` | `8` | `8`, or `10` for `h265` and `av1` — see [10-bit](#10-bit) |
| `--preset` | `slow` | Software encoder speed/efficiency trade-off (x264 and x265 take the same names) |
| `--decode-threads` | all CPUs | Pictures the decoder works on at once |
| `--gpu-api` | `builtin` | `builtin` drives a GPU encoder through its system library, in process (ffmpeg when the library is missing); `ffmpeg` always goes through ffmpeg — see [Hardware encoding](#hardware-encoding) |
| `--vaapi-device` | `/dev/dri/renderD128` | The render node VAAPI encodes on |

`--layout full` is almost always what a 3D library wants: it is the only layout
that keeps the disc's resolution, and it is what the decoder emits natively, so
it costs no resample.

`--swap-lr` costs nothing: the decoder stacks the eyes the other way round as
it decodes. `--layout half` is a filter on the stacked frame, and neither x264
nor x265 can filter. Asking for it with `--encoder software` therefore runs the
encode through **ffmpeg's `libx264` or `libx265`** instead of the standalone
binary: the same encoder library, reached by a route that can filter, with
`--crf` and `--preset` passed straight through. Unfiltered software encoding
still uses the standalone binary, which is fewer moving parts and works on a
machine that has x264 but no ffmpeg.

`--check` follows the same rule, so it looks for ffmpeg when the filter is
asked for rather than reporting x265 missing on a machine that never needs it. Having
ffmpeg is not the same as having libx265, which a build may omit, so that case
gets a one-frame trial encode too — the alternative is finding out at the
encode step, hours into a conversion.

## Codec

`--codec h264` (the default) plays on anything, including hardware too old to
decode HEVC at all. `--codec h265` is materially smaller at the same quality: a
full-SBS frame is double width — 3840x1080 from a 1080p disc — which is exactly
the case HEVC's larger coding units were designed for.

The trade-off is decoder support. HEVC is widely but not universally
direct-played, and a client that has to *transcode* a 3840x1080 stream is worse
off than one direct-playing H.264. If the library is served to a mix of clients,
H.264 is the safer default; if you know what plays it, HEVC saves real space.

`--crf` means something different to each codec: x265 at a given CRF is roughly a
step *higher* quality — and larger — than x264 at the same number. Nothing here
adjusts it for you, because silently re-interpreting a number you typed is worse
than saying what it means. If you want HEVC's saving rather than its extra
quality, raise the CRF by two or three.

The codec is independent of the encoder: every encoder produces H.264 and
HEVC; AV1 is below.

### AV1

`--codec av1` is smaller again, but the newest to decode in hardware (Intel
Arc and 11th-gen Core onwards, NVIDIA RTX 30, AMD RX 6000, recent Android TV
devices and Apple M3/A17 onwards), so check what plays it first. It is encoded
by:

| Encoder | AV1 | Notes |
|---|---|---|
| NVENC | RTX 40 and later | in process, or `av1_nvenc` through ffmpeg; B-frames as for HEVC |
| VAAPI | Intel Arc / Core Ultra, AMD RX 7000 | in process, or `av1_vaapi` through ffmpeg; I and P frames only, so it is larger than NVENC's at the same `--crf` |
| software | everywhere | `SvtAv1EncApp` when installed, else ffmpeg's `libsvtav1`; slow at this frame size |
| VideoToolbox | no | Apple has no AV1 encoder: refused up front |

`--crf` keeps its 0–51 scale and means about the same picture quality as the
same number in HEVC: it is mapped onto AV1's 0–255 quantiser index by a line
measured on NVENC over a Blu-ray 3D, the AV1 index giving the same luma PSNR as
HEVC at QP 14, 18, 24 and 30 (index ≈ 7.1 × crf − 76). At equal PSNR the AV1
encodes were 11–15% smaller than HEVC's. SVT-AV1's CRF (0–63) is a quarter of
that index, and `--preset` maps onto SVT's numbered presets:

| `--preset` | ultrafast | superfast | veryfast | faster | fast | medium | slow | slower | veryslow | placebo |
|---|---|---|---|---|---|---|---|---|---|---|
| SVT-AV1 | 12 | 11 | 10 | 9 | 8 | 6 | 5 | 4 | 3 | 2 |

### 10-bit

`--bit-depth 10` encodes HEVC Main 10 or 10-bit AV1. A Blu-ray is 8-bit, so
the picture going in is the same, each sample at four times its value; what
changes is the encoder, which predicts and quantises at finer precision. That
shows as less banding in smooth gradients (skies, dark scenes, fades), and
in fewer bits: on NVENC HEVC over 300 frames of a Blu-ray 3D, QP 20 at 10
bits was 1.4% smaller than at 8 bits and 0.1 dB better (luma PSNR against
the source, rounded back to 8 bits), about 4% smaller at equal quality. It
costs no speed. Every HEVC and AV1 decoder plays it, since 10-bit is what
HDR is.

NVENC, VAAPI and VideoToolbox encode it in process (and through ffmpeg),
x265 with `--output-depth 10` (a build with 10-bit support, which the
common packages are), SVT-AV1 from 10-bit Y4M. H.264 is refused: High 10
plays on almost nothing. So is Media Foundation, whose 10-bit path is
untested; `--encoder nvenc` or `software` instead. `--name-details` adds
`10bit` after the codec.

## Choosing tracks

By default every audio and subtitle track the disc carries is passed through
untouched, each tagged with the language the disc gives it, so a player
can tell them apart.

That default is often not what you want, because **lossless audio dominates the
output**. A well-compressed conversion of a clean CG feature can come out with
2.5 GB of video and 10 GB of audio: the single TrueHD Atmos track alone was
more than twice the video on one measured disc. Dropping the tracks you will
never play is the largest saving available that costs no picture quality.

Start by seeing what is there:

```sh
bdtools --list --input "Toy Story 1995 3D.iso" --temp /scratch
```

```
track kind               lang  codec                info
  4113  video (base view)  und   H.264                Profile: High@4.1  Resolution: 1920:1080p  Frame rate: 23.976
  4114  video (dependent)  und   MVC                  H.264/MVC Views: 2 Profile: Stereo High@4.1  Resolution: 1920:1080p  Frame rate: 23.976
  4352  audio              eng   TRUE-HD              AC3 core + TRUE-HD + ATMOS. Peak bitrate: 9612Kbps (core 640Kbps) Sample Rate: 48KHz Channels: 7.1
  4353  audio              eng   DTS-HD Master Audio  Sample Rate: 48KHz Channels: 5.1
  4354  audio              eng   AC3                  Bitrate: 192Kbps Sample Rate: 48KHz Channels: 2.0
  4355  audio              fra   AC3                  Bitrate: 640Kbps Sample Rate: 48KHz Channels: 5.1
  4356  audio              spa   AC3                  Bitrate: 640Kbps Sample Rate: 48KHz Channels: 5.1
  4608  subtitle           eng   PGS                  Presentation Graphic Stream #0
  4609  subtitle           fra   PGS                  Presentation Graphic Stream #1
  4610  subtitle           spa   PGS                  Presentation Graphic Stream #2
```

Then narrow it. Language and codec are **both** required when both are given,
so the pair names one track rather than the union of two sets:

```sh
bdtools --input disc.iso --output out.mkv \
        --audio-lang eng --audio-codec truehd --subs-lang eng
```

Notes on the matching:

- A codec is matched as a **substring**, case-insensitively, against both the
  stream ID and the human type. `truehd` finds `A_TRUEHD`, and `dts` finds both
  `A_DTS` and `DTS-HD Master Audio`, so you need not know which spelling the
  disc used.
- A language is an ISO-639 code as the disc states it. **`und` matches a track
  the disc gave no language for**, which is how a commentary track with no tag
  is selected — and it means an English filter will not sweep untagged tracks
  in.
- A filter that matches **nothing is an error**, reported before any work is
  done, listing what the disc actually has. Carrying every track on would
  defeat the request, and dropping all audio would produce a film nobody can
  watch, discovered hours later.
- Filtering happens **before the demux**, so a narrowed selection means fewer
  tracks written and less scratch space, not merely a smaller output.

`--list` reads the playlists and the first few megabytes of the feature's
stream, wherever they are, so it is instant even on an image over a network
share.

### The best track, rather than a named one

Naming a codec means knowing what the disc has. `--audio-best` instead keeps the
single highest-quality track of those matching, and it composes with the
language filter — this is "the best English track", not "the best track, if it
happens to be English":

```sh
bdtools --input disc.iso --output out.mkv \
        --audio-lang eng --audio-best --subs-lang eng,pt-br
```

The ranking is, in order:

1. **Lossless beats lossy.** TrueHD, DTS-HD Master Audio, LPCM and FLAC rank
   above DTS-HD High Resolution and E-AC-3, which rank above AC-3, DTS and AAC.
   No bitrate of a lossy codec puts back what it discarded.
2. **Then channels.** A 7.1 track is what a 7.1 system is for, and a
   higher-bitrate 5.1 mix cannot supply the two channels it does not have.
3. **Then bitrate**, where the disc states one. Lossless tracks often do not,
   which is why it is only ever a tie-breaker.
4. **Then the disc's own order**, so the choice is deterministic and one disc
   always gives one answer.

The chosen track is logged, because a decision made on your behalf should be
visible rather than inferred from the finished file hours later:

```
bdtools: audio: TRUE-HD 8ch (eng) 9612kbps lossless
```

`--audio-best` does not apply to subtitles. Several are routinely wanted at
once, and ranking PGS streams against each other would mean nothing.

### Languages, and what a disc actually calls them

Several languages at once is just a list: `--subs-lang eng,pt-br`.

A Blu-ray has no way to say *Brazilian* Portuguese in ISO-639-2 — there is only
`por` — so authoring tools variously emit `por`, or the non-standard `pob` or
`ptb`. Asking for `pt-br` matches whichever the disc chose, so you need not
know which. The same goes for the pairs where ISO-639-2 has both a
bibliographic and a terminological code and sources disagree about which to
use: `fra`/`fre`, `deu`/`ger` and `zho`/`chi` each match either spelling. A code
with no alias entry matches itself, so nothing is lost by not being listed.

### 3D subtitles

A Blu-ray 3D draws its subtitles once, and the player moves them apart in
the two eyes so that they float in front of the picture, at a depth the
disc sets frame by frame (the offset metadata in the MVC stream, one of up
to 32 sequences, which the playlist assigns to each subtitle track). A
side-by-side file has no such player.

- `--subs-3d off` (the default) keeps the subtitles as the disc has them,
  drawn once. That is right for a player that places subtitles in 3D itself
  (Kodi in its 3D mode does), and wrong for one that shows the frame as it
  is: a TV or headset in side-by-side mode stretches each half to the whole
  screen, and a subtitle drawn across the middle of the frame ends up in
  neither eye whole.
- `--subs-3d on` draws each subtitle into both halves of the frame, moved
  apart by the disc's offset (right in the left eye, left in the right one,
  for the usual depth in front of the screen), as a 3D player would. With
  `--layout half` it is squeezed to half width like the picture, keeping
  thin strokes. The track is named "3D".
- `--subs-3d both` keeps the flat track and adds the 3D one after it. The
  first flat track is marked default, since a player that places subtitles
  in 3D itself (Kodi playing frame packed) draws a 3D track squeezed into
  one eye; pick the 3D one on a player that shows the frame as it is.

A subtitle takes the depth the disc gives at its first frame; a disc that
moves it while it is up is followed from its next display set. A subtitle
track the disc assigns no offset sequence sits at the screen plane. A
Matroska source keeps the offset metadata but not which sequence a track
follows, so its subtitles take the sequence nearest the viewer at each
moment: never behind the picture, at most a little further forward than
the disc meant. (On Avatar: Fire and Ash the subtitles follow other
sequences than the first, which would have put them inside the scene.)

### Naming the output after the audio

`--name-audio-codec` inserts the kept codec before the extension:

```
Toy Story (1995) 3D FSBS.mkv  ->  Toy Story (1995) 3D FSBS.TrueHD-Atmos.mkv
```

`--name-details` says everything that tells one conversion from another:
the layout, the resolution per eye, the codec (with `10bit` at 10 bits) and
its quality setting (`QP` for a GPU, `CRF` for x264, x265 and SVT-AV1, `Q`
for VideoToolbox's quality), the encoder, and the main audio track with its
channels:

```
Moana (2016).mkv  ->  Moana (2016) 3D FSBS 1080p HEVC QP20 NVENC TrueHD-Atmos 7.1.mkv
```

A name that already ends in `3D FSBS` does not get it twice. "3D FSBS" is
also what Kodi and Jellyfin look for in a name to treat a file as
side-by-side 3D.

Useful with `--audio-best`, where the codec is whatever the disc turned out to
offer. The rename happens after the conversion, because the codec is not known
until the source has been probed, and probing a disc image twice to decide a
filename would cost as much as the conversion's first stage. The final path is
printed to stdout either way, so a script driving this need not guess at it.

## Resuming an interrupted conversion

A conversion that stops partway — Ctrl-C, a reboot, a network share that
went away — continues where it left off when the same command is run
again. The video is encoded in segments of 2,500 frames (ten keyframe
intervals, under two minutes of film), each a complete stream from its own
encoder run, into a work directory beside the output named after it
(`.Film (2016).mkv.bdtools`, or under `--temp`). A manifest there lists
the segments that finished.

Run again, bdtools decodes from the start, since a picture cannot be
decoded without the ones before it, but encodes only from the first frame
no segment holds; the decoder alone runs at several times an encoder's
speed, so catching up an hour of film takes a minute or two. The audio and
subtitles are demuxed again on the way, from the same read. The result is
the file an uninterrupted run makes, frame for frame.

What is kept is only ever whole segments: one cut short is discarded, so an
interruption costs at most one segment of encoding (seconds on a GPU,
minutes in software). The segments belong to the source and the settings
that made them. A run with another source, codec, encoder, quality, layout,
eye order, bit depth or title starts over, and says so; `--restart` starts
over regardless. A run that succeeds removes the work directory; one that
fails keeps only the segments, and says how many frames they hold.

## Remuxing instead of converting

`--remux` keeps the disc's own MVC video, bit for bit, and drops only the
tracks the filters leave out:

```sh
bdtools --remux --input disc.iso --output "Film (2012) 3D.m2ts" \
         --audio-lang eng --audio-best --subs-lang eng
```

The transport packets are copied untouched, arrival timestamps and all; only
the program tables are rewritten to list what is kept (base view, dependent
view, then the rest in the disc's order, each with its language). A pressed
disc keeps the two views in separate clips interleaved in the SSIF, so their
packets are merged back into arrival order — both clips run on one clock —
giving one transport stream with both views, which is what a player that
decodes MVC expects. Nothing is decoded or re-timed, so the result plays
exactly as the disc does, and it needs no tools at all.

A title made of several clips — a playlist of several play items, as
seamless-branching and some long films are — is joined into one stream:

- Each clip is cut to its play item's window. A remux cannot re-encode, so a
  clip starts at the picture that opens the GOP at or before its IN time
  (the pictures before IN are a fraction of a second at most), and keeps
  pictures while they decode before OUT, so none loses a picture it is
  predicted from. Audio and subtitles are kept by their presentation time,
  IN to OUT.
- Every later clip is moved onto the first one's timeline: its PTS, DTS,
  clock references and arrival timestamps, so the result has one clock and
  plays and seeks like a single clip, rather than marking a discontinuity
  that many players handle badly. Where a clip's opening GOP would overlap
  the end of the clip before it, it moves just far enough not to.
- Continuity counters are renumbered across the cuts, and the clips must
  carry the kept tracks on the same PIDs (discs do); if not, the remux says
  so rather than guessing.

The output must be `.m2ts` (or `.ts`, without the arrival timestamps): MVC
has no home in Matroska that players agree on. Nothing about the picture can
change, so `--layout half` and `--swap-lr` are refused.

## What plays the result, and at what resolution

The output declares its layout in the Matroska `StereoMode` element
(`side_by_side_left_first`), so a player need not infer 3D from the filename or
be told by hand. That flag is what decides whether the resolution this spent
hours preserving survives to the screen.

| player | what it does | per eye |
|---|---|---|
| **Kodi / CoreELEC** | reads the flag, splits the frame, emits HDMI **frame-packed** 3D | **1920x1080** |
| Plex | recognises only *half*-SBS and *half*-TAB, from the filename; treats this as flat 2D | 960x1080 at best |

Full per-eye resolution needs **frame packing**, the HDMI 3D format that carries
two complete 1920x1080 frames in one 1920x2205 transport. The side-by-side HDMI
format cannot: it squeezes both eyes into a single 1920x1080 frame, so each eye
is 960 columns stretched back to 1920, whatever the source file held.

So a full-SBS file is a 1:1 pixel map through a frame-packing player — each
1920x1080 view lands on the panel untouched — and a waste through anything that
only speaks half-SBS, which scales it down before the display ever sees it.
That is also the ceiling worth chasing: MVC on a 3D Blu-ray is 1080p per eye,
and consumer 3D displays present Full HD per eye, so 1920x1080 is both what the
disc holds and what the screen can show.

CoreELEC 21.1 and later handle `(F)SBS`, `(F)TAB` and MVC frame packing on
devices using the hdmitx20 driver. Half-SBS (`--layout half`) exists for players
that will only take that, and costs half the horizontal detail by definition.

## Tools, and why each is needed

At most one: the encoder. The demux, the decode and the mux are built in
(libavcodec drops the MVC dependent view outright, so ffmpeg could not stand
in for the first two), and a GPU encodes through its driver's own library, in
process.

| Tool | What it does | Why nothing else will do |
|---|---|---|
| **x264** / **x265** / **SvtAv1EncApp** *or* **ffmpeg** | Re-encodes the stacked frames when no GPU does | Side-by-side is a new frame layout, so a re-encode is unavoidable. x264, x265 or SvtAv1EncApp follows `--codec`; ffmpeg instead for software half-SBS (its scaler), for AV1 without SvtAv1EncApp (its libsvtav1), or for a GPU whose library is missing or with `--gpu-api ffmpeg` |

A `--remux` needs nothing at all.

Installing them is left to you; `--check` says what is missing, what each one
does, and where to start:

```
platform: linux   encoder: software   codec: h264

  ok       x264       /usr/bin/x264

all 1 required tools present
```

With a GPU that encodes in process it says so: `no external tools needed`.

`--check` exits non-zero when anything is missing, so it works as a preflight.
It answers for the options given, so `--check --layout half` looks for
ffmpeg rather than x264.

### What the built-in demuxer handles

It does what tsMuxeR, which it replaced, did with a Blu-ray where that matters
— the audio and subtitle files it writes were checked **byte-identical to
tsMuxeR's** on the discs tried (TrueHD with its AC-3 core, E-AC-3 7.1 with its
AC-3 core, DTS, DTS-HD High Resolution and Master Audio, AC-3, PGS) — and
handles the same disc layouts:

- **The SSIF** a pressed 3D disc interleaves its views in, or the two `.m2ts`
  files of a folder rip without one, or one `.m2ts` holding both views (a
  remux, an AVCHD recording).
- **Playlists of several clips**, joined, with each clip's IN and OUT times.
- **Multi-angle titles**: the first angle.
- **Languages** from the playlist, or from the clip info when a loose stream
  file is given; AVCHD's 8.3 names (`.MPL`, `.CPI`, `.MTS`).
- **LPCM** is written as WAV (byte order and 7.1 channel order converted,
  20-bit samples padded to 24), as tsMuxeR did.
- **Chapters** from the playlist marks, which tsMuxeR's demux left out.

Where it deliberately differs, it is to play as the disc does: audio and
subtitles **before the playlist's IN time** (or the first picture of a loose
stream) and after its OUT time are not written, nor are pictures outside
them. A disc whose sound starts before its picture — The Wild Robot's starts
1.16 s early — otherwise comes out with the sound that much ahead, because a
demuxed elementary stream has no timestamps left to say so.

## What it does with the rest of the disc

Audio and subtitle tracks are demuxed alongside the two views and muxed into the
output in the order the source listed them, so the first audio track stays
first, and the first audio track is the default one. One output track per disc
track (and, with `--subs-3d both`, a 3D subtitle track after each flat one):
a Blu-ray TrueHD stream carries an embedded AC-3 core for players that
cannot decode TrueHD, and DTS-HD carries a plain DTS core the same way, and the
demux writes each pair as a single file. The muxer takes the main stream and leaves the core out — it is the same audio,
lossily, and an extra track the probe never reported would be a surprise —
unless `--keep-fallback` asks for it as its own track. (A 7.1 E-AC-3 track's
AC-3 part is not a separate core: it is half of every E-AC-3 frame, and
stays.) A Matroska remux keeps a TrueHD track's AC-3 as a track of its own,
which is an ordinary audio track here, chosen or not by the filters.

Each track is tagged with the language the disc gives it; a track the disc gave
no language for is tagged `und`, undetermined, rather than guessed at — and
rather than left untagged, which Matroska reads as English. Use
[the track filters](#choosing-tracks) to carry fewer of them. A track the demux
failed to produce is reported and skipped — that costs a language, not the
film.

The views are identified by **stream ID**, not by order or track number: a disc
is not obliged to list them in any order, and taking the wrong one as the base
gives a stream that cannot decode at all.

A source that is not 3D is refused by name rather than failing obscurely:

```
bdtools: no MVC track: this source is not 3D (found V_MPEG4/ISO/AVC (track 4113))
```

So are two MVC tracks, an MVC track with no AVC base view, and an elementary
stream handed in where a container was expected.

## Testing without a disc

`testdata/bluray` is a synthetic Blu-ray 3D, as a folder and as a UDF image,
muxed (once, by tsMuxeR) from the MVC fixtures below and generated audio. The
demuxer is tested on it with no tools installed: the listing, the decoded
pictures (identical to the combined stream's), the audio cut to the playlist,
and the remux. A Matroska remux is built from the same fixtures on the fly;
`TestRunnerConvertsADiscImage` and `TestRunnerConvertsAMatroskaSource`
convert both and check the output is 1280×480 side by side with its tracks.

The MVC streams are
[mvc-source](https://github.com/jens-duttke/mvc-source)'s `tests/fixtures`,
committed under `testdata/mvc-source`: `mvc_base.264`, `mvc_dependent.mvc` and
`mvc_combined.264` at 35, 23 and 57 KB — the exact shape a demux produces. The
decode stage is tested on them directly; the end-to-end tests also need x264
on `PATH` and skip without it, so CI stays green without the toolchain:

```sh
go test ./internal/convert/
```

## Encoders per platform

| Platform | Hardware | Software |
|---|---|---|
| Linux | NVENC, VAAPI | x264 / x265 / SVT-AV1 |
| macOS | VideoToolbox (not AV1) | x264 / x265 / SVT-AV1 |
| Windows | NVENC, Media Foundation (Intel, AMD; H.264 and HEVC) | x264 / x265 / SVT-AV1 |

`auto` runs a **trial encode** for each candidate and takes the first that
succeeds, falling back to software: in process first, then through ffmpeg.
Merely finding ffmpeg or a driver library is not evidence a GPU is present — a stock build advertises `h264_nvenc` on a machine with no
NVIDIA card — and discovering that at the encode step would waste the hours
already spent decoding.

The trial is run for the codec being produced, not for the encoder in the
abstract: a GPU generation can carry an H.264 encoder and no HEVC one, so
`--codec h265` can fall back to x265 on the same machine where `--codec h264`
picks NVENC.

## Hardware encoding

A GPU encoder is driven **in process, through its system library**, loaded
at run time — no cgo, no ffmpeg, nothing to link:

| Encoder | Library | Platforms |
|---|---|---|
| NVENC | the NVIDIA driver's `libnvidia-encode` and `libcuda` (`nvEncodeAPI64.dll`, `nvcuda.dll`) | Linux, Windows |
| VAAPI | `libva` and `libva-drm`, with the GPU's driver (Intel's `iHD`, Mesa's `radeonsi`) | Linux |
| VideoToolbox | the system frameworks | macOS |
| Media Foundation | the encoder MFT the GPU driver installs (Intel Quick Sync, AMD AMF), through `mfplat.dll` | Windows |

The decoder's frames are drawn side by side straight into the encoder's
input buffer, squeezed for half-SBS on the way, so nothing is piped and no
filter runs. Each encoder makes an IDR every 250 frames (about 10 s, as x264
and x265 do; 7% smaller than every 2 s at the same quality) with B-frames
between references, at a constant quantiser: `--crf` is the P-picture QP,
and I and B pictures get the offsets ffmpeg applies by default for that
encoder, so a number means what it meant through ffmpeg's `-qp`.
VideoToolbox has no QP; `--crf` maps onto its quality scale, `0` the best
and `51` the worst.

When the library is missing or its trial encode fails, the encoder is reached
through ffmpeg as before; `--gpu-api ffmpeg` asks for that outright.
On Windows, `auto` tries NVENC, then the Media Foundation encoder a driver installs, which is how Intel and AMD GPUs encode there. Microsoft's own software encoder MFT is never picked. `--encoder mediafoundation --gpu-api ffmpeg` uses ffmpeg's `h264_mf` / `hevc_mf` instead. Media Foundation takes a constant QP where the driver supports one, which Intel's and AMD's do, and otherwise its 0-100 quality, mapped from `--crf` as for VideoToolbox. This path is tested in CI with Microsoft's software encoder, not yet on an Intel or AMD GPU.

Decoding and encoding a minute of a Blu-ray to H.264 on a Core Ultra 9 285K
(wall time for the whole conversion, then the decode-and-encode rate):

| Encoder | Full SBS | Half SBS |
|---|---|---|
| NVENC (RTX 5090), in process | 7.0 s, 292 fps | 5.0 s, 435 fps |
| NVENC through ffmpeg | 7.9 s | 7.5 s |
| VAAPI (Arrow Lake iGPU), in process | 7.8 s, 239 fps | 6.8 s, 270 fps |
| VAAPI through ffmpeg | 7.6 s | 7.4 s |

The iGPU's encoder tops out near 270 fps at this size, so VAAPI is as fast
either way; NVENC is not the limit, and in process it keeps up with the
decoder.

A VAAPI driver that only decodes (NVIDIA's `nvidia-vaapi-driver`) has no
encode entrypoint; the probe sees that and moves on. Intel's driver for
recent GPUs is `intel-media-va-driver-non-free` on Debian and Ubuntu.

## Docker

`Dockerfile.bdtools` carries the whole toolchain, for amd64 and arm64, on
Debian. A release publishes it:

```sh
docker run --rm -v /media:/media ghcr.io/brunoga/bdtools:latest --check
```

Or build it yourself:

```sh
docker build -f Dockerfile.bdtools -t bdtools .
docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile.bdtools -t bdtools .
```

The demuxer, decoder and muxer are part of the `bdtools` binary, with the
decoder's assembly kernels chosen at run time by what the CPU supports; the
image adds x264, x265, ffmpeg (with NVENC, VAAPI and libsvtav1, which
software AV1 uses), libva, and the VAAPI drivers for Intel (non-free, amd64)
and AMD.

It is Debian, not Alpine, because a GPU's own libraries are built for glibc:
NVIDIA's `libnvidia-encode` and `libcuda`, which the NVIDIA container toolkit
mounts in, cannot be loaded by a musl system, and Alpine's ffmpeg has no
NVENC either.

### GPUs in the container

- **NVIDIA:** install the NVIDIA container toolkit on the host and run with
  `--gpus all`. The image sets `NVIDIA_DRIVER_CAPABILITIES=compute,video,utility`;
  `video` is the capability that brings `libnvidia-encode` in, and without it
  there is no NVENC.
- **Intel / AMD (VAAPI):** pass the render node, `--device /dev/dri/renderD128`
  (and `--vaapi-device` if it is another one). The drivers are in the image.

`--check` in the container tries a GPU reached through ffmpeg with a short
encode, so an image or host that cannot drive the GPU says so before a
conversion starts rather than at its encode.

### Running the conversion out of the image

bdtools knows nothing about Docker: it runs its tools from its own PATH, so
inside the image it just works. That means you can skip installing the toolchain
on the host and let a scheduler drive the image instead — pipeliner's `exec`
sink takes an `args` list, so `docker` becomes the command and nothing needs
quoting:

```python
output("exec", upstream=once, command="docker",
       args=["run", "--rm",
             "--volume", "/media:/media",
             "--gpus", "all",                    # or --device /dev/dri for VAAPI
             "ghcr.io/brunoga/bdtools:latest",
             "--input", "{file_location}",
             "--output", "{sbs_path}", "--quiet"])
```

Both forms behave identically to the pipeline: a non-zero exit fails the entry,
so the conversion is retried rather than recorded as done. A retry resumes:
the work directory sits beside the output, on the mounted volume, and
`docker stop` sends the SIGTERM that stops a conversion cleanly. Hardware encoding
needs the device passed through, which is the one thing the container cannot
arrange for itself.

## Driving it from pipeliner

Pair it with [pipeliner](https://github.com/brunoga/pipeliner)'s `exec` sink,
whose `args` list passes each value as one argument, so paths with spaces need
no quoting:

```python
src  = input("filesystem", path="/media/3d-staging", recursive=True, mask="*.iso")
meta = process("metainfo_file", upstream=src)
conv = output("exec", upstream=meta,
              command="/usr/local/bin/bdtools",
              args=["--input", "{file_location}",
                    "--output", "/media/3dmovies/{title} ({video_year}).mkv"])
pipeline("convert-3d", schedule="0 4 * * *")
```

A non-zero exit fails the entry, so a failed conversion is not recorded as done
and is retried next run.
