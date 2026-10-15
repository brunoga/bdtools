// Command mvcenc adds a dependent view to an H.264 base view stream,
// writing an H.264 MVC (Stereo High) stream.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime/pprof"
	"time"

	"github.com/brunoga/bdtools/internal/mkv"
	"github.com/brunoga/bdtools/mvc"
)

func main() {
	out := flag.String("o", "", "output MVC Annex B stream")
	qp := flag.Int("qp", 26, "dependent view quantiser")
	dep := flag.String("dep", "", "dependent view source: raw 4:2:0 8-bit YUV in display order (without it the dependent view repeats the base view)")
	width := flag.Int("w", 1920, "source width")
	height := flag.Int("h", 1080, "source height")
	mkvOut := flag.String("mkv", "", "also write the stream into this Matroska file, both views in each block")
	mvcC := flag.Bool("mvcc", false, "with -mkv: describe the dependent view in an mvcC BlockAdditionMapping")
	interView := flag.Bool("interview-only", false, "predict the dependent view from the base view only")
	cpuProfile := flag.String("cpuprofile", "", "write a CPU profile to this file")
	noB := flag.Bool("no-b", false, "code the dependent view as P slices only (no prediction from two references)")
	slices := flag.Int("slices", 0, "dependent view slices per picture (0: the default)")
	split := flag.String("split", "", "also write the views as Blu-ray style elementary streams: PREFIX.264 and PREFIX.mvc (for tsMuxeR)")
	base := flag.String("base", "", "encode the base view too, from this raw 4:2:0 8-bit YUV in display order (no base.264 then)")
	bqp := flag.Int("bqp", 22, "with -base: the base view's P picture quantiser")
	keyint := flag.Int("keyint", 250, "with -base: frames between IDR pictures")
	bframes := flag.Int("bframes", 3, "with -base: B pictures between references")
	fps := flag.String("fps", "24000/1001", "with -base: the frame rate, for the VUI")
	bslices := flag.Int("bslices", 0, "with -base: base view slices per picture (0: the default)")
	wavefront := flag.Bool("wavefront", true, "decide a slice's macroblock rows in parallel (off: one worker per slice)")
	aq := flag.Float64("aq", 0, "adaptive quantisation strength, both views (0: off)")
	aqAbs := flag.Bool("aq-absolute", false, "offset the quantiser from a fixed energy (x264's), not the picture's mean")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: mvcenc [flags] -o out.264 base.264\n       mvcenc [flags] -base left.yuv -dep right.yuv -o out.264\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	mvc.SetWavefront(*wavefront)
	mvc.AQAbsolute = *aqAbs
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile) //nolint:gosec // a file the user named
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal(err)
		}
		defer pprof.StopCPUProfile()
	}
	if *out == "" || (*base == "") != (flag.NArg() == 1) {
		flag.Usage()
		os.Exit(2)
	}
	if *base != "" {
		if *dep == "" {
			log.Fatal("-base needs -dep")
		}
		var num, den int
		if _, err := fmt.Sscanf(*fps, "%d/%d", &num, &den); err != nil {
			log.Fatalf("-fps %q: want NUM/DEN", *fps)
		}
		encodeBoth(*base, *dep, *out, *width, *height, mvc.BaseOptions{QP: *bqp, Keyint: *keyint, BFrames: *bframes, Slices: *bslices, FPSNum: num, FPSDen: den, AQ: *aq},
			*qp, *slices, *noB, *mkvOut, *mvcC, *split)
		return
	}
	var order []int64 // decode index of each picture in display order
	var src *os.File
	if *dep != "" {
		var err error
		if order, err = displayOrder(flag.Arg(0)); err != nil {
			log.Fatal(err)
		}
		if src, err = os.Open(*dep); err != nil {
			log.Fatal(err)
		}
		defer func() { _ = src.Close() }()
	}
	// display index of each decode index
	disp := make(map[int64]int, len(order))
	for i, d := range order {
		disp[d] = i
	}

	in, err := os.Open(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	r := mvc.NewAUReader(in)
	dw := mvc.NewDepWriter(*qp)
	dw.AQ = *aq
	dw.InterViewOnly = *interView
	dw.Slices = *slices
	dw.NoBSlices = *noB
	w, h := *width, *height
	cw, ch := (w+15)&^15, (h+15)&^15
	frame := make([]byte, w*h*3/2)
	ds := &mvc.DepSource{Y: make([]byte, cw*ch), Cb: make([]byte, cw*ch/4), Cr: make([]byte, cw*ch/4), StrideY: cw, StrideC: cw / 2}
	start := time.Now()
	n := 0
	for {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		var s *mvc.DepSource
		if src != nil && hasPicture(au) {
			di, ok := disp[int64(n)]
			if !ok {
				log.Fatalf("access unit %d: no display position", n)
			}
			if _, err := src.ReadAt(frame, int64(di)*int64(len(frame))); err != nil {
				log.Fatalf("dependent view frame %d: %v", di, err)
			}
			fill(ds, frame, w, h)
			s = ds
		}
		mau, err := dw.AccessUnit(au, s)
		if err != nil {
			log.Fatalf("access unit %d: %v", n, err)
		}
		if _, err := bw.Write(mau); err != nil {
			log.Fatal(err)
		}
		n++
		if n%50 == 0 {
			fmt.Fprintf(os.Stderr, "\r%d access units, %.1f/s", n, float64(n)/time.Since(start).Seconds())
		}
	}
	if err := bw.Flush(); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	if *mkvOut != "" {
		if err := writeMKV(*out, *mkvOut, *mvcC); err != nil {
			log.Fatal(err)
		}
	}
	if *split != "" {
		if err := splitViews(*out, *split); err != nil {
			log.Fatal(err)
		}
	}
	st := dw.Stats()
	fmt.Fprintf(os.Stderr, "\r%d access units in %.1fs; dependent view macroblocks: %d skipped, %d direct, %d from the base view, %d temporal, %d from two references\n",
		n, time.Since(start).Seconds(), st.Skip, st.Direct, st.InterView, st.Temporal, st.Bi)
}

