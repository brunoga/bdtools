# mvc — pure Go H.264 / MVC stereo decoder, and mvctools

A high-performance H.264 (AVC) decoder written in pure Go with support for
**Multiview Video Coding** (MVC, ITU-T H.264 Annex H) as used on 3D Blu-ray.
For every access unit it outputs **two full-resolution frames** — the base
view and the dependent view — ready to be consumed by other code.

It ships two commands: `mvcdec`, which decodes a stream to raw YUV or Y4M,
and [`mvctools`](cmd/mvctools/README.md), which turns a Blu-ray 3D disc,
image or playlist into a side-by-side MKV (or remuxes it), reading the disc in
place with its own demuxer and decoder, handing the frames to x264/x265 or
ffmpeg, and writing the MKV with its own muxer.

- Bit-exact: verified against the ITU-T/JVT conformance suite (2D and MVC)
  and against a complete 3D Blu-ray feature (all 110,162 access units, both
  views byte-identical to the edge264 reference decoder).
- Fast: frame-parallel multithreading plus AVX2 kernels in Go assembly
  (assembled by the Go toolchain, no cgo), with pure-Go fallbacks.
- Realtime friendly: access units in, display-ordered stereo pairs out, with
  presentation timestamps carried through; zero-copy frame buffers.
- Includes a Blu-ray M2TS demuxer (`m2ts`), an Annex B access-unit splitter,
  a Y4M writer (side by side, top-and-bottom, either view, with the frame
  rate taken from the stream) and a stream decoding loop for all three
  input forms.

## Build

```sh
go build ./cmd/mvcdec ./cmd/mvctools
```

On amd64 the hot paths are hand-written Go assembly (`*_amd64.s`, assembled
by the Go toolchain, no cgo): the CABAC residual, motion vector difference
and coded block pattern decoders, motion compensation (6-tap luma, bilinear
chroma, averaging and weighted prediction), the inverse transforms, the
deblocking filters and the per-macroblock bookkeeping. They are used when
the CPU has AVX2, BMI2 and LZCNT (checked with CPUID at startup); other
CPUs and architectures use the equivalent Go code, which can also be forced
with `-tags purego`. Every assembly routine has a randomized test against
its Go counterpart (`TestCabac*Asm`, `TestSIMD*`).

Releases are built with `CGO_ENABLED=0` for Linux, macOS
and Windows on amd64, arm64 and arm/v7 (see `.goreleaser.yml`).

## Library usage

```go
dec := mvc.NewDecoder(mvc.Options{})       // Threads: 0 = all CPUs
dm := m2ts.NewDemuxer(file)                 // Blu-ray .m2ts / .ssif
for {
	au, err := dm.Next()
	if err != nil { break }
	dec.DecodeAU(au.Base, au.PTS)           // base view (AVC)
	dec.DecodeAU(au.Dep, au.PTS)            // dependent view (MVC)
	dm.Recycle(&au)
	for {
		sf, ok := dec.NextFrame()       // display order
		if !ok { break }
		left, right := sf.Base, sf.Dependent // *mvc.Frame (right nil for 2D)
		_ = left.Y; _ = left.Cb; _ = left.Cr // cropped 4:2:0 planes + strides
		_ = left.Image()                // zero-copy *image.YCbCr
		sf.Release()                    // give the buffers back
	}
}
dec.Flush()                                 // then drain NextFrame again
```

Or let the decoder drive the loop for a whole stream — a transport stream,
an interleaved Annex B stream, or the demuxed pair tsMuxeR writes — and write
it out as Y4M:

```go
dec := mvc.NewDecoder(mvc.Options{})
y4m := mvc.NewY4MWriter(w, mvc.LayoutSideBySide)
src := mvc.Source{Format: mvc.FormatSplit, R: baseFile, Dependent: depFile}
stats, err := dec.DecodeStream(src, mvc.DecodeOptions{}, func(sf *mvc.StereoFrame) error {
	if stats.Frames == 0 {              // the SPS has been seen by now
		y4m.FPSNum, y4m.FPSDen = dec.FrameRate()
	}
	return y4m.Write(sf)
})
err = y4m.Flush()
```

Other inputs:

- `dec.Decode(chunk)` accepts an Annex B byte stream split at arbitrary
  boundaries (MVC NAL units interleaved, as in the JVT conformance streams).
- `mvc.NewAUReader(r)` splits an Annex B stream into access units; use two of
  them to pair a base `.264` with a separate dependent `.mvc` stream (the
  files tsMuxeR writes).
- `dec.DecodeNAL(nal)` decodes one NAL unit without start code.

Frames are returned in display order once fully decoded. `NextFrame` never
blocks; `WaitFrame` blocks until the next frame is ready. Frame planes stay
valid until `Release`; buffers that are not released are not reused (the
decoder allocates new ones), so release frames promptly or copy them.
A `Decoder` must be used from one goroutine; decoding work is spread over
`Options.Threads` worker goroutines internally.

## mvcdec

```sh
mvcdec [flags] movie.m2ts            # Blu-ray transport stream
mvcdec [flags] stream.264            # interleaved Annex B stream
mvcdec [flags] base.264 dep.mvc      # separate view streams (tsMuxeR)

# full side-by-side Y4M into x264
mvcdec -y4m - -layout sbs movie.m2ts | x264 --demuxer y4m --frame-packing 3 -o sbs.264 -

# raw planes of each view
mvcdec -base left.yuv -dep right.yuv movie.m2ts
```

