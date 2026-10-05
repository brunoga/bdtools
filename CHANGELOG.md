# Changelog

All notable changes to this project are documented here. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

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

### Fixed

- Audio and subtitles before the playlist's IN time (or a loose stream's
  first picture) and after its OUT time are no longer muxed. tsMuxeR's demux
  keeps them, which put the sound ahead of the picture on discs whose audio
  starts early (1.16 s on The Wild Robot).