// encodeBoth encodes both views from raw frames: the base view with the
// encoder here, each of its access units then getting its dependent view.
func encodeBoth(basePath, depPath, outPath string, w, h int, bo mvc.BaseOptions, depQP, slices int, noB bool, mkvOut string, mvcC bool, split string) {
	bo.Width, bo.Height = w, h
	enc, err := mvc.NewBaseEncoder(bo)
	if err != nil {
		log.Fatal(err)
	}
	bf, err := os.Open(basePath) //nolint:gosec // a file the user named
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = bf.Close() }()
	df, err := os.Open(depPath) //nolint:gosec // a file the user named
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = df.Close() }()
	f, err := os.Create(outPath) //nolint:gosec // a file the user named
	if err != nil {
		log.Fatal(err)
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	dw := mvc.NewDepWriter(depQP)
	dw.AQ = bo.AQ
	dw.Slices = slices
	dw.NoBSlices = noB
	cw, ch := (w+15)&^15, (h+15)&^15
	frameSize := w * h * 3 / 2
	frame := make([]byte, frameSize)
	depFrame := make([]byte, frameSize)
	newSource := func() *mvc.DepSource {
		return &mvc.DepSource{Y: make([]byte, cw*ch), Cb: make([]byte, cw*ch/4), Cr: make([]byte, cw*ch/4), StrideY: cw, StrideC: cw / 2}
	}
	start := time.Now()
	coded := 0
	// pictures picked so far, in decode order: the writer asks for them
	// as it starts coding, ahead of what it has finished
	picked, cvsStart := 0, 0
	pick := func(poc int32, idr bool) (*mvc.DepSource, error) {
		if idr {
			cvsStart = picked
		}
		picked++
		i := cvsStart + int(poc/2)
		if _, err := df.ReadAt(depFrame, int64(i)*int64(frameSize)); err != nil {
			return nil, fmt.Errorf("dependent view frame %d: %w", i, err)
		}
		depSrc := newSource() // the writer holds it while the picture is coded
		fill(depSrc, depFrame, w, h)
		return depSrc, nil
	}
	// The dependent view is coded behind the base view's encoder, as
	// bdtools does, so both run at once.
	queue := make(chan []byte, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		emit := func(maus [][]byte) {
			for _, mau := range maus {
				coded++
				if _, err := bw.Write(mau); err != nil {
					log.Fatal(err)
				}
				if coded%50 == 0 {
					fmt.Fprintf(os.Stderr, "\r%d access units, %.1f/s", coded, float64(coded)/time.Since(start).Seconds())
				}
			}
		}
		for au := range queue {
			maus, err := dw.Submit(au, pick)
			if err != nil {
				log.Fatalf("access unit %d: %v", coded, err)
			}
			emit(maus)
		}
		maus, err := dw.Flush()
		if err != nil {
			log.Fatal(err)
		}
		emit(maus)
	}()
	write := func(aus [][]byte) {
		for _, au := range aus {
			queue <- au
		}
	}
	for n := 0; ; n++ {
		if _, err := io.ReadFull(bf, frame); err != nil {
			if err == io.EOF {
				break
			}
			log.Fatalf("base view frame %d: %v", n, err)
		}
		src := newSource() // the encoder holds it until the frame is coded
		fill(src, frame, w, h)
		aus, err := enc.Encode(src, int64(n))
		if err != nil {
			log.Fatal(err)
		}
		write(aus)
	}
	aus, err := enc.Flush()
	if err != nil {
		log.Fatal(err)
	}
	write(aus)
	close(queue)
	<-done
	if err := bw.Flush(); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	if mkvOut != "" {
		if err := writeMKV(outPath, mkvOut, mvcC); err != nil {
			log.Fatal(err)
		}
	}
	if split != "" {
		if err := splitViews(outPath, split); err != nil {
			log.Fatal(err)
		}
	}
	bs, ds := enc.Stats(), dw.Stats()
	fmt.Fprintf(os.Stderr, "\r%d access units in %.1fs; base view macroblocks: %d skipped, %d direct, %d temporal, %d from two references, %d partitioned, %d intra; dependent view: %d skipped, %d direct, %d from the base view, %d temporal, %d from two references, %d partitioned, %d intra\n",
		coded, time.Since(start).Seconds(), bs.Skip, bs.Direct, bs.Temporal, bs.Bi, bs.Partitioned, bs.Intra,
		ds.Skip, ds.Direct, ds.InterView, ds.Temporal, ds.Bi, ds.Partitioned, ds.Intra)
}