`-layout` is `sbs` (3840x1080 for 1080p), `tab`, `base` or `dep`; `-swap`
exchanges the views. The Y4M frame rate comes from the stream's VUI timing
(`-fps num:den` overrides it).

## mvctools

```sh
mvctools --check                                   # which external tools are installed
mvctools --input "Life of Pi (2012).iso" --output "Life of Pi (2012) 3D.mkv"
mvctools --remux --input disc.iso --output film.m2ts --audio-lang eng --audio-best
```

Converts a Blu-ray 3D disc image, BDMV folder, playlist or m2ts into a
side-by-side MKV with the audio and subtitles you choose, or remuxes the
disc's own MVC video with just those tracks. The disc is read once and in
place — an image is not extracted, the views are not demuxed to disk — and
decoded in process, and the MKV is written in process too; the only external
tool is the encoder, x264/x265 or ffmpeg (for hardware encoding and
half-SBS), and a remux needs no tools at all. tsMuxeR and mkvmerge remain
available with `--demuxer tsmuxer` and `--muxer mkvmerge`. See
[cmd/mvctools](cmd/mvctools/README.md).
A container with the whole toolchain is published as
`ghcr.io/brunoga/mvctools` for amd64 and arm64.

## Performance

Measured on an Intel Core Ultra 9 285K (24 cores), Go 1.27.1:

| stream | single thread | all cores |
|---|---|---|
| 3D Blu-ray, 1080p24 MVC, ~28 Mbit/s (stereo pairs/s) | 113 | ~1100 |
| x264 1080p, 25 Mbit/s, B-pyramid, weighted pred, deblocking (frames/s) | 119 | ~1000 |
| x264 1080p, crf 35 (1.5 Mbit/s, mostly skipped macroblocks) (frames/s) | 745 | |

For comparison, edge264 (C, SSE/AVX2 intrinsics) decodes the same clips at
126 stereo pairs/s, 127 and ~880 frames/s on one core. The CABAC residual
decoding, motion compensation, intra prediction and deblocking are all
assembly here (with pure-Go fallbacks, `-tags purego`); the remaining
difference is per-macroblock bookkeeping that is still Go.

Decoding from memory, one access unit at a time (`go test -bench File`);
`mvcdec` with file I/O is somewhat slower. Real time for a 3D Blu-ray needs 24 stereo pairs/s, so a
single core is over 3x real time.

## Supported features

- Profiles: Baseline (no FMO/ASO), Main, High, Stereo High, Multiview High
  (two views).
- CAVLC and CABAC, I/P/B slices, 8x8 transform, scaling matrices, weighted
  prediction (explicit and implicit), spatial and temporal direct, long-term
  references and all MMCO operations, frame_num gaps, multiple slices,
  POC types 0/1/2, reference list modification including inter-view
  references, bumping/reordering via VUI `num_reorder_frames`.
- MVC: prefix NAL units (14), subset SPS (15), coded slice extensions (20),
  Blu-ray dependent view delimiters (24), dependent view NAL units arriving
  before their base view, streams whose dependent view starts late.

Not supported (not used by 3D Blu-ray): interlaced coding (field pictures,
PAFF, MBAFF), 4:2:2 / 4:4:4, bit depths above 8, FMO/ASO, SP/SI slices,
more than two views.

Malformed input never panics (errors are returned and lost macroblocks are
concealed from the previous reference picture); decoding is fuzzed with
`go test -fuzz FuzzDecode`.

## Testing

```sh
go test -race ./...                            # assembly kernels vs Go, conformance, mvctools
go test -tags purego ./...                     # the pure-Go decoder every other platform runs
MVC_BENCH_FILE=clip.264 go test -bench File -run X
```

`testdata/bluray` is a synthetic Blu-ray 3D (a folder and a UDF image) the
built-in demuxer is tested on with no tools installed. The `mvctools`
end-to-end tests also build a real 3D m2ts and image with tsMuxeR and convert
them with both demuxers; they skip when tsMuxeR, x264, mkvmerge or ffmpeg are
not on `PATH`.

`testdata/conformance` holds the ITU-T/JVT conformance bitstreams with
per-view output hashes (from edge264-mvc). `tools/refdump` is a small C
program that dumps both views with edge264, used as the reference for real
Blu-ray content where no other decoder outputs the dependent view.

## Layout

| file | contents |
|---|---|
| `decoder.go` | public API, NAL dispatch, picture/AU management, workers |
| `params.go`, `slice.go` | SPS / subset SPS / PPS, slice headers |
| `dpb.go` | POC, reference marking, reference lists, MVC inter-view refs |
| `cabac*.go`, `cavlc.go`, `mb*.go`, `recon.go` | entropy decoding, macroblock layer |
| `mvpred.go`, `inter.go`, `intra.go`, `transform.go`, `deblock.go` | prediction and reconstruction |
| `*_amd64.s`, `*_amd64.go` | assembly kernels and their dispatch; `*_noasm.go`, `*_generic.go` the Go fallbacks |
| `aureader.go`, `m2ts/` | access unit splitting, transport stream reading (PES, program tables) |
| `stream.go`, `y4m.go` | whole-stream decoding loop, Y4M output |
| `cmd/mvcdec`, `cmd/mvctools` | the commands |
| `internal/convert` | the Blu-ray 3D conversion pipeline behind mvctools, with the built-in demuxer and remuxer |
| `internal/bdmv` | Blu-ray structure: playlists, clip info, folders and UDF images read in place |
| `internal/esinfo` | audio and video stream headers, for track listings |
| `internal/mkv` | the Matroska muxer: H.264/HEVC frame timing from picture order counts, audio and PGS framing |
