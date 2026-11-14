# bdtools from the command line

Common operations, each a complete command. Only the paths are yours.
The [command's README](../cmd/bdtools/README.md) explains every flag and
the choices behind the defaults; `bdtools --help` lists the flags.

## Before anything

**What is installed.** The answer depends on the options, so pass the
ones you will use:

```sh
bdtools --check
bdtools --check --encoder software --codec h265
```

**What a disc holds.** Instant, even over a network share: the playlists
are read, the feature chosen, and the tracks listed with their ids,
languages and codecs:

```sh
bdtools --list --input "Avatar (2009) 3D.iso"
bdtools --list --json --input "Avatar (2009) 3D.iso"      # for a script
```

**The manual**, these pages and the grading manual with their pictures,
served from the binary at an address, or written out as files:

```sh
bdtools --manual :8080        # then http://localhost:8080/
bdtools --manual ~/bdtools-manual
```

**What would run**, without running it:

```sh
bdtools --dry-run --input disc.iso --output film.mkv
```

A disc image needs no mounting: point at the `.iso`, a BDMV folder, a
playlist, an `.m2ts`, or an MKV, MP4 or MOV. A disc's feature is chosen
for you (`--playlist 00801` names another title).

## 3D discs

**A 3D Blu-ray to a side-by-side MKV**, full resolution (1080p an eye),
HEVC at 10 bits on the GPU when there is one, every track kept:

```sh
bdtools --input "Avatar (2009) 3D.iso" --output "Avatar (2009) 3D FSBS.mkv"
```

**With only the tracks you will play.** Lossless audio often outweighs
the video, so this is the largest saving there is:

```sh
bdtools --input "Avatar (2009) 3D.iso" --output "Avatar (2009) 3D FSBS.mkv" \
        --audio-lang eng --audio-best --subs-lang eng
```

**Half side by side** for a player that takes only that (Plex, most
phones), the file named after what it holds:

```sh
bdtools --input disc.iso --output "Frozen (2013).mkv" --layout half --name-details
# -> Frozen (2013) 3D HSBS 1080p HEVC 10bit CQ26 NVENC TrueHD 7.1.mkv
```

**Over-under** (`ou` full, `half-ou` half), for a projector or television
that prefers it:

```sh
bdtools --input disc.iso --output "Film (2012) 3D OU.mkv" --layout ou
```

**MVC kept as MVC**, an MKV CoreELEC's Kodi plays frame packed at full
resolution; both eyes are re-encoded in H.264 by the encoder here:

```sh
bdtools --input disc.iso --output "Film (2012) 3D.mkv" --layout mvc --audio-lang eng --audio-best
```

**For a headset or an Apple device**, MV-HEVC in an MP4 or MOV. These
carry no subtitle track, so one is drawn in by its id from `--list`:

```sh
bdtools --input disc.iso --output "Film (2012).mp4" --for quest --burn-subs 4608
bdtools --input disc.iso --output "Film (2012).mov" --for visionpro
```

**A Blu-ray 3D disc image**, to burn or to play from an ISO:

```sh
bdtools --input disc.iso --output "Film (2012) 3D.iso" --audio-lang eng --audio-best
```

**The 3D disc as a 2D film**, its base view alone:

```sh
bdtools --input "Avatar (2009) 3D.iso" --output "Avatar (2009).mkv" --2d
```

**The eyes the wrong way round** (rare; the disc's own marking is
followed): `--swap-lr`.

## 2D and Ultra HD discs

**An Ultra HD Blu-ray to HEVC**, HDR10, HDR10+ and Dolby Vision kept,
10 bits, the quality chosen for the source:

```sh
bdtools --input "Dune (2021) UHD.iso" --output "Dune (2021).mkv"
```

**Dolby Vision.** The default composes the full enhancement layer into
the picture, profile 8.1, which every Dolby Vision player plays. A player
that composes the layers itself (CoreELEC) can keep profile 7; or leave
Dolby Vision out:

```sh
bdtools --input uhd.iso --output film.mkv                          # profile 8.1
bdtools --input uhd.iso --output film.mkv --dv-fel keep --encoder nvenc   # profile 7
bdtools --input uhd.iso --output film.mkv --dv-fel drop            # HDR10 only
```

**A 1080p copy of a UHD film**, its black bars found and cut off, and
made SDR for a screen that shows HDR wrongly:

```sh
bdtools --input uhd.iso --output film.mkv --crop auto --scale 1080p
bdtools --input uhd.iso --output film.mkv --crop auto --scale 1080p --tonemap
```

**An HD Blu-ray** (H.264, VC-1 or MPEG-2 on the disc) converts the same
way; its default quality is a little higher since it is small already:

```sh
bdtools --input "Film (2007).iso" --output "Film (2007).mkv" --audio-lang eng --audio-best
```

**H.264 for an old player**, or **AV1** for a new one:

```sh
bdtools --input disc.iso --output film.mkv --codec h264
bdtools --input disc.iso --output film.mkv --codec av1 --crf 22
```

## 2D to 3D

**A 2D film made 3D**, side by side, the depth the 3D discs' own:

```sh
bdtools --input "Film (2020).mkv" --output "Film (2020) 3D FSBS.mkv" --to-3d
bdtools --input "Film (2020) UHD.iso" --output "Film (2020) 3D FSBS.mkv" --to-3d --scale 1080p
```

Any 3D output takes it: `--layout half`, `ou`, `mvc`, `mvhevc` (or
`--for quest`), and a Blu-ray 3D image (`--output film.iso`). An HDR
source is tone mapped on the way; the depth network runs on an NVIDIA
GPU at about 35 pictures a second, on the CPU at about one a second.

**Stills first**, to choose a depth before hours of conversion: eight
places through the film, each at a few depths, as side-by-side and
red-cyan JPEGs:

```sh
bdtools --input "Film (2020).mkv" --output previews/ --to-3d --3d-preview
```

**More or less depth, nearer or farther**, for the whole film:

```sh
bdtools --input film.mkv --output film-3d.mkv --to-3d --3d-depth 1.3 --3d-screen 0.2
```

**For a large screen**, the background kept short of the eyes'
separation (the screen's diagonal in inches):

```sh
bdtools --input film.mkv --output film-3d.mkv --to-3d --3d-screen-size 120
```

**Shots graded by hand**, from a file written by eye with `--3d-grade`
(the [grading manual](grading.md)) or by hand:

```sh
bdtools --input film.mkv --output film.3d --to-3d --3d-grade :8420        # grade, then
bdtools --input film.mkv --output film-3d.mkv --to-3d --3d-shots film.3d  # convert
```

**The other networks and the options**: a larger depth network
(`--depth-model base` or `large`, non-commercial licences), the learnt
hole filler (`--3d-fill weights.bin`), no refinement (`--3d-refine none`),
no floating window (`--3d-window=false`).

## Tracks

**See them**, then narrow. A language and a codec together name one
track; a filter that matches nothing stops before any work:

```sh
bdtools --list --input disc.iso
bdtools --input disc.iso --output film.mkv --audio-lang eng --audio-codec truehd --subs-lang eng,pt-br
```

**The best audio track of a language**, rather than a named codec
(lossless first, then channels, then bitrate):

```sh
bdtools --input disc.iso --output film.mkv --audio-lang eng --audio-best
```

**A foreign film**: the original sound and the English subtitles as the
defaults, the dub kept beside them:

```sh
bdtools --input disc.iso --output film.mkv --audio-lang jpn,eng --audio-best \
        --default-audio jpn --subs-lang eng --default-subs eng
```

**No commentary**, and **the lossy core kept** for a player that cannot
decode the lossless track:

```sh
bdtools --input disc.iso --output film.mkv --no-commentary
bdtools --input disc.iso --output film.mkv --audio-best --keep-fallback
```

**Subtitles.** A 3D conversion carries each subtitle track twice by
default, flat and drawn into both eyes at the disc's depth (`--subs-3d
both`); `on` or `off` keeps one kind. PGS subtitles can be read as text,
and a subtitle file added as a track:

```sh
bdtools --input disc.iso --output film.mkv --subs-lang eng --subs-3d on
bdtools --input disc.iso --output film.mkv --subs-ocr                  # an SRT track beside each PGS one
bdtools --input disc.iso --output film.mkv --subs-file eng:film.srt --subs-file film.eng.forced.srt
```

## Size and quality

**The quality** is `--crf`, lower better, chosen by the source when not
given: 20 for 3D, 18 for Ultra HD, 16 for HD. Grainy films are what cost
bits; give those a lower number when their worst pictures matter:

```sh
bdtools --input disc.iso --output film.mkv --crf 14
```

**A target size** for the video (the sound comes on top): the CRF is
found by test encodes first:

```sh
bdtools --input disc.iso --output film.mkv --video-size 8G
```

**Measure a conversion** against its source, PSNR, SSIM and VMAF, with no
other tool:

```sh
bdtools --input disc.iso --compare film.mkv
bdtools --input disc.iso --compare film.mkv --compare-every 10 --compare-csv film.csv
```

**No GPU**, or a slower machine: software encoding at a faster preset:

```sh
bdtools --input disc.iso --output film.mkv --encoder software --preset medium
```

## Parts, batches and players

**A scene**, to try settings on or to keep:

```sh
bdtools --input disc.iso --output scene.mkv --start 1:02:30 --duration 2m
bdtools --input disc.iso --output scene.mkv --start 1:02:30 --end 1:04:30
```

**A folder of sources**, converted with one set of settings into another
folder; run again, it converts only what is new, and `--watch` keeps
looking for arrivals:

```sh
bdtools --input /media/inbox --output /media/films --for plex
bdtools --input /media/inbox --output /media/films --for plex --watch
```

**A player's settings** in one word, each only where the command line
gives none: `coreelec`, `kodi`, `plex`, `appletv`, `visionpro`, `quest`:

```sh
bdtools --for plex --input disc.iso --output film.mkv
bdtools --for appletv --crf 20 --input disc.iso --output film.mp4
```

**Naming for a 3D library.** Kodi and CoreELEC read the layout from the
MKV itself; Plex and some players read it from the name, so the project's
outputs carry it (`Film (2020) 3D FSBS.mkv`, `3D HSBS`, `3D OU`).
`--name-details` appends the rest.

## Remuxing, no re-encoding

**A 3D disc's MVC to one `.m2ts`**, for a player that decodes MVC, the
tracks chosen:

```sh
bdtools --remux --input "Avatar (2009) 3D.iso" --output "Avatar (2009) 3D.m2ts" --audio-lang eng --audio-best
```

**A 2D or Ultra HD disc to an MKV**, the video untouched, Dolby Vision
profile 7 kept whole:

```sh
bdtools --remux --input "Dune (2021) UHD.iso" --output "Dune (2021).mkv" --audio-lang eng --subs-lang eng
```

**Tracks dropped from an existing MKV**:

```sh
bdtools --remux --input "Film (2012).mkv" --output "Film (2012) eng.mkv" --audio-lang eng --audio-best --subs-lang eng
```

## Interruptions, scratch space, containers

**Resume**: run the same command again; it continues from the last
finished segment (whole segments only, so an interruption costs at most
one). `--restart` starts over:

```sh
bdtools --input disc.iso --output film.mkv            # stopped at 40%...
bdtools --input disc.iso --output film.mkv            # ...continues from there
bdtools --input disc.iso --output film.mkv --restart  # from the beginning
```

A run with another source, codec, encoder, quality, layout or title
starts over and says so.

**Scratch space elsewhere** (a fast local disk when the output is on a
share), kept afterwards for a look:

```sh
bdtools --input /nas/disc.iso --output /nas/film.mkv --temp /scratch --keep-temp
```

**In Docker**, with the whole toolchain (NVIDIA through the container
toolkit; Intel and AMD through the render node):

```sh
docker run --rm --gpus all -v /media:/media ghcr.io/brunoga/bdtools:latest \
  --input "/media/discs/Film.iso" --output "/media/films/Film.mkv"
docker run --rm --device /dev/dri -v /media:/media ghcr.io/brunoga/bdtools:latest \
  --input "/media/discs/Film.iso" --output "/media/films/Film.mkv"
```

**A whole shelf**, one disc after another, a failure noted and the rest
going on (a failed one resumes when run again):

```sh
for iso in /media/3d-staging/*.iso; do
  name=$(basename "$iso" .iso)
  bdtools --quiet --input "$iso" --output "/media/3dmovies/$name.mkv" --audio-best \
    || echo "failed: $iso"
done
```

## When something is off

| Symptom | Likely cause, what to do |
|---|---|
| `--check` reports an encoder missing | Only the ones the options need matter: a GPU encoder needs its driver's library, software needs x264, x265 or SVT-AV1 in the path, or `--encoder builtin` for the encoders here. |
| A filter matched nothing | The run stops before any work and lists the disc's tracks; `--list` shows the ids, languages and codecs to match. |
| The wrong title | `--list` says which playlist was chosen of how many; `--playlist NNNNN` names another. |
| 3D plays flat, or half width | The player does not read the Matroska stereo flag: name the file with its layout, or use `--layout half` for players that take only that. |
| The eyes feel reversed | `--swap-lr`. |
| The conversion restarted from the beginning | A setting changed since the interrupted run; the log names it. |
| The output is huge | Lossless audio: `--audio-lang` and `--audio-best`. Grain: expected; a lower `--crf` costs more still. |
