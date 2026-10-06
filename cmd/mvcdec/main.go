// Command mvcdec decodes H.264/MVC stereo video into raw YUV or Y4M.
//
// Inputs:
//
//	mvcdec [flags] movie.m2ts           Blu-ray transport stream (PIDs 0x1011/0x1012)
//	mvcdec [flags] stream.264           Annex B stream (MVC views interleaved)
//	mvcdec [flags] base.264 dep.mvc     separate base and dependent view streams
//
// Example: full side-by-side 3D encode with x264
//
//	mvcdec -y4m - -layout sbs movie.m2ts | x264 --demuxer y4m -o out.264 -
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	pversion "github.com/brunoga/mvc/internal/version"

	"github.com/brunoga/mvc"
)

var (
	baseOut   = flag.String("base", "", "write the base view as raw YUV 4:2:0 to `file`")
	depOut    = flag.String("dep", "", "write the dependent view as raw YUV 4:2:0 to `file`")
	y4mOut    = flag.String("y4m", "", "write YUV4MPEG2 to `file` (- for stdout)")
	layout    = flag.String("layout", "sbs", "Y4M frame layout: sbs (full side-by-side), tab (top-and-bottom), base, dep")
	fps       = flag.String("fps", "", "Y4M frame rate `num:den` (default: from the stream, else 24000:1001)")
	swap      = flag.Bool("swap", false, "exchange the views in the Y4M output (dependent view first)")
	muxOut    = flag.String("mux", "", "write the interleaved Annex B stream (base+dependent) to `file`")
	maxFrames = flag.Int("n", 0, "stop after `n` access units (0 = all)")
	baseOnly  = flag.Bool("2d", false, "decode the base view only")
	threads   = flag.Int("threads", 0, "pictures decoded in parallel (0 = number of CPUs)")
)

func create(name string) *bufio.Writer {
	if name == "-" {
		return bufio.NewWriterSize(os.Stdout, 8<<20)
	}
	// Write-only: os.Create would open a pipe named through /dev/stdout
	// read-write, and a process holding its own read end never sees EPIPE.
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec // the user's own output path
	if err != nil {
		log.Fatal(err)
	}
	return bufio.NewWriterSize(f, 8<<20)
}

func open(name string) *os.File {
	f, err := os.Open(name) //nolint:gosec // the user's own input path
	if err != nil {
		log.Fatal(err)
	}
	return f
}

// version is set at build time with -ldflags="-X main.version=..."; left
// alone, the module version the toolchain embeds is reported.
var version = pversion.Placeholder

func main() {
	showVer := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "mvcdec %s — H.264/MVC stereo decoder\n\n", pversion.Resolve(version))
		fmt.Fprintf(os.Stderr, "usage: mvcdec [flags] input.{m2ts,264} [dependent.mvc]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVer {
		fmt.Printf("mvcdec %s\n", pversion.Resolve(version))
		return
	}
	if flag.NArg() < 1 || flag.NArg() > 2 {
		flag.Usage()
		os.Exit(2)
	}
	if !mvc.Layout(*layout).Valid() {
		log.Fatalf("unknown layout %q", *layout)
	}
	var fpsNum, fpsDen int
	if *fps != "" {
		if _, err := fmt.Sscanf(*fps, "%d:%d", &fpsNum, &fpsDen); err != nil || fpsNum <= 0 || fpsDen <= 0 {
			log.Fatalf("bad frame rate %q (want num:den)", *fps)
		}
	}

	var bw, dw *bufio.Writer
	var yw *mvc.Y4MWriter
	var opts mvc.DecodeOptions
	var flushes []func() error
	flush := func() {
		for _, f := range flushes {
			if err := f(); err != nil {
				log.Fatal(err)
			}
		}
	}
	if *baseOut != "" {
		bw = create(*baseOut)
		flushes = append(flushes, bw.Flush)
	}
	if *depOut != "" {
		dw = create(*depOut)
		flushes = append(flushes, dw.Flush)
	}
	if *muxOut != "" {
		mw := create(*muxOut)
		flushes = append(flushes, mw.Flush)
		opts.Mux = mw
	}
	if *y4mOut != "" {
		w := create(*y4mOut)
		yw = mvc.NewY4MWriter(w, mvc.Layout(*layout))
		yw.SwapViews = *swap
		flushes = append(flushes, yw.Flush, w.Flush)
	}
	opts.MaxFrames = *maxFrames
	nerr := 0
	opts.OnError = func(err error) {
		nerr++
		if nerr <= 10 {
			log.Printf("decode: %v", err)
		}
	}

	dec := mvc.NewDecoder(mvc.Options{BaseOnly: *baseOnly, Threads: *threads})
	src := mvc.Source{Format: mvc.FormatByName(flag.Arg(0)), R: open(flag.Arg(0))}
	if flag.NArg() == 2 {
		src.Format = mvc.FormatSplit
		src.Dependent = open(flag.Arg(1))
	}
	start := time.Now()
	st, err := dec.DecodeStream(src, opts, func(sf *mvc.StereoFrame) error {
		if bw != nil {
			if err := mvc.WritePlanes(bw, sf.Base); err != nil {
				return err
			}
		}
		if dw != nil && sf.Dependent != nil {
			if err := mvc.WritePlanes(dw, sf.Dependent); err != nil {
				return err
			}
		}
		if yw != nil {
			if yw.FPSNum == 24000 && yw.FPSDen == 1001 && fpsNum == 0 {
				if n, d := dec.FrameRate(); n > 0 {
					yw.FPSNum, yw.FPSDen = n, d
				}
			} else if fpsNum > 0 {
				yw.FPSNum, yw.FPSDen = fpsNum, fpsDen
			}
			return yw.Write(sf)
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	flush()
	el := time.Since(start)
	fmt.Fprintf(os.Stderr, "%d frames (%d with dependent view) in %v: %.1f fps, %d errors\n",
		st.Frames, st.DependentFrames, el.Round(time.Millisecond), float64(st.Frames)/el.Seconds(), st.Errors)
}
