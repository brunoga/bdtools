// Command hevcenc codes raw 4:2:0 video with the HEVC encoder in this
// module, for its development: planar 8-bit or 10-bit little endian
// samples in, an Annex B stream out.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/pprof"
	"runtime/trace"
	"time"

	"github.com/brunoga/bdtools/internal/hevc"
)

func main() {
	var (
		in     = flag.String("i", "", "input: raw planar YUV 4:2:0 (8-bit bytes or 10-bit little endian words)")
		out    = flag.String("o", "", "output: an Annex B HEVC stream")
		width  = flag.Int("w", 1920, "picture width")
		height = flag.Int("h", 1080, "picture height")
		depth  = flag.Int("depth", 8, "bit depth: 8 or 10")
		qp     = flag.Int("qp", 27, "quantiser")
		keyint = flag.Int("keyint", 250, "IDR picture interval (1: intra only)")
		refs   = flag.Int("refs", 2, "reference pictures a P picture may use")
		bf     = flag.Int("bframes", -1, "B pictures between anchors: 0, 1, 3 or 7 (-1: the encoder's choice)")
		qpP    = flag.Int("qp-p", 4, "the P pictures' QP above -qp")
		qpB    = flag.Int("qp-b", 8, "the first-level B pictures' QP above -qp (one more per level)")
		rect   = flag.Bool("rect", false, "try rectangular partitions of inter CUs")
		nover  = flag.Bool("noverify", false, "leave out the decoding of every access unit written")
		thr    = flag.Int("threads", 0, "CTB rows coded at once per picture (0: the CPUs)")
		pipe   = flag.Int("pipeline", 0, "pictures coded at once (0: by the threads)")
		frames = flag.Int("frames", 0, "pictures to code (0: all)")
		fpsNum = flag.Int("fps-num", 24000, "frame rate numerator")
		fpsDen = flag.Int("fps-den", 1001, "frame rate denominator")
		prof   = flag.String("cpuprofile", "", "write a CPU profile here")
		trc    = flag.String("trace", "", "write an execution trace here")
	)
	flag.Parse()
	if *in == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *trc != "" {
		tf, err := os.Create(*trc)
		if err == nil && trace.Start(tf) == nil {
			defer trace.Stop()
		}
	}
	if err := run(*in, *out, *width, *height, *depth, *qp, *frames, *fpsNum, *fpsDen, *keyint, *refs, *bf, *qpP, *qpB, *thr, *pipe, *rect, *nover, *prof); err != nil {
		fmt.Fprintln(os.Stderr, "hevcenc:", err)
		os.Exit(1)
	}
}

func run(in, out string, w, h, depth, qp, frames, fpsNum, fpsDen, keyint, refs, bframes, qpP, qpB, threads, pipeline int, rect, noVerify bool, prof string) error {
	f, err := os.Open(in) //nolint:gosec // the operator's file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	o, err := os.Create(out) //nolint:gosec // the operator's file
	if err != nil {
		return err
	}
	defer func() { _ = o.Close() }()
	bw := bufio.NewWriterSize(o, 1<<20)
	e, err := hevc.NewEncoder(hevc.EncoderOptions{Width: w, Height: h, BitDepth: depth, QP: qp, FPSNum: fpsNum, FPSDen: fpsDen,
		Keyint: keyint, Refs: refs, BFrames: bframes, QPOffsetP: qpP, QPOffsetB: qpB, Rect: rect, Threads: threads, Pipeline: pipeline, NoVerify: noVerify})
	if err != nil {
		return err
	}
	if prof != "" {
		pf, err := os.Create(prof) //nolint:gosec // the operator's file
		if err != nil {
			return err
		}
		if err := pprof.StartCPUProfile(pf); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}
	bps := 1
	if depth > 8 {
		bps = 2
	}
	raw := make([]byte, (w*h+w*h/2)*bps)
	src := &hevc.Source{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4), StrideY: w, StrideC: w / 2}
	r := bufio.NewReaderSize(f, 1<<22)
	start := time.Now()
	n := 0
	for frames == 0 || n < frames {
		if _, err := io.ReadFull(r, raw); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return err
		}
		unpack := func(dst []uint16, b []byte) {
			if bps == 1 {
				for i := range dst {
					dst[i] = uint16(b[i])
				}
			} else {
				for i := range dst {
					dst[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
				}
			}
		}
		unpack(src.Y, raw[:w*h*bps])
		unpack(src.Cb, raw[w*h*bps:(w*h+w*h/4)*bps])
		unpack(src.Cr, raw[(w*h+w*h/4)*bps:])
		aus, err := e.Encode(src)
		if err != nil {
			return fmt.Errorf("picture %d: %w", n, err)
		}
		for _, au := range aus {
			if _, err := bw.Write(au); err != nil {
				return err
			}
		}
		n++
		if n%24 == 0 {
			el := time.Since(start).Seconds()
			fmt.Fprintf(os.Stderr, "\r%d pictures, %.1f fps", n, float64(n)/el)
		}
	}
	aus, err := e.Flush()
	if err != nil {
		return err
	}
	for _, au := range aus {
		if _, err := bw.Write(au); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	el := time.Since(start).Seconds()
	st := e.Stats()
	fmt.Fprintf(os.Stderr, "\r%d pictures in %.1f s (%.2f fps), %d bytes, PSNR Y %.3f Cb %.3f Cr %.3f dB\n",
		st.Pictures, el, float64(st.Pictures)/el, st.Bytes, st.PSNR(0, depth), st.PSNR(1, depth), st.PSNR(2, depth))
	return nil
}
