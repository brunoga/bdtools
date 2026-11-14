# bdtools — Blu-ray tools in pure Go

[`bdtools`](cmd/bdtools/README.md) is a command-line tool that turns a
Blu-ray into a file any player handles. It converts a **3D Blu-ray into a
side-by-side MKV**. The second eye of a 3D disc is coded as MVC, which Plex
and libavcodec cannot decode, so bdtools decodes it and stacks it beside the
first. It converts a **2D or Ultra HD Blu-ray into an H.264, HEVC or AV1
MKV** and keeps HDR10, HDR10+ and Dolby Vision. Or it makes a **remux**
with only the tracks you want.

bdtools reads `.iso` images and BDMV folders in place. Nothing is mounted
and nothing is extracted. It also reads playlists, `.m2ts` streams and
Matroska remuxes. It picks the feature itself, and when a conversion is
interrupted it resumes where it stopped.

Everything between the disc and the encoder is Go, and all of it is in this
repository. That means the UDF and Blu-ray structure readers, the transport
stream demuxer, four video decoders (H.264 with MVC, HEVC, MPEG-2 and VC-1),
a deinterlacer, the Dolby Vision RPU and enhancement-layer processing, and
the Matroska muxer. GPUs from NVIDIA, Intel, AMD and Apple are driven in
process through their own system libraries, without cgo. With a GPU, a
conversion runs no other program at all. Without one, x264, x265 or SVT-AV1
does the encoding.

```sh
bdtools --check                                              # what is installed
bdtools --list --input "Avatar (2009) 3D.iso"                # what the disc holds
bdtools --input "Avatar (2009) 3D.iso" --output "Avatar (2009) 3D FSBS.mkv" \
        --codec h265 --audio-lang eng --audio-best --subs-lang eng
bdtools --input "Dune (2021) UHD.iso" --output "Dune (2021).mkv" --codec h265   # HDR10/DV kept
bdtools --remux --input "Dune (2021) UHD.iso" --output "Dune (2021).mkv"       # no re-encode
bdtools --input "Furiosa (2024).mkv" --output "Furiosa (2024) 3D.iso" --to-3d    # a Blu-ray 3D made
```

