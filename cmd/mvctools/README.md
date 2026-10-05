# mvctools

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
demuxer    read the disc in place; the video goes to the decoder, audio,   ─┐
           subtitles and chapters to the work directory — built in         │
decoder    decode both eyes and stack them side by side, as Y4M — built in ─┤ one pass,
encoder    x264 or x265, or ffmpeg with a platform hardware encoder        ─┘ piped
muxer      write the MKV: video, audio, subtitles, chapters — built in
```

The disc is read **once and in place**: a `.iso` straight out of the image, a
folder straight from its files. Nothing is extracted and neither view is
demuxed to disk — the access units go from the disc to this repository's MVC
decoder, and the stacked frames straight into the encoder. The only files
written are the audio and subtitle tracks the mux needs, and the encoded
video. For a feature film that is the difference between a scratch directory
the size of the disc (twice over: the extracted stream, then the demuxed
views) and one the size of its soundtrack.

`--demuxer tsmuxer` uses [tsMuxeR](#tsmuxer) instead, as before: the image's
streams are extracted, tsMuxeR demuxes them, and the decoder reads the
demuxed pair. A Matroska, MP4 or VOB source always goes that way, since the
built-in demuxer reads Blu-ray sources. `--muxer mkvmerge` writes the MKV
with mkvmerge instead of the built-in muxer. Any combination works.

The built-in muxer writes what mkvmerge writes from the same streams: on a
whole film (The Wild Robot, re-muxed from an mkvmerge-made MKV) every one of
the 146,237 video packets is identical — timestamp, size, keyframe flag and
content — and so are the subtitles; the TrueHD packets are identical but for
sub-millisecond timestamp rounding. It reads the encoder's raw output (an
H.264 or HEVC stream has no timestamps, so each frame's display time comes
from its picture order count, B-frames and open GOPs included) and the
audio and subtitle files frame by frame, and a whole film takes about a
minute and a half.

## Usage

```sh
mvctools --check                       # preflight: what is installed, what is not
mvctools --dry-run --input 00800.m2ts --output "Life of Pi (2012).mkv"
mvctools --input 00800.m2ts --output "Life of Pi (2012).mkv"
```

### What you can point it at

| You have | Point at | Who picks the title |
|---|---|---|
| A `.iso` disc image | the `.iso` | **it does** |
| A ripped BDMV folder | the folder, or its `BDMV` | **it does** |
| An AVCHD 3D recording | the folder holding `PRIVATE/AVCHD` | **it does** |
| A specific playlist | `BDMV/PLAYLIST/00800.mpls` | you |
| Loose streams | the feature's `.m2ts`, or its `STREAM/SSIF/*.ssif` | you |
| An MKV from MakeMKV | the `.mkv` (read with tsMuxeR) | you |

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
mvctools: reading the disc image in place (no mount, no extraction)
mvctools: chose 00800.mpls (1h28m3s) from 46 playlists, 24 of them 3D
```

**The eye order comes from the disc too.** The playlist says whether the base
view is the left or the right eye, and the decoder stacks the views the other
way round when it is the right — most discs are left, some are not, and the
difference is the difference between 3D and a headache. `--swap-lr` overrides
it when given explicitly.

It reports as it goes, because a feature film takes hours:

```
mvctools: probing STREAM/SSIF/00272.ssif
mvctools: source: base view track 4113, dependent view track 4114, 2 audio, 4 subtitle
mvctools: decoding and encoding (h264, nvenc)
mvctools: frame rate 24000/1001
mvctools: 1800 frames decoded (61.2 fps)
mvctools: decoded 170271 frames
mvctools: muxing /media/3dmovies/Life of Pi (2012).mkv
mvctools: done: /media/3dmovies/Life of Pi (2012).mkv
```

A non-zero exit means the conversion did not happen, which is what lets a
scheduler such as pipeliner retry it.

| Flag | Default | Description |
|---|---|---|
| `--check` | — | Report which external tools are present and which are missing, then exit |
| `--dry-run` | — | Print the commands that would run, without running them |
| `--keep-temp` | — | Leave the demuxed streams behind instead of deleting them |
| `--quiet` | — | Only report errors |
| `--input` | — | An `.m2ts`, a `.mpls` playlist from a BDMV, or an MKV — see [What you can point it at](#what-you-can-point-it-at) |
| `--output` | — | Destination `.mkv` |
| `--temp` | beside the output | Scratch space for the demuxed views |
| `--layout` | `full` | `full` (1080p per eye) or `half` (960p per eye, roughly half the size) |
| `--encoder` | `auto` | `auto`, `software`, `vaapi`, `videotoolbox`, `nvenc` (`x264` is still accepted for `software`) |
| `--codec` | `h264` | `h264` or `h265` — see [Codec](#codec) |
| `--swap-lr` | — | Exchange the eyes, for a disc whose base view is the right one |
| `--list` | — | Print the source's tracks and exit — see [Choosing tracks](#choosing-tracks) |
| `--audio-lang` | — | Keep only audio in these languages, e.g. `eng` or `eng,fra` |
| `--audio-codec` | — | Keep only audio matching these codecs, e.g. `truehd` or `dts,ac3` |
| `--audio-best` | — | Of the audio that matches, keep only the highest-quality track |
| `--subs-lang` | — | Keep only subtitles in these languages, e.g. `eng,pt-br` |
| `--subs-codec` | — | Keep only subtitles matching these codecs |
| `--keep-fallback` | — | Keep the lossy core embedded in a lossless track instead of dropping it |
| `--name-audio-codec` | — | Append the kept audio codec to the output filename |
| `--remux` | — | Copy the disc's MVC video out with no re-encoding — see [Remuxing](#remuxing-instead-of-converting) |
| `--crf` | `18` | Quality target, 0–51; lower is better. **Not comparable between codecs** |
| `--preset` | `slow` | Software encoder speed/efficiency trade-off (x264 and x265 take the same names) |
| `--decode-threads` | all CPUs | Pictures the decoder works on at once |
| `--demuxer` | `builtin` | `builtin` reads the disc in place; `tsmuxer` uses tsMuxeR — see [The pipeline](#the-pipeline) |
| `--muxer` | `builtin` | `builtin` writes the MKV in process; `mkvmerge` uses mkvmerge |

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

The codec is independent of the encoder: every encoder below produces either.

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
mvctools --list --input "Toy Story 1995 3D.iso" --temp /scratch
```

```
track kind               lang  codec                info
4113  video (base view)  und   H.264                Profile: High@4.1 Resolution: 1920:1080p
4114  video (dependent)  und   MVC                  H.264/MVC Views: 2
4352  audio              eng   TrueHD Atmos         Bitrate: 0Kbps Channels: 8
4353  audio              eng   AC3                  Bitrate: 640Kbps Channels: 6
4354  audio              fra   DTS-HD Master Audio  Channels: 6
4356  audio              und   AC3                  Bitrate: 192Kbps Channels: 2
4608  subtitle           eng   PGS
```

Then narrow it. Language and codec are **both** required when both are given,
so the pair names one track rather than the union of two sets:

```sh
mvctools --input disc.iso --output out.mkv \
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
share. (With `--demuxer tsmuxer` it has to extract the image first, which
costs what a conversion's first stage costs: pass `--temp` somewhere with
room.)

### The best track, rather than a named one

Naming a codec means knowing what the disc has. `--audio-best` instead keeps the
single highest-quality track of those matching, and it composes with the
language filter — this is "the best English track", not "the best track, if it
happens to be English":

```sh
mvctools --input disc.iso --output out.mkv \
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
mvctools: audio: TrueHD Atmos 8ch (eng) lossless
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

### Naming the output after the audio

`--name-audio-codec` inserts the kept codec before the extension:

```
Toy Story (1995) 3D FSBS.mkv  ->  Toy Story (1995) 3D FSBS.TrueHD-Atmos.mkv
```

Useful with `--audio-best`, where the codec is whatever the disc turned out to
offer. The rename happens after the conversion, because the codec is not known
until the source has been probed, and probing a disc image twice to decide a
filename would cost as much as the conversion's first stage. The final path is
printed to stdout either way, so a script driving this need not guess at it.

## Remuxing instead of converting

`--remux` keeps the disc's own MVC video, bit for bit, and drops only the
tracks the filters leave out:

```sh
mvctools --remux --input disc.iso --output "Film (2012) 3D.m2ts" \
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

The output must be `.m2ts` (or `.ts`, without the arrival timestamps): MVC
has no home in Matroska that players agree on. Nothing about the picture can
change, so `--layout half` and `--swap-lr` are refused. A title made of
several clips joined together is remuxed with `--demuxer tsmuxer`.

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

One: the encoder. The demux, the decode and the mux are built in
(libavcodec drops the MVC dependent view outright, so ffmpeg could not stand
in for the first two).

| Tool | What it does | Why nothing else will do |
|---|---|---|
| **x264** / **x265** *or* **ffmpeg** | Re-encodes the stacked frames | Side-by-side is a new frame layout, so a re-encode is unavoidable. x264 or x265 follows `--codec`; ffmpeg instead, for GPU encoding or the half-SBS filter |

A `--remux` needs nothing at all. **mkvmerge** is needed only for
`--muxer mkvmerge`.

Installing them is left to you; `--check` says what is missing, what each one
does, and where to start:

```
platform: linux   encoder: software   codec: h264

  ok       x264       /usr/bin/x264

all 1 required tools present
```

`--check` exits non-zero when anything is missing, so it works as a preflight.
It answers for the options given, so `--check --demuxer tsmuxer` looks for
tsMuxeR too, and `--check --muxer mkvmerge` for mkvmerge.

### tsMuxeR

[tsMuxeR](https://github.com/justdan96/tsMuxer) is only needed for
`--demuxer tsmuxer`, and for Matroska, MP4 or VOB sources. Upstream's release
binaries demux MVC; the Linux one is x86_64 only, but its **CLI needs no Qt** —
that is the GUI alone — so on Arm Linux it is

```sh
apt install build-essential cmake ninja-build zlib1g-dev libfreetype-dev
cmake -S . -B build -G Ninja && ninja -C build tsmuxer
```

about a minute. `Dockerfile.mvctools` does exactly that, so the image has it.

### What the built-in demuxer handles

It does what tsMuxeR does with a Blu-ray where that matters — the audio and
subtitle files it writes are **byte-identical to tsMuxeR's** on the discs it
was checked against (TrueHD with its AC-3 core, E-AC-3 7.1 with its AC-3 core,
DTS, DTS-HD High Resolution and Master Audio, AC-3, PGS) — and handles the
same disc layouts:

- **The SSIF** a pressed 3D disc interleaves its views in, or the two `.m2ts`
  files of a folder rip without one, or one `.m2ts` holding both views (a
  remux, an AVCHD recording).
- **Playlists of several clips**, joined, with each clip's IN and OUT times.
- **Multi-angle titles**: the first angle.
- **Languages** from the playlist, or from the clip info when a loose stream
  file is given; AVCHD's 8.3 names (`.MPL`, `.CPI`, `.MTS`).
- **LPCM** is written as WAV (byte order and 7.1 channel order converted,
  20-bit samples padded to 24), as tsMuxeR does.
- **Chapters** from the playlist marks, which tsMuxeR's demux leaves out.

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
track: a Blu-ray TrueHD stream carries an embedded AC-3 core for players that
cannot decode TrueHD, and DTS-HD carries a plain DTS core the same way, and the
demux writes each pair as a single file (tsMuxeR names it `.ac3+thd`). The
muxer takes the main stream and leaves the core out — it is the same audio,
lossily, and an extra track the probe never reported would be a surprise —
unless `--keep-fallback` asks for it as its own track. (A 7.1 E-AC-3 track's
AC-3 part is not a separate core: it is half of every E-AC-3 frame, and
stays.) With `--muxer mkvmerge` the same choice is made by identifying each
file with mkvmerge and matching on the codec, since the tools spell codecs
differently (`TRUE-HD` against `TrueHD Atmos`).

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
mvctools: no MVC track: this source is not 3D (found V_MPEG4/ISO/AVC (track 4113))
```

So are two MVC tracks, an MVC track with no AVC base view, and an elementary
stream handed in where a container was expected.

## Testing without a disc

`testdata/bluray` is a synthetic Blu-ray 3D, as a folder and as a UDF image,
muxed by tsMuxeR from the MVC fixtures below and generated audio. The built-in
demuxer is tested on it with no tools installed: the listing, the decoded
pictures (identical to the combined stream's), the audio cut to the playlist,
and the remux. tsMuxeR also builds a real 3D m2ts and image on the fly;
`TestRunnerConvertsARealSource` and `TestRunnerConvertsADiscImage` convert
those with both demuxers and check the output is 1280×480 with its audio
intact.

The MVC streams are
[mvc-source](https://github.com/jens-duttke/mvc-source)'s `tests/fixtures`,
committed under `testdata/mvc-source`: `mvc_base.264`, `mvc_dependent.mvc` and
`mvc_combined.264` at 35, 23 and 57 KB — the exact shape a demux produces. The
decode stage is tested on them directly; the end-to-end tests also need
tsMuxeR, x264, mkvmerge and ffmpeg on `PATH` and skip without them, so CI stays
green without the toolchain:

```sh
go test ./internal/convert/
```

## Encoders per platform

| Platform | Hardware | Software |
|---|---|---|
| Linux | NVENC, VAAPI | x264 / x265 |
| macOS | VideoToolbox | x264 / x265 |
| Windows | NVENC | x264 / x265 |

`auto` runs a **one-frame trial encode** for each candidate and takes the first
that succeeds, falling back to software. Merely finding ffmpeg is not evidence a
GPU is present — a stock build advertises `h264_nvenc` on a machine with no
NVIDIA card — and discovering that at the encode step would waste the hours
already spent decoding.

The trial is run for the codec being produced, not for the encoder in the
abstract: a GPU generation can carry an H.264 encoder and no HEVC one, so
`--codec h265` can fall back to x265 on the same machine where `--codec h264`
picks NVENC.

## Docker

`Dockerfile.mvctools` carries the whole toolchain, for amd64 and arm64, on
Alpine. A release publishes it:

```sh
docker run --rm -v /media:/media ghcr.io/brunoga/mvctools:latest --check
```

Or build it yourself:

```sh
docker build -f Dockerfile.mvctools -t mvctools .
docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile.mvctools -t mvctools .
```

tsMuxeR (for `--demuxer tsmuxer`) is built rather than downloaded so the arm64
image is a real arm64 image — upstream publishes a single Linux binary and it
is x86_64. The demuxer and decoder need no such care: they are part of the
`mvctools` binary, with the decoder's assembly kernels chosen at run time by
what the CPU supports.

### Running the conversion out of the image

mvctools knows nothing about Docker: it runs its tools from its own PATH, so
inside the image it just works. That means you can skip installing the toolchain
on the host and let a scheduler drive the image instead — pipeliner's `exec`
sink takes an `args` list, so `docker` becomes the command and nothing needs
quoting:

```python
output("exec", upstream=once, command="docker",
       args=["run", "--rm",
             "--volume", "/media:/media",
             "--gpus", "all",                    # or --device /dev/dri for VAAPI
             "ghcr.io/brunoga/mvctools:latest",
             "--input", "{file_location}",
             "--output", "{sbs_path}", "--quiet"])
```

Both forms behave identically to the pipeline: a non-zero exit fails the entry,
so the conversion is retried rather than recorded as done. Hardware encoding
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
              command="/usr/local/bin/mvctools",
              args=["--input", "{file_location}",
                    "--output", "/media/3dmovies/{title} ({video_year}).mkv"])
pipeline("convert-3d", schedule="0 4 * * *")
```

A non-zero exit fails the entry, so a failed conversion is not recorded as done
and is retried next run.
