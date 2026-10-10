# bdtools — Blu-ray tools in pure Go

[`bdtools`](cmd/bdtools/README.md) turns Blu-rays into files any player
handles: a **3D Blu-ray into a side-by-side MKV** (the MVC second eye that
Plex and libavcodec cannot decode, stacked beside the first), a **2D or Ultra
HD Blu-ray into an H.264, HEVC or AV1 MKV** keeping HDR10, HDR10+ and Dolby
Vision, or a **remux** with only the tracks you want. It reads `.iso` images
and BDMV folders in place (no mount, no extraction), playlists, `.m2ts`
streams and Matroska remuxes, picks the feature itself, and resumes an
interrupted conversion where it stopped.

Everything between the disc and the encoder is Go, in this repository: the
UDF and Blu-ray structure readers, the transport stream demuxer, four video
decoders (H.264 with MVC, HEVC, MPEG-2, VC-1), a deinterlacer, the Dolby
Vision RPU and enhancement-layer processing, and the Matroska muxer. GPUs
(NVIDIA, Intel, AMD, Apple) are driven in process through their own system
libraries, with no cgo: with one, a conversion runs no other program at all.
Without one, x264, x265 or SVT-AV1 encode.

```sh
bdtools --check                                              # what is installed
bdtools --list --input "Avatar (2009) 3D.iso"                # what the disc holds
bdtools --input "Avatar (2009) 3D.iso" --output "Avatar (2009) 3D FSBS.mkv" \
        --codec h265 --audio-lang eng --audio-best --subs-lang eng
bdtools --input "Dune (2021) UHD.iso" --output "Dune (2021).mkv" --codec h265   # HDR10/DV kept
bdtools --remux --input "Dune (2021) UHD.iso" --output "Dune (2021).mkv"       # no re-encode
```