There is more in [the cookbook](docs/cookbook.md), which gives a complete
command for each common operation, and in
[bdtools' common tasks](cmd/bdtools/README.md#common-tasks). The
[grading manual](docs/grading.md) explains how to grade a 2D-to-3D
conversion by eye, on the web page or in a headset. Both manuals are also
web pages with the pictures, in [docs/manual](docs/manual/index.html). They
are carried in the binary and in every release, and `bdtools --manual :8080`
serves them.

These are the decoders. Each one is checked against the conformance streams
of its standard.

| Decoder | Package | What it decodes | Checked against | Speed (Core Ultra 9 285K) |
|---|---|---|---|---|
| H.264 / MVC | [`mvc`](mvc) | Baseline, Main and High; Stereo High and Multiview High with two views; progressive pictures | the JVT conformance streams, and a whole 3D Blu-ray byte for byte against edge264 | ~1100 stereo pairs/s, 113 on one core |
| HEVC | `internal/hevc` | Main and Main 10, which is everything Ultra HD Blu-ray uses | all 152 JCT-VC conformance streams of those profiles, byte for byte against ffmpeg | ~250 fps for a disc's 4K stream |
| MPEG-2 | `internal/mpeg2` | Main Profile, frame and field pictures | the ISO conformance streams, within the transform's tolerance of ffmpeg, 64 dB and up | ~30 fps for 1080i |
| VC-1 | `internal/vc1` | Advanced Profile, progressive and interlaced | the SMPTE conformance streams, and a VC-1 Blu-ray byte for byte against ffmpeg | ~110 fps for 1080p on one core |

The HEVC, MPEG-2 and H.264 parsers also drive VAAPI, which Intel and AMD
GPUs use on Linux, because VAAPI has no parser of its own. NVDEC and
VideoToolbox decode on their own. See
[what works where](cmd/bdtools/README.md#what-works-where).

The H.264/MVC decoder can be used on its own as a library, as shown below.
`mvcdec` decodes a stream to raw YUV or Y4M. [`probe`](probe) identifies a
disc image, an MKV or an MP4 from a pre-download sample.

## Build

```sh
go build ./cmd/mvcdec ./cmd/bdtools
```

On amd64 the hot paths are hand-written Go assembly. The files are
`*_amd64.s`, the Go toolchain assembles them, and no cgo is involved. The
assembly covers the CABAC residual, motion vector difference and coded block
pattern decoders, motion compensation (6-tap luma, bilinear chroma,
averaging and weighted prediction), the inverse transforms, the deblocking
filters and the per-macroblock bookkeeping. It is used when the CPU has
AVX2, BMI2 and LZCNT, which is checked with CPUID at startup. Other CPUs and
other architectures use the equivalent Go code. You can force the Go code on
any CPU with `-tags purego`. Every assembly routine has a randomized test
against its Go counterpart, in `TestCabac*Asm` and `TestSIMD*`.

Releases are built with `CGO_ENABLED=0` for Linux, macOS and Windows, on
amd64, arm64 and arm/v7. See `.goreleaser.yml`.

## The H.264/MVC decoder as a library

```go
import (
	"github.com/brunoga/bdtools/m2ts"
	"github.com/brunoga/bdtools/mvc"
)

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

You can also let the decoder drive the loop over a whole stream and write
the result out as Y4M. The stream can be a transport stream, an interleaved
Annex B stream, or the demuxed pair that tsMuxeR writes:

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

There are other ways to feed the decoder:

- `dec.Decode(chunk)` accepts an Annex B byte stream split at arbitrary
  boundaries. The MVC NAL units are interleaved in it, as they are in the
  JVT conformance streams.
- `mvc.NewAUReader(r)` splits an Annex B stream into access units. Use two
  of them to pair a base `.264` with a separate dependent `.mvc` stream,
  which are the files tsMuxeR writes.
- `dec.DecodeNAL(nal)` decodes one NAL unit without its start code.

Frames come back in display order once they are fully decoded. `NextFrame`
never blocks. `WaitFrame` blocks until the next frame is ready. A frame's
planes stay valid until you call `Release`. A buffer that is not released is
not reused, and the decoder allocates a new one instead, so release frames
promptly or copy them. A `Decoder` must be used from one goroutine.
Internally, the decoding work is spread over `Options.Threads` worker
goroutines.

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

`-layout` is one of `sbs`, `tab`, `base` or `dep`. Side by side makes a
3840x1080 picture from a 1080p stream. `-swap` exchanges the views. The Y4M
frame rate comes from the stream's VUI timing, and `-fps num:den` overrides
it.

## bdtools

```sh
bdtools --check                                   # which external tools are installed
bdtools --input "Life of Pi (2012).iso" --output "Life of Pi (2012) 3D.mkv"
bdtools --input "Dune (2021) UHD.iso" --output "Dune (2021).mkv" --codec h265
bdtools --remux --input disc.iso --output film.m2ts --audio-lang eng --audio-best
```

bdtools converts a Blu-ray into an MKV with the audio and subtitles you
choose. The source can be a disc image, a BDMV folder, a playlist or an
m2ts, or a Matroska remux of one. A 3D disc comes out side by side, at full
or half width, with the subtitles flat or drawn in 3D. A 2D or Ultra HD disc
comes out as it is, in H.264, HEVC or AV1 at 8 or 10 bits. HDR10, HDR10+ and
Dolby Vision are kept. Dolby Vision can be written as profile 8.1, with its
full enhancement layer composed in, or as profile 7, with the layers kept
apart. bdtools can also remux, with no re-encoding. A 3D disc's MVC goes
into one `.m2ts`. A 2D or Ultra HD disc, Dolby Vision included, or an MKV
goes into an MKV.

The disc is read once and in place. An image is not extracted, and nothing
is demuxed to disk except the audio and subtitles. Decoding and muxing
happen in process. A GPU decodes and encodes in process as well, through its
own system library, so with a GPU the conversion needs no external tools at
all. Without one, the only external tool is x264, x265 or SvtAv1EncApp.
ffmpeg takes over for software half-SBS, and for the rare interlaced H.264
disc when there is no GPU decoder. A remux needs no tools. A container with
the whole toolchain is published as `ghcr.io/brunoga/bdtools` for amd64 and
arm64. See [cmd/bdtools](cmd/bdtools/README.md), which starts with
[examples of common tasks](cmd/bdtools/README.md#common-tasks).

## Probing a source before downloading it

[`probe`](probe) identifies a Blu-ray image, a Matroska file, or an MP4 or
QuickTime file from the little that a pre-download sample holds. For a
torrent, that is a piece or two. It reports what the container states:
whether the film is 2D or 3D, and if 3D, how it is laid out (MVC, MV-HEVC,
side by side or top-bottom), the feature's length, and its video, audio and
subtitle tracks. It reads no video and pulls in nothing of the decoder.

```go
r, err := probe.Image(sample, imageSize, name) // an io.ReaderAt over the pieces you have
var missing *probe.MissingDataError
if errors.As(err, &missing) {
	// fetch the piece holding missing.Offset, then try again
}
if r.Is3D && r.Layout == probe.LayoutMVC { /* a Blu-ray 3D */ }

r, err = probe.Matroska(prefix, name) // the first 32 KiB or so of an MKV

r, err = probe.MP4(sample, fileSize, name) // an io.ReaderAt: the movie box is at the start or the end
if r.Is3D && r.Layout == probe.LayoutMVHEVC { /* Apple spatial video */ }
```

A disc image keeps its UDF directory at the front and its playlists and
clip info at the end, so the first and last pieces of a torrent are usually
enough. On ten Blu-ray 3D images, two 16 MiB pieces were enough for all but
one. That disc had 316 playlists and needed a third piece, and the error
said which bytes were missing. When the sample cannot satisfy a read, the
result is an error that names the missing bytes. It is never a panic, and
never a plausible wrong answer. Fields the source does not state are left
zero. For example, a disc's "multi-channel" audio has no channel count until
the probe can read the stream's first frames, which it does when the sample
holds them.

## H.264/MVC decoder performance

Measured on an Intel Core Ultra 9 285K (24 cores), Go 1.27.1:

| stream | single thread | all cores |
|---|---|---|
| 3D Blu-ray, 1080p24 MVC, ~28 Mbit/s (stereo pairs/s) | 113 | ~1100 |
| x264 1080p, 25 Mbit/s, B-pyramid, weighted pred, deblocking (frames/s) | 119 | ~1000 |
| x264 1080p, crf 35 (1.5 Mbit/s, mostly skipped macroblocks) (frames/s) | 745 | |

For comparison, edge264, which is C with SSE/AVX2 intrinsics, decodes the
same clips at 126 stereo pairs/s, 127 frames/s and ~880 frames/s on one
core. Here the CABAC residual decoding, motion compensation, intra
prediction and deblocking are all assembly, with pure-Go fallbacks under
`-tags purego`. The remaining difference is the per-macroblock bookkeeping,
which is still Go.

These numbers decode from memory, one access unit at a time, with
`go test -bench File`. `mvcdec` with file I/O is somewhat slower. Real time
for a 3D Blu-ray is 24 stereo pairs/s, so a single core is over 3x real
time.

## H.264/MVC decoder features

The decoder supports the following:

- The Baseline, Main, High, Stereo High and Multiview High profiles.
  Baseline is supported without FMO and ASO. Multiview High is supported
  with two views.
- CAVLC and CABAC, I, P and B slices, the 8x8 transform, scaling matrices,
  explicit and implicit weighted prediction, spatial and temporal direct
  prediction, long-term references and all MMCO operations, frame_num gaps,
  multiple slices, POC types 0, 1 and 2, and reference list modification
  including inter-view references. Output is bumped and reordered through
  the VUI's `num_reorder_frames`.
- For MVC, prefix NAL units (type 14), the subset SPS (15), coded slice
  extensions (20) and the Blu-ray dependent view delimiters (24). The
  dependent view's NAL units may arrive before their base view, and the
  dependent view may start late in the stream.

Some things are not supported, because 3D Blu-ray does not use them:
interlaced coding (field pictures, PAFF and MBAFF), 4:2:2 and 4:4:4 chroma,
bit depths above 8, FMO and ASO, SP and SI slices, and more than two views.
Interlaced 2D H.264, which 1080i discs use, is handed by bdtools to NVDEC,
VideoToolbox or ffmpeg instead.

The decoder can also work with an accelerator, set with `Decoder.SetAccel`.
It then parses a 2D progressive stream, keeps track of its reference
pictures and output order, and hands each picture's slices to the GPU. This
is how bdtools decodes H.264 on VAAPI.

Malformed input never panics. Errors are returned, and lost macroblocks are
concealed from the previous reference picture. Decoding is fuzzed with
`go test -fuzz FuzzDecode`.

## Testing

```sh
go test -race ./...                            # assembly kernels vs Go, conformance, bdtools
go test -tags purego ./...                     # the pure-Go decoder every other platform runs
MVC_BENCH_FILE=clip.264 go test -bench File -run X
```

`testdata/bluray` is a synthetic Blu-ray 3D, as both a folder and a UDF
image. The built-in demuxer is tested on it with no tools installed. The
`bdtools` end-to-end tests convert it through x264, and also convert a
Matroska remux built from the MVC fixtures. They skip when x264 is not on
`PATH`.

`testdata/conformance` holds the ITU-T/JVT conformance bitstreams, with
per-view output hashes taken from edge264-mvc. `tools/refdump` is a small C
program that dumps both views with edge264. It is the reference for real
Blu-ray content, where no other decoder outputs the dependent view.

The other decoders' conformance suites are large and are not committed.
Point the tests at them with these variables. They skip when the suites are
missing, and they compare against ffmpeg, so ffmpeg must be installed:

```sh
BDTOOLS_HEVC_SAMPLES=~/hevc-conformance go test -run Conformance ./internal/hevc   # JCT-VC *.bit
BDTOOLS_MPEG2_SAMPLES=~/fate/mpeg2 go test -run Conformance ./internal/mpeg2        # FATE's mpeg2/
BDTOOLS_VC1_SAMPLES=~/fate/vc1 go test -run Conformance ./internal/vc1              # FATE's vc1/
BDTOOLS_HEVC_BENCH=uhd.hevc go test -bench Decode -run X ./internal/hevc
```

The GPU tests run on whatever the machine has and skip what it lacks. On
Linux, the VAAPI tests are pointed at a driver with libva's own environment
variables. This example uses Intel's driver from a Flatpak runtime:

```sh
D=/var/lib/flatpak/runtime/org.freedesktop.Platform.VAAPI.Intel/x86_64/25.08/active/files
LIBVA_DRIVERS_PATH=$D LIBVA_DRIVER_NAME=iHD LD_LIBRARY_PATH=$D/lib \
  BDTOOLS_HEVC_SAMPLES=~/hevc-conformance BDTOOLS_MPEG2_SAMPLES=~/fate/mpeg2 \
  go test -run 'VAAPI|GPUDecoders' ./internal/gpu
```

The GPU libraries' structures are mirrored in Go and checked against their
C headers. Variables such as `MVC_LIBVA_HEADERS` and `MVC_NVENC_HEADERS` say
where the headers are. `testdata/vaoff.c` generates `vaapi_layout.go`.

## Layout

| file | contents |
|---|---|
| `mvc/decoder.go` | the public API, NAL dispatch, picture and access unit management, and the workers |
| `mvc/params.go`, `mvc/slice.go` | the SPS, subset SPS and PPS, and the slice headers |
| `mvc/dpb.go` | POC, reference marking, reference lists and MVC inter-view references |
| `mvc/cabac*.go`, `cavlc.go`, `mb*.go`, `recon.go` | entropy decoding and the macroblock layer |
| `mvc/mvpred.go`, `inter.go`, `intra.go`, `transform.go`, `deblock.go` | prediction and reconstruction |
| `mvc/*_amd64.s`, `*_amd64.go` | the assembly kernels and their dispatch. `*_noasm.go` and `*_generic.go` are the Go fallbacks |
| `mvc/aureader.go`, `m2ts/` | access unit splitting, and transport stream reading (PES and program tables) |
| `mvc/stream.go`, `y4m.go` | the whole-stream decoding loop, and Y4M output |
| `cmd/mvcdec`, `cmd/bdtools` | the commands |
| `tools/pgskodi`, `tools/felrd`, `tools/qcmp`, `tools/refdump` | `pgskodi` splits wide PGS subtitles for Kodi in films converted before bdtools did it. `felrd` measures Dolby Vision FEL encodes against the source's composition. `qcmp` measures any encode against its source in PSNR, VMAF and CAMBI banding, Dolby Vision FEL and 3D sources included. `refdump` is the edge264 reference dumper |
| `internal/convert` | the Blu-ray 3D conversion pipeline behind bdtools. It holds the built-in demuxer and remuxer, resumable segmented encoding, and the 3D subtitles |
| `internal/hevc`, `internal/mpeg2`, `internal/vc1`, `internal/deint` | the HEVC, MPEG-2 and VC-1 decoders in Go, whose parsers also drive VAAPI, and a deinterlacer. They are for 2D sources |
| `internal/dovi`, `internal/hdr` | Dolby Vision, that is RPU parsing and rewriting from profile 7 to 8.1, and enhancement-layer composition. Also HDR10 and HDR10+ metadata |
| `internal/gpu` | the GPU encoders and decoders, driven in process through their system libraries. NVENC and NVDEC, VAAPI, VideoToolbox and Media Foundation |
| `internal/bdmv` | the Blu-ray structure. Playlists, clip info, and folders and UDF images read in place |
| `internal/esinfo` | audio and video stream headers, for the track listings |
| `internal/mkv` | the Matroska muxer. It takes H.264 and HEVC frame timing from picture order counts, and frames AV1 temporal units, audio and PGS. Also a streaming Matroska reader |
| `probe` | identifies a disc image, a Matroska file, or an MP4 or QuickTime file from a pre-download sample |
