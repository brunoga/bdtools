# bdtools from the command line

This is a cookbook: the things people most often do with bdtools, each
with a complete command you can copy and change the paths of. It does
not explain every flag. For that, and for the reasoning behind the
defaults, see the [README](../cmd/bdtools/README.md), or run `bdtools
--help`.

## Before anything

**See what is installed.** The answer depends on which options you plan
to use, so pass those:

```sh
bdtools --check
bdtools --check --encoder software --codec h265
```

**See what a disc holds.** This is instant, even over a network share:
bdtools reads the playlists, picks the feature, and lists the tracks with
their ids, languages and codecs.

```sh
bdtools --list --input "Avatar (2009) 3D.iso"
bdtools --list --json --input "Avatar (2009) 3D.iso"      # for a script
```

**Read the manual**, this page and the grading guide, with their
pictures. It is built into the binary. You can serve it at an address or
write it out as files:

```sh
bdtools --manual :8080        # then open http://localhost:8080/
bdtools --manual ~/bdtools-manual
```

**See what would run**, without running anything:

```sh
bdtools --dry-run --input disc.iso --output film.mkv
```

You never need to mount a disc image. Point `--input` at the `.iso`, at
a BDMV folder, at a playlist, at an `.m2ts`, or at an MKV, MP4 or MOV.
bdtools picks the feature for you; `--playlist 00801` names another
title.

## 3D discs

**A 3D Blu-ray to a side-by-side MKV.** This is the plain conversion:
full resolution (1080p per eye), HEVC at 10 bits, on the GPU if there is
one, with every track kept.

```sh
bdtools --input "Avatar (2009) 3D.iso" --output "Avatar (2009) 3D FSBS.mkv"
```

**Keep only the tracks you will play.** Lossless audio often weighs more
than the video, so this is the biggest saving there is:

```sh
bdtools --input "Avatar (2009) 3D.iso" --output "Avatar (2009) 3D FSBS.mkv" \
        --audio-lang eng --audio-best --subs-lang eng
```

**Half side by side**, for a player that only takes that (Plex, most
phones). `--name-details` names the file after what it holds:

```sh
bdtools --input disc.iso --output "Frozen (2013).mkv" --layout half --name-details
# -> Frozen (2013) 3D HSBS 1080p HEVC 10bit CQ26 NVENC TrueHD 7.1.mkv
```

**Over-under**, if your projector or television prefers it. `ou` is full
resolution, `half-ou` half:

```sh
bdtools --input disc.iso --output "Film (2012) 3D OU.mkv" --layout ou
```

**MVC kept as MVC.** This makes an MKV that CoreELEC's Kodi plays frame
packed at full resolution. Both eyes are re-encoded in H.264 by the
encoder built into bdtools:

```sh
bdtools --input disc.iso --output "Film (2012) 3D.mkv" --layout mvc --audio-lang eng --audio-best
```

**For a headset or an Apple device**, which want MV-HEVC in an MP4 or
MOV. These files carry no subtitle track, so if you want subtitles, draw
one in with its id from `--list`:

```sh
bdtools --input disc.iso --output "Film (2012).mp4" --for quest --burn-subs 4608
bdtools --input disc.iso --output "Film (2012).mov" --for visionpro
```

**A Blu-ray 3D disc image**, to burn or to play as an ISO:

```sh
bdtools --input disc.iso --output "Film (2012) 3D.iso" --audio-lang eng --audio-best
```

**The 3D disc as a 2D film**, using its base view alone:

```sh
bdtools --input "Avatar (2009) 3D.iso" --output "Avatar (2009).mkv" --2d
```

If the eyes come out the wrong way round, which is rare because the
disc's own marking is followed, add `--swap-lr`.

## 2D and Ultra HD discs

**An Ultra HD Blu-ray to HEVC.** HDR10, HDR10+ and Dolby Vision are
kept, the output is 10-bit, and the quality is chosen for the kind of
source:

```sh
bdtools --input "Dune (2021) UHD.iso" --output "Dune (2021).mkv"
```

**Dolby Vision.** By default the full enhancement layer is composed into
the picture, which gives profile 8.1, the one every Dolby Vision player
plays. A player that composes the layers itself (CoreELEC) can keep
profile 7. Or you can leave Dolby Vision out altogether:

```sh
bdtools --input uhd.iso --output film.mkv                          # profile 8.1
bdtools --input uhd.iso --output film.mkv --dv-fel keep --encoder nvenc   # profile 7
bdtools --input uhd.iso --output film.mkv --dv-fel drop            # HDR10 only
```

**A 1080p copy of a UHD film.** `--crop auto` finds the black bars and
cuts them off, `--scale 1080p` fits the rest in 1080p, and `--tonemap`
makes it SDR for a screen that shows HDR wrongly:

```sh
bdtools --input uhd.iso --output film.mkv --crop auto --scale 1080p
bdtools --input uhd.iso --output film.mkv --crop auto --scale 1080p --tonemap
```

**An HD Blu-ray** (H.264, VC-1 or MPEG-2 on the disc) converts the same
way. Its default quality is a little higher, since the file is small
anyway:

```sh
bdtools --input "Film (2007).iso" --output "Film (2007).mkv" --audio-lang eng --audio-best
```

**H.264 for an old player, or AV1 for a new one:**

```sh
bdtools --input disc.iso --output film.mkv --codec h264
bdtools --input disc.iso --output film.mkv --codec av1 --crf 22
```

## 2D to 3D

**Make a 2D film 3D.** The output is side by side and the depth is what
the 3D discs have:

```sh
bdtools --input "Film (2020).mkv" --output "Film (2020) 3D FSBS.mkv" --to-3d
bdtools --input "Film (2020) UHD.iso" --output "Film (2020) 3D FSBS.mkv" --to-3d --scale 1080p
```

Any 3D output works here: `--layout half`, `ou`, `mvc`, `mvhevc` (or
`--for quest`), and a Blu-ray 3D image with `--output film.iso`. An HDR
source is tone mapped on the way. The depth network runs on an NVIDIA
GPU at about 35 pictures a second, or on the CPU at about one a second.

**Look at stills first**, to choose a depth before you spend hours on a
conversion. This writes eight places from the film, each at a few
depths, as side-by-side and red-cyan JPEGs:

```sh
bdtools --input "Film (2020).mkv" --output previews/ --to-3d --3d-preview
```

**More or less depth, nearer or farther**, for the whole film:

```sh
bdtools --input film.mkv --output film-3d.mkv --to-3d --3d-depth 1.3 --3d-screen 0.2
```

**For a large screen.** Give the screen's diagonal in inches and the
background is kept short of your eyes' separation:

```sh
bdtools --input film.mkv --output film-3d.mkv --to-3d --3d-screen-size 120
```

**Grade the shots by eye.** First grade with `--3d-grade` (the
[grading guide](grading.md) explains it), then convert with the file it
wrote:

```sh
bdtools --input film.mkv --output film.3d --to-3d --3d-grade :8420        # grade, then
bdtools --input film.mkv --output film-3d.mkv --to-3d --3d-shots film.3d  # convert
```

There are a few more options: a larger depth network (`--depth-model
base` or `large`, both under a non-commercial licence), the learnt hole
filler (`--3d-fill weights.bin`), no depth refinement (`--3d-refine
none`), and no floating window (`--3d-window=false`).

## Tracks

**See them, then narrow them down.** A language and a codec together
name one track. A filter that matches nothing stops the run before any
work is done:

```sh
bdtools --list --input disc.iso
bdtools --input disc.iso --output film.mkv --audio-lang eng --audio-codec truehd --subs-lang eng,pt-br
```

**The best audio track in a language**, rather than a named codec.
Lossless beats lossy, then more channels, then a higher bitrate:

```sh
bdtools --input disc.iso --output film.mkv --audio-lang eng --audio-best
```

**A foreign film.** The original sound and the English subtitles become
the defaults, and the dub is kept beside them:

```sh
bdtools --input disc.iso --output film.mkv --audio-lang jpn,eng --audio-best \
        --default-audio jpn --subs-lang eng --default-subs eng
```

**No commentary**, or **the lossy core kept** for a player that cannot
decode the lossless track:

```sh
bdtools --input disc.iso --output film.mkv --no-commentary
bdtools --input disc.iso --output film.mkv --audio-best --keep-fallback
```

**Subtitles.** A 3D conversion carries each subtitle track twice by
default, once flat and once drawn into both eyes at the disc's depth
(`--subs-3d both`); `on` or `off` keeps just one kind. PGS subtitles can
be read as text, and a subtitle file can be added as a track:

```sh
bdtools --input disc.iso --output film.mkv --subs-lang eng --subs-3d on
bdtools --input disc.iso --output film.mkv --subs-ocr                  # an SRT track beside each PGS one
bdtools --input disc.iso --output film.mkv --subs-file eng:film.srt --subs-file film.eng.forced.srt
```

## Size and quality

**The quality** is `--crf`. Lower is better. If you don't give one, it is
chosen by the source: 20 for 3D, 18 for Ultra HD, 16 for HD. Grainy
films are what cost bits, so give those a lower number when their worst
pictures matter to you:

```sh
bdtools --input disc.iso --output film.mkv --crf 14
```

**A target size** for the video (the sound comes on top). bdtools finds
the CRF with a few test encodes first:

```sh
bdtools --input disc.iso --output film.mkv --video-size 8G
```

**Measure a conversion** against its source. PSNR, SSIM and VMAF, with
no other tool installed:

```sh
bdtools --input disc.iso --compare film.mkv
bdtools --input disc.iso --compare film.mkv --compare-every 10 --compare-csv film.csv
```

**No GPU**, or a slower machine? Software encoding at a faster preset:

```sh
bdtools --input disc.iso --output film.mkv --encoder software --preset medium
```

## Parts, batches and players

**A scene**, to try settings on or to keep:

```sh
bdtools --input disc.iso --output scene.mkv --start 1:02:30 --duration 2m
bdtools --input disc.iso --output scene.mkv --start 1:02:30 --end 1:04:30
```

**A folder of sources**, all converted with the same settings into
another folder. Run it again and only the new ones are converted;
`--watch` keeps looking for new arrivals:

```sh
bdtools --input /media/inbox --output /media/films --for plex
bdtools --input /media/inbox --output /media/films --for plex --watch
```

**A player's settings in one word.** `--for` sets what a player wants,
but only where you have not said otherwise. The choices are `coreelec`,
`kodi`, `plex`, `appletv`, `visionpro` and `quest`:

```sh
bdtools --for plex --input disc.iso --output film.mkv
bdtools --for appletv --crf 20 --input disc.iso --output film.mp4
```

**Naming for a 3D library.** Kodi and CoreELEC read the layout from the
MKV itself, but Plex and some other players read it from the name. That
is why the outputs here carry it: `Film (2020) 3D FSBS.mkv`, `3D HSBS`,
`3D OU`. `--name-details` appends the rest.

## Remuxing, with no re-encoding

**A 3D disc's MVC into one `.m2ts`**, for a player that decodes MVC,
with just the tracks you want:

```sh
bdtools --remux --input "Avatar (2009) 3D.iso" --output "Avatar (2009) 3D.m2ts" --audio-lang eng --audio-best
```

**A 2D or Ultra HD disc into an MKV.** The video is untouched, and Dolby
Vision profile 7 is kept whole:

```sh
bdtools --remux --input "Dune (2021) UHD.iso" --output "Dune (2021).mkv" --audio-lang eng --subs-lang eng
```

**Drop tracks from an existing MKV:**

```sh
bdtools --remux --input "Film (2012).mkv" --output "Film (2012) eng.mkv" --audio-lang eng --audio-best --subs-lang eng
```

## Interruptions, scratch space and containers

**Resume.** Just run the same command again. It picks up from the last
finished segment, and since only whole segments are kept, an
interruption costs you at most one. `--restart` starts over:

```sh
bdtools --input disc.iso --output film.mkv            # stopped at 40%...
bdtools --input disc.iso --output film.mkv            # ...continues from there
bdtools --input disc.iso --output film.mkv --restart  # from the beginning
```

If you change the source, codec, encoder, quality, layout or title, the
run starts over and tells you why.

**Scratch space somewhere else**, say a fast local disk when the output
lives on a share. `--keep-temp` leaves the work files for you to look
at:

```sh
bdtools --input /nas/disc.iso --output /nas/film.mkv --temp /scratch --keep-temp
```

**In Docker**, with the whole toolchain in the image. NVIDIA GPUs go
through the container toolkit, Intel and AMD through the render node:

```sh
docker run --rm --gpus all -v /media:/media ghcr.io/brunoga/bdtools:latest \
  --input "/media/discs/Film.iso" --output "/media/films/Film.mkv"
docker run --rm --device /dev/dri -v /media:/media ghcr.io/brunoga/bdtools:latest \
  --input "/media/discs/Film.iso" --output "/media/films/Film.mkv"
```

**A whole shelf**, one disc after another. A failure is noted and the
rest carry on, and a failed one resumes the next time you run this:

```sh
for iso in /media/3d-staging/*.iso; do
  name=$(basename "$iso" .iso)
  bdtools --quiet --input "$iso" --output "/media/3dmovies/$name.mkv" --audio-best \
    || echo "failed: $iso"
done
```

## When something is off

| What you see | What is probably going on |
|---|---|
| `--check` says an encoder is missing | Only the ones your options need matter. A GPU encoder needs its driver's library, software encoding needs x264, x265 or SVT-AV1 on the path, and `--encoder builtin` uses the encoders inside bdtools. |
| A filter matched nothing | The run stops before doing any work and lists the disc's tracks. `--list` shows the ids, languages and codecs you can match. |
| The wrong title was chosen | `--list` says which playlist was picked and how many there are. `--playlist NNNNN` names another. |
| 3D plays flat, or at half width | The player does not read the Matroska stereo flag. Put the layout in the file name, or use `--layout half` for players that only take that. |
| The eyes feel reversed | Add `--swap-lr`. |
| The conversion started from the beginning | A setting changed since the interrupted run. The log names it. |
| The output is huge | Usually lossless audio: use `--audio-lang` and `--audio-best`. Grain is the other reason, and a lower `--crf` only costs more. |
