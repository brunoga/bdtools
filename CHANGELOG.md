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