// fill copies a w x h frame into the coded-size source, repeating the
// last row and column into the padding.
func fill(ds *mvc.DepSource, frame []byte, w, h int) {
	planes := [3][]byte{ds.Y, ds.Cb, ds.Cr}
	strides := [3]int{ds.StrideY, ds.StrideC, ds.StrideC}
	off := 0
	for c := 0; c < 3; c++ {
		pw, ph := w, h
		if c > 0 {
			pw, ph = w/2, h/2
		}
		st := strides[c]
		ch := len(planes[c]) / st
		for y := 0; y < ch; y++ {
			sy := min(y, ph-1)
			row := planes[c][y*st : y*st+st]
			copy(row, frame[off+sy*pw:off+sy*pw+pw])
			for x := pw; x < st; x++ {
				row[x] = row[pw-1]
			}
		}
		off += pw * ph
	}
}

// hasPicture reports whether an access unit holds a coded picture.
func hasPicture(au []byte) bool {
	for i := 0; i+3 < len(au); i++ {
		if au[i] == 0 && au[i+1] == 0 && au[i+2] == 1 {
			if t := au[i+3] & 31; t == 1 || t == 5 {
				return true
			}
		}
	}
	return false
}

// displayOrder decodes the base view and returns the decode index of each
// picture in output order.
func displayOrder(path string) ([]int64, error) {
	in, err := os.Open(path) //nolint:gosec // a file the user named
	if err != nil {
		return nil, err
	}
	defer func() { _ = in.Close() }()
	r := mvc.NewAUReader(in)
	d := mvc.NewDecoder(mvc.Options{BaseOnly: true})
	var order []int64
	drain := func() {
		for {
			f, ok := d.WaitFrame()
			if !ok {
				return
			}
			order = append(order, f.Base.PTS)
			f.Release()
		}
	}
	n := int64(0)
	for {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if !hasPicture(au) {
			continue
		}
		if err := d.DecodeAU(au, n); err != nil {
			return nil, err
		}
		n++
		drain()
	}
	if err := d.Flush(); err != nil {
		return nil, err
	}
	drain()
	return order, nil
}