More in [bdtools' common tasks](cmd/bdtools/README.md#common-tasks).

The decoders, each checked against its standard's conformance streams:

| Decoder | Package | What it decodes | Checked against | Speed (Core Ultra 9 285K) |
|---|---|---|---|---|
| H.264 / MVC | [`mvc`](mvc) | Baseline, Main, High; Stereo and Multiview High (two views); progressive | the JVT conformance streams and a whole 3D Blu-ray, byte-identical to edge264 | ~1100 stereo pairs/s, 113 on one core |
| HEVC | `internal/hevc` | Main, Main 10: everything Ultra HD Blu-ray uses | all 152 JCT-VC conformance streams of those profiles, byte-identical to ffmpeg | ~250 fps for a disc's 4K stream |
| MPEG-2 | `internal/mpeg2` | Main Profile, frame and field pictures | the ISO conformance streams, within the transform's tolerance of ffmpeg (64 dB and up) | ~30 fps for 1080i |
| VC-1 | `internal/vc1` | Advanced Profile, progressive and interlaced | the SMPTE conformance streams and a VC-1 Blu-ray, byte-identical to ffmpeg | ~110 fps for 1080p on one core |

The HEVC, MPEG-2 and H.264 parsers also drive VAAPI (Intel and AMD GPUs on
Linux), which has no parser of its own; NVDEC and VideoToolbox decode on
their own. See [what works where](cmd/bdtools/README.md#what-works-where).

The H.264/MVC decoder is usable on its own as a library (below), and
`mvcdec` decodes a stream to raw YUV or Y4M. [`probe`](probe) identifies a
disc image or MKV from a pre-download sample.

## Build

```sh
go build ./cmd/mvcdec ./cmd/bdtools
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

## bdtools

```sh
bdtools --check                                   # which external tools are installed
bdtools --input "Life of Pi (2012).iso" --output "Life of Pi (2012) 3D.mkv"
bdtools --input "Dune (2021) UHD.iso" --output "Dune (2021).mkv" --codec h265
bdtools --remux --input disc.iso --output film.m2ts --audio-lang eng --audio-best
```

Converts a Blu-ray — a disc image, BDMV folder, playlist or m2ts, or a
Matroska remux of one — into an MKV with the audio and subtitles you choose:
a 3D one side by side (full or half width, subtitles flat or drawn in 3D),
a 2D or Ultra HD one as it is, in H.264, HEVC or AV1 at 8 or 10 bits, HDR10,
HDR10+ and Dolby Vision kept (profile 8.1, its full enhancement layer
composed in, or profile 7 with the layers apart). Or it remuxes, with no
re-encoding: a 3D disc's MVC into one `.m2ts`, a 2D or Ultra HD disc (Dolby
Vision included) or an MKV into an MKV.

The disc is read once and in place — an image is not extracted, nothing is
demuxed to disk but the audio and subtitles — and decoded and muxed in
process. A GPU decodes and encodes in process as well, through its own system
library, so with one the conversion needs no tools at all; otherwise the only
external tool is x264, x265 or SvtAv1EncApp (or ffmpeg, for software
half-SBS and the rare interlaced H.264 without a GPU decoder), and a remux
needs none. A container with the whole toolchain is published as
`ghcr.io/brunoga/bdtools` for amd64 and arm64. See
[cmd/bdtools](cmd/bdtools/README.md), which starts with
[examples of common tasks](cmd/bdtools/README.md#common-tasks).

## Probing a source before downloading it

[`probe`](probe) identifies a Blu-ray image or a Matroska file from the little
a pre-download sample holds — for a torrent, a piece or two — and reports what
the container states: 2D or 3D and how (MVC, side by side, top-bottom), the
feature's length, and its video, audio and subtitle tracks. It reads no
video and pulls in nothing of the decoder.

```go
r, err := probe.Image(sample, imageSize, name) // an io.ReaderAt over the pieces you have
var missing *probe.MissingDataError
if errors.As(err, &missing) {
	// fetch the piece holding missing.Offset, then try again
}
if r.Is3D && r.Layout == probe.LayoutMVC { /* a Blu-ray 3D */ }

r, err = probe.Matroska(prefix, name) // the first 32 KiB or so of an MKV
```

A disc image keeps its UDF directory at the front and its playlists and clip
info at the end, so the first and last pieces of a torrent are usually enough:
on ten Blu-ray 3D images, two 16 MiB pieces were, except one disc with 316
playlists, which needed a third — the error says which bytes. A read the
sample cannot satisfy is an error naming them, never a panic and never a
plausible wrong answer. Fields a source does not state are left zero: a
disc's "multi-channel" audio has no channel count until the probe can read
the stream's first frames, which it does when the sample holds them.

## H.264/MVC decoder performance

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

## H.264/MVC decoder features

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
more than two views. bdtools hands interlaced 2D H.264 (1080i discs) to
NVDEC, VideoToolbox or ffmpeg instead.

With an accelerator (`Decoder.SetAccel`), the decoder parses a 2D
progressive stream and keeps its reference pictures and output order, and
hands each picture's slices to the GPU: this is how bdtools decodes H.264 on
VAAPI.

Malformed input never panics (errors are returned and lost macroblocks are
concealed from the previous reference picture); decoding is fuzzed with
`go test -fuzz FuzzDecode`.

## Testing

```sh
go test -race ./...                            # assembly kernels vs Go, conformance, bdtools
go test -tags purego ./...                     # the pure-Go decoder every other platform runs
MVC_BENCH_FILE=clip.264 go test -bench File -run X
```

`testdata/bluray` is a synthetic Blu-ray 3D (a folder and a UDF image) the
built-in demuxer is tested on with no tools installed. The `bdtools`
end-to-end tests convert it, and a Matroska remux built from the MVC
fixtures, through x264; they skip when x264 is not on `PATH`.

`testdata/conformance` holds the ITU-T/JVT conformance bitstreams with
per-view output hashes (from edge264-mvc). `tools/refdump` is a small C
program that dumps both views with edge264, used as the reference for real
Blu-ray content where no other decoder outputs the dependent view.

The other decoders' conformance suites are large and not committed; point
the tests at them (they skip otherwise, and compare against ffmpeg, which
they need):

```sh
BDTOOLS_HEVC_SAMPLES=~/hevc-conformance go test -run Conformance ./internal/hevc   # JCT-VC *.bit
BDTOOLS_MPEG2_SAMPLES=~/fate/mpeg2 go test -run Conformance ./internal/mpeg2        # FATE's mpeg2/
BDTOOLS_VC1_SAMPLES=~/fate/vc1 go test -run Conformance ./internal/vc1              # FATE's vc1/
BDTOOLS_HEVC_BENCH=uhd.hevc go test -bench Decode -run X ./internal/hevc
```

The GPU tests run on whatever the machine has and skip what it lacks. On
Linux, VAAPI's are pointed at a driver with libva's own variables, e.g.
Intel's from a Flatpak runtime:

```sh
D=/var/lib/flatpak/runtime/org.freedesktop.Platform.VAAPI.Intel/x86_64/25.08/active/files
LIBVA_DRIVERS_PATH=$D LIBVA_DRIVER_NAME=iHD LD_LIBRARY_PATH=$D/lib \
  BDTOOLS_HEVC_SAMPLES=~/hevc-conformance BDTOOLS_MPEG2_SAMPLES=~/fate/mpeg2 \
  go test -run 'VAAPI|GPUDecoders' ./internal/gpu
```

The GPU libraries' structures are mirrored in Go and checked against their
C headers (`MVC_LIBVA_HEADERS`, `MVC_NVENC_HEADERS` and the like name where
the headers are; `testdata/vaoff.c` generates `vaapi_layout.go`).

## Layout

| file | contents |
|---|---|
| `mvc/decoder.go` | public API, NAL dispatch, picture/AU management, workers |
| `mvc/params.go`, `mvc/slice.go` | SPS / subset SPS / PPS, slice headers |
| `mvc/dpb.go` | POC, reference marking, reference lists, MVC inter-view refs |
| `mvc/cabac*.go`, `cavlc.go`, `mb*.go`, `recon.go` | entropy decoding, macroblock layer |
| `mvc/mvpred.go`, `inter.go`, `intra.go`, `transform.go`, `deblock.go` | prediction and reconstruction |
| `mvc/*_amd64.s`, `*_amd64.go` | assembly kernels and their dispatch; `*_noasm.go`, `*_generic.go` the Go fallbacks |
| `mvc/aureader.go`, `m2ts/` | access unit splitting, transport stream reading (PES, program tables) |
| `mvc/stream.go`, `y4m.go` | whole-stream decoding loop, Y4M output |
| `cmd/mvcdec`, `cmd/bdtools` | the commands |
| `tools/pgskodi`, `tools/felrd`, `tools/qcmp`, `tools/refdump` | splitting wide PGS subtitles for Kodi in films converted before bdtools did; measuring Dolby Vision FEL encodes against the source's composition; measuring any encode against its source (PSNR and VMAF, Dolby Vision FEL and 3D sources included); the edge264 reference dumper |
| `internal/convert` | the Blu-ray 3D conversion pipeline behind bdtools, with the built-in demuxer and remuxer, resumable segmented encoding, and 3D subtitles |
| `internal/hevc`, `internal/mpeg2`, `internal/vc1`, `internal/deint` | HEVC, MPEG-2 and VC-1 decoders in Go (whose parsers also drive VAAPI), and a deinterlacer, for 2D sources |
| `internal/dovi`, `internal/hdr` | Dolby Vision: RPU parsing and rewriting (profile 7 to 8.1), enhancement-layer composition; HDR10 and HDR10+ metadata |
| `internal/gpu` | the GPU encoders and decoders, driven in process through their system libraries: NVENC and NVDEC, VAAPI, VideoToolbox, Media Foundation |
| `internal/bdmv` | Blu-ray structure: playlists, clip info, folders and UDF images read in place |
| `internal/esinfo` | audio and video stream headers, for track listings |
| `internal/mkv` | the Matroska muxer: H.264/HEVC frame timing from picture order counts, AV1 temporal units, audio and PGS framing; and a streaming Matroska reader |
| `probe` | identifying a disc image or Matroska file from a pre-download sample |
