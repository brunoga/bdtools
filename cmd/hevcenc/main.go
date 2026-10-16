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
		right  = flag.String("right", "", "the right view, as -i: code MV-HEVC, -i the left view")
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
	if err := run(*in, *right, *out, *width, *height, *depth, *qp, *frames, *fpsNum, *fpsDen, *keyint, *refs, *bf, *qpP, *qpB, *thr, *pipe, *rect, *nover, *prof); err != nil {
		fmt.Fprintln(os.Stderr, "hevcenc:", err)
		os.Exit(1)
	}
}

func run(in, right, out string, w, h, depth, qp, frames, fpsNum, fpsDen, keyint, refs, bframes, qpP, qpB, threads, pipeline int, rect, noVerify bool, prof string) error {
	f, err := os.Open(in) //nolint:gosec // the operator's file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	views := 1
	var rf *os.File
	if right != "" {
		views = 2
		if rf, err = os.Open(right); err != nil { //nolint:gosec // the operator's file
			return err
		}
		defer func() { _ = rf.Close() }()
	}
	o, err := os.Create(out) //nolint:gosec // the operator's file
	if err != nil {
		return err
	}
	defer func() { _ = o.Close() }()
	bw := bufio.NewWriterSize(o, 1<<20)
	e, err := hevc.NewEncoder(hevc.EncoderOptions{Width: w, Height: h, BitDepth: depth, QP: qp, FPSNum: fpsNum, FPSDen: fpsDen,
		Keyint: keyint, Refs: refs, BFrames: bframes, QPOffsetP: qpP, QPOffsetB: qpB, Rect: rect, Threads: threads, Pipeline: pipeline, NoVerify: noVerify, Views: views})
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
	newSrc := func() *hevc.Source {
		return &hevc.Source{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4), StrideY: w, StrideC: w / 2}
	}
	src, srcR := newSrc(), newSrc()
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
	read := func(r io.Reader, dst *hevc.Source) error {
		if _, err := io.ReadFull(r, raw); err != nil {
			return err
		}
		unpack(dst.Y, raw[:w*h*bps])
		unpack(dst.Cb, raw[w*h*bps:(w*h+w*h/4)*bps])
		unpack(dst.Cr, raw[(w*h+w*h/4)*bps:])
		return nil
	}
	r := bufio.NewReaderSize(f, 1<<22)
	var rr io.Reader
	if rf != nil {
		rr = bufio.NewReaderSize(rf, 1<<22)
	}
	var layerBytes [2]int64
	write := func(aus [][]byte) error {
		for _, au := range aus {
			countLayers(au, &layerBytes)
			if _, err := bw.Write(au); err != nil {
				return err
			}
		}
		return nil
	}
	start := time.Now()
	n := 0
	for frames == 0 || n < frames {
		err := read(r, src)
		if err == nil && rr != nil {
			err = read(rr, srcR)
		}
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return err
		}
		var aus [][]byte
		if rr != nil {
			aus, err = e.EncodeStereo(src, srcR)
		} else {
			aus, err = e.Encode(src)
		}
		if err != nil {
			return fmt.Errorf("picture %d: %w", n, err)
		}
		if err := write(aus); err != nil {
			return err
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
	if err := write(aus); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	el := time.Since(start).Seconds()
	st := e.Stats()
	fmt.Fprintf(os.Stderr, "\r%d pictures in %.1f s (%.2f fps), %d bytes, PSNR Y %.3f Cb %.3f Cr %.3f dB\n",
		st.Pictures, el, float64(st.Pictures)/el, st.Bytes, st.PSNR(0, depth), st.PSNR(1, depth), st.PSNR(2, depth))
	if views == 2 {
		fmt.Fprintf(os.Stderr, "base layer %d bytes, second view %d bytes\n", layerBytes[0], layerBytes[1])
	}
	return nil
}

// countLayers adds an access unit's NAL unit sizes to each layer's count.
func countLayers(au []byte, n *[2]int64) {
	layer, start := -1, 0
	for i := 0; i+3 <= len(au); i++ {
		if au[i] == 0 && au[i+1] == 0 && au[i+2] == 1 && i+4 < len(au) {
			if layer >= 0 {
				n[layer] += int64(i - start)
			}
			layer = int(au[i+3]&1)<<5 | int(au[i+4]>>3)
			layer = min(layer, 1)
			start = i
		}
	}
	if layer >= 0 {
		n[layer] += int64(len(au) - start)
	}
}