// nals splits an Annex B buffer into NAL units without start codes.
func nals(b []byte) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				end := i
				for end > start && b[end-1] == 0 {
					end--
				}
				out = append(out, b[start:end])
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 && start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// ppsID reads pic_parameter_set_id, the first field of a PPS (small ids:
// its Exp-Golomb code fits the first byte after the header).
func ppsID(nal []byte) int {
	if len(nal) < 2 {
		return -1
	}
	b := nal[1]
	z := 0
	for z < 4 && b&(0x80>>z) == 0 {
		z++
	}
	if z >= 4 {
		return -1
	}
	v := int(b>>(7-2*z)) & (1<<(z+1) - 1)
	return v - 1
}

// mvcParams returns the stream's subset SPS and the PPS that slices of the
// dependent view use: the last PPS before the first dependent slice.
func mvcParams(stream []byte) (subset, pps []byte) {
	var lastPPS []byte
	for _, n := range nals(stream) {
		switch n[0] & 31 {
		case 15:
			if subset == nil {
				subset = n
			}
		case 8:
			lastPPS = n
		case 20:
			return subset, lastPPS
		}
	}
	return subset, lastPPS
}

// mvcCRecord builds an MVCDecoderConfigurationRecord (ISO/IEC 14496-15).
func mvcCRecord(subset, pps []byte) []byte {
	r := []byte{1, subset[1], subset[2], subset[3], 0xff, 0xe1}
	r = append(r, byte(len(subset)>>8), byte(len(subset)))
	r = append(r, subset...)
	r = append(r, 1, byte(len(pps)>>8), byte(len(pps)))
	return append(r, pps...)
}

func writeMKV(in, out string, withMvcC bool) error {
	b, err := os.ReadFile(in) //nolint:gosec // a file the user named
	if err != nil {
		return err
	}
	src, err := mkv.NewVideoSource(bytesReader(b), mkv.H264, 24000, 1001, 0)
	if err != nil {
		return err
	}
	if withMvcC {
		subset, pps := mvcParams(b)
		if subset == nil || pps == nil {
			return fmt.Errorf("no MVC parameter sets in %s", in)
		}
		src.SetBlockAdditions([]mkv.BlockAddition{{Type: mkv.FourCC("mvcC"), ExtraData: mvcCRecord(subset, pps)}})
	}
	f, err := os.Create(out) //nolint:gosec // a file the user named
	if err != nil {
		return err
	}
	if err := mkv.Mux(f, []mkv.Source{src}, mkv.Options{WritingApp: "mvcenc"}); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// splitViews writes the base view and the dependent view (its delimiters,
// the subset SPS and its PPS, and the coded slice extensions) as Blu-ray
// discs carry them.
func splitViews(in, prefix string) error {
	b, err := os.ReadFile(in) //nolint:gosec // a file the user named
	if err != nil {
		return err
	}
	_, depPPS := mvcParams(b)
	depID := ppsID(depPPS)
	var base, dep []byte
	sc := []byte{0, 0, 0, 1}
	for _, n := range nals(b) {
		switch t := n[0] & 31; {
		case t == 24 || t == 15 || t == 20 || t == 8 && ppsID(n) == depID:
			dep = append(append(dep, sc...), n...)
		default:
			base = append(append(base, sc...), n...)
		}
	}
	// A last delimiter, or tsMuxeR leaves out the last dependent picture.
	dep = append(append(dep, sc...), 0x18, 0x30)
	if err := os.WriteFile(prefix+".264", base, 0o644); err != nil { //nolint:gosec // output files
		return err
	}
	return os.WriteFile(prefix+".mvc", dep, 0o644) //nolint:gosec // output files
}

type byteReader struct {
	b []byte
}

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

func bytesReader(b []byte) io.Reader { return &byteReader{b} }
