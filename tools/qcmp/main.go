// Command qcmp measures encodes against their source, picture by picture:
//
//	qcmp [-vmaf] SRC OUT...
//
// prints, for each output, its video's size and its PSNR against SRC (and
// with -vmaf, VMAF: the mean, the 1st percentile of pictures and the
// worst, from Netflix's vmaf tool, which must be on the PATH or in
// $QCMP_VMAF; the 4K model for pictures above 1080 lines). SRC is a
// Matroska file (a Dolby Vision profile 7 FEL source's layers composed,
// as an output's are), or an MVC Blu-ray stream (.m2ts), decoded by mvcdec
// side by side. Every stream is lined up by its end: a clip cut from a
// film drops its first pictures (they refer to what was cut away), and
// streams do not drop the same number.
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brunoga/bdtools/internal/dovi"
	"github.com/brunoga/bdtools/internal/mkv"
)

type stream struct {
	name             string
	blBytes, elBytes int64
	w, h             int
	next             func() (*dovi.Picture, error)
	frames           int
	seY              float64
	n                int
	// VMAF: the pictures go to vmaf through two pipes.
	ref, dist *bufio.Writer
	vmaf      *exec.Cmd
	vmafOut   string
}

func main() {
	useVMAF := flag.Bool("vmaf", false, "measure VMAF too")
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: qcmp [-vmaf] SRC OUT...")
		os.Exit(2)
	}
	tmp, err := os.MkdirTemp(os.Getenv("QCMP_TMP"), "qcmp")
	check(err)
	defer os.RemoveAll(tmp) //nolint:errcheck // best effort
	var ss []*stream
	for i, p := range flag.Args() {
		s, err := open(p, filepath.Join(tmp, fmt.Sprint(i)))
		check(err)
		ss = append(ss, s)
	}
	ref, outs := ss[0], ss[1:]
	n := ref.frames
	for _, o := range outs {
		n = min(n, o.frames)
		if o.w != ref.w || o.h != ref.h {
			check(fmt.Errorf("%s is %dx%d, the source %dx%d", o.name, o.w, o.h, ref.w, ref.h))
		}
	}
	for _, o := range ss {
		check(skip(o.next, o.frames-n))
	}
	if *useVMAF {
		for i, o := range outs {
			check(o.startVMAF(filepath.Join(tmp, fmt.Sprintf("vmaf%d", i)), ref.w, ref.h, max(2, 24/len(outs))))
		}
	}
	fmt.Fprintf(os.Stderr, "comparing %d pictures\n", n)
	for k := range n {
		r, err := ref.next()
		check(err)
		var refY4M []byte
		if *useVMAF {
			refY4M = y4mFrame(r)
		}
		var wg sync.WaitGroup
		for _, o := range outs {
			wg.Go(func() {
				p, err := o.next()
				check(err)
				o.seY += se(p.Y, r.Y)
				o.n++
				if o.vmaf != nil {
					// vmaf reads the two pipes by turns, within a frame.
					var both sync.WaitGroup
					both.Go(func() {
						_, err := o.ref.Write(refY4M)
						check(err)
					})
					_, err := o.dist.Write(y4mFrame(p))
					check(err)
					both.Wait()
				}
			})
		}
		wg.Wait()
		if (k+1)%240 == 0 {
			fmt.Fprintf(os.Stderr, "%d pictures\n", k+1)
		}
	}
	ys := ref.w * ref.h
	fmt.Printf("%-36s %8s %8s %7s %6s %6s %6s %6s\n", "output", "BL MB", "EL MB", "Y dB", "VMAF", "1%", "min", "frames")
	for _, o := range outs {
		mean, p1, worst := math.NaN(), math.NaN(), math.NaN()
		if o.vmaf != nil {
			mean, p1, worst, err = o.finishVMAF()
			check(err)
		}
		fmt.Printf("%-36s %8.1f %8.1f %7.2f %6.2f %6.2f %6.2f %6d\n", o.name, float64(o.blBytes)/1e6, float64(o.elBytes)/1e6,
			psnr(o.seY/float64(o.n), ys), mean, p1, worst, o.n)
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "qcmp:", err)
		os.Exit(1)
	}
}

// se is the squared error between two P010 planes.
func se(a, b []byte) float64 {
	var s int64
	for i := 0; i+1 < len(a); i += 2 {
		d := int64(binary.LittleEndian.Uint16(a[i:])>>6) - int64(binary.LittleEndian.Uint16(b[i:])>>6)
		s += d * d
	}
	return float64(s)
}

func psnr(se float64, n int) float64 {
	return 10 * math.Log10(1023*1023*float64(n)/max(se, 1e-9))
}

// y4mFrame is a P010 picture as a 10-bit YUV4MPEG2 frame: planar, the
// values in each sample's low bits.
func y4mFrame(p *dovi.Picture) []byte {
	w, h := p.Width, p.Height
	out := make([]byte, 6+w*h*3)
	copy(out, "FRAME\n")
	y, u, v := out[6:6+w*h*2], out[6+w*h*2:6+w*h*2+w*h/2], out[6+w*h*2+w*h/2:]
	for i := 0; i+1 < len(y); i += 2 {
		s := binary.LittleEndian.Uint16(p.Y[i:]) >> 6
		binary.LittleEndian.PutUint16(y[i:], s)
	}
	for i := 0; i+3 < len(p.UV); i += 4 {
		binary.LittleEndian.PutUint16(u[i/2:], binary.LittleEndian.Uint16(p.UV[i:])>>6)
		binary.LittleEndian.PutUint16(v[i/2:], binary.LittleEndian.Uint16(p.UV[i+2:])>>6)
	}
	return out
}

// startVMAF starts vmaf reading the reference and this output from pipes.
func (s *stream) startVMAF(dir string, w, h, threads int) error {
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // under our temporary directory
		return err
	}
	refPath, distPath := filepath.Join(dir, "ref.y4m"), filepath.Join(dir, "dist.y4m")
	for _, p := range []string{refPath, distPath} {
		if err := mkfifo(p); err != nil {
			return err
		}
	}
	model := "version=vmaf_v0.6.1"
	if h > 1080 {
		model = "version=vmaf_4k_v0.6.1"
	}
	bin := cmpOr(os.Getenv("QCMP_VMAF"), "vmaf")
	s.vmafOut = filepath.Join(dir, "out.json")
	s.vmaf = exec.CommandContext(context.Background(), bin, "-r", refPath, "-d", distPath, //nolint:gosec // the vmaf named
		"--model", model, "--threads", fmt.Sprint(threads), "--json", "-o", s.vmafOut, "-q")
	s.vmaf.Stderr = os.Stderr
	if err := s.vmaf.Start(); err != nil {
		return fmt.Errorf("starting vmaf: %w", err)
	}
	header := fmt.Sprintf("YUV4MPEG2 W%d H%d F24000:1001 Ip A1:1 C420p10 XYSCSS=420P10\n", w, h)
	open := func(p string) (*bufio.Writer, error) {
		f, err := os.OpenFile(p, os.O_WRONLY, 0) //nolint:gosec // our pipe
		if err != nil {
			return nil, err
		}
		b := bufio.NewWriterSize(f, 1<<22)
		fifoWriters.Store(b, f)
		if _, err := b.WriteString(header); err != nil {
			return nil, err
		}
		return b, b.Flush()
	}
	// vmaf opens the reference first.
	var err error
	if s.ref, err = open(refPath); err != nil {
		return err
	}
	s.dist, err = open(distPath)
	return err
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// finishVMAF ends vmaf's input and gives its scores.
func (s *stream) finishVMAF() (mean, p1, worst float64, err error) {
	for _, w := range []*bufio.Writer{s.ref, s.dist} {
		if err := w.Flush(); err != nil {
			return 0, 0, 0, err
		}
	}
	// The pipes close with the process: close them through their files.
	closeFIFO(s.ref)
	closeFIFO(s.dist)
	if err := s.vmaf.Wait(); err != nil {
		return 0, 0, 0, fmt.Errorf("vmaf: %w", err)
	}
	b, err := os.ReadFile(s.vmafOut) //nolint:gosec // under our temporary directory
	if err != nil {
		return 0, 0, 0, err
	}
	var out struct {
		Frames []struct {
			Metrics map[string]float64 `json:"metrics"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return 0, 0, 0, err
	}
	var v []float64
	for _, f := range out.Frames {
		v = append(v, f.Metrics["vmaf"])
	}
	if len(v) == 0 {
		return 0, 0, 0, errors.New("vmaf gave no frames")
	}
	sort.Float64s(v)
	var sum float64
	for _, x := range v {
		sum += x
	}
	return sum / float64(len(v)), v[len(v)/100], v[0], nil
}

// fifoWriters remembers each pipe's file, for closing.
var fifoWriters sync.Map

func closeFIFO(w *bufio.Writer) {
	if f, ok := fifoWriters.Load(w); ok {
		_ = f.(*os.File).Close()
	}
}

// open reads a source or an output: a Matroska file (two Dolby Vision
// layers composed), or an MVC stream decoded side by side.
func open(path, tmp string) (*stream, error) {
	if ext := strings.ToLower(filepath.Ext(path)); ext == ".m2ts" || ext == ".ts" {
		return openMVC(path)
	}
	f, err := os.Open(path) //nolint:gosec // the file named
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read only
	r, err := mkv.NewReader(f)
	if err != nil {
		return nil, err
	}
	var tr *mkv.ReadTrack
	for i := range r.Tracks {
		if r.Tracks[i].Type == mkv.TypeVideo {
			tr = &r.Tracks[i]
			break
		}
	}
	if tr == nil {
		return nil, fmt.Errorf("%s: no video", path)
	}
	s := &stream{name: filepath.Base(path), w: tr.Width, h: tr.Height}
	hevc := tr.CodecID == "V_MPEGH/ISO/HEVC"
	if err := os.MkdirAll(tmp, 0o700); err != nil { //nolint:gosec // under our temporary directory
		return nil, err
	}
	blPath, elPath := filepath.Join(tmp, "bl.hevc"), filepath.Join(tmp, "el.hevc")
	var bl, el *esWriter
	if hevc {
		if bl, err = createES(blPath); err != nil {
			return nil, err
		}
		if el, err = createES(elPath); err != nil {
			_ = bl.close()
			return nil, err
		}
		writeParams(bl, tr.CodecPrivate)
	}
	type rpu struct {
		at  time.Duration
		nal []byte
	}
	var rpus []rpu
	for {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if p.Track != tr.Number {
			continue
		}
		if !hevc {
			s.blBytes += int64(len(p.Data))
			continue
		}
		for b := p.Data; len(b) >= 4; {
			n := int(binary.BigEndian.Uint32(b))
			if n < 1 || n > len(b)-4 {
				break
			}
			nal := b[4 : 4+n]
			switch nal[0] >> 1 & 0x3f {
			case dovi.NALEL:
				el.nal(nal[2:])
				s.elBytes += int64(n + 4)
			case dovi.NALRPU:
				rpus = append(rpus, rpu{p.Time, append([]byte(nil), nal...)})
			default:
				bl.nal(nal)
				s.blBytes += int64(n + 4)
			}
			b = b[4+n:]
		}
	}
	if !hevc || s.elBytes == 0 {
		if hevc {
			_, _ = bl.close(), el.close()
		}
		next, n, err := decode(ffmpeg(path), s.w, s.h)
		if err != nil {
			return nil, err
		}
		s.next, s.frames = next, n
		return s, nil
	}
	if err := bl.close(); err != nil {
		return nil, err
	}
	if err := el.close(); err != nil {
		return nil, err
	}
	sort.SliceStable(rpus, func(i, j int) bool { return rpus[i].at < rpus[j].at })
	blNext, nb, err := decode(ffmpeg(blPath), s.w, s.h)
	if err != nil {
		return nil, err
	}
	elNext, ne, err := decode(ffmpeg(elPath), s.w/2, s.h/2)
	if err != nil {
		return nil, err
	}
	// Each layer's dropped pictures are its first; the RPUs are all there.
	s.frames = min(nb, ne, len(rpus))
	if err := skip(blNext, nb-s.frames); err != nil {
		return nil, err
	}
	if err := skip(elNext, ne-s.frames); err != nil {
		return nil, err
	}
	i := len(rpus) - s.frames
	s.next = func() (*dovi.Picture, error) {
		b, err := blNext()
		if err != nil {
			return nil, err
		}
		e, err := elNext()
		if err != nil {
			return nil, err
		}
		if i >= len(rpus) {
			return nil, io.EOF
		}
		u, err := dovi.ParseNAL(rpus[i].nal)
		i++
		if err != nil {
			return nil, err
		}
		c, err := dovi.NewComposer(u)
		if err != nil {
			return nil, err
		}
		out := &dovi.Picture{Width: s.w, Height: s.h, Y: make([]byte, s.w*s.h*2), UV: make([]byte, s.w*s.h), Pitch: 2 * s.w}
		if err := c.Compose(out, b, e); err != nil {
			return nil, err
		}
		return out, nil
	}
	return s, nil
}

// openMVC decodes an MVC stream's two views side by side.
func openMVC(path string) (*stream, error) {
	cmd := func() *exec.Cmd {
		return exec.CommandContext(context.Background(), "sh", "-c", //nolint:gosec // our own pipeline
			`mvcdec -y4m - -layout sbs "$1" 2>/dev/null | ffmpeg -v quiet -f yuv4mpegpipe -i - -pix_fmt p010le -f rawvideo -`, "sh", path)
	}
	// The size from mvcdec's header (not -n 1: a cut clip's first access
	// units fail to decode, and count).
	probe := exec.CommandContext(context.Background(), "sh", "-c", `mvcdec -y4m - -layout sbs "$1" 2>/dev/null | head -1`, "sh", path) //nolint:gosec // ours
	hdr, err := probe.Output()
	if err != nil && len(hdr) == 0 {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var w, h int
	for _, f := range strings.Fields(strings.SplitN(string(hdr), "\n", 2)[0]) {
		switch f[0] {
		case 'W':
			_, _ = fmt.Sscan(f[1:], &w)
		case 'H':
			_, _ = fmt.Sscan(f[1:], &h)
		}
	}
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("%s: no picture size from mvcdec", path)
	}
	s := &stream{name: filepath.Base(path), w: w, h: h}
	next, n, err := decode(cmd, w, h)
	if err != nil {
		return nil, err
	}
	s.next, s.frames = next, n
	return s, nil
}

// esWriter writes an elementary stream's NAL units, keeping the first error.
type esWriter struct {
	f   *os.File
	w   *bufio.Writer
	err error
}

func createES(path string) (*esWriter, error) {
	f, err := os.Create(path) //nolint:gosec // our temporary directory
	if err != nil {
		return nil, err
	}
	return &esWriter{f: f, w: bufio.NewWriterSize(f, 1<<22)}, nil
}

func (e *esWriter) nal(b []byte) {
	if e.err == nil {
		_, e.err = e.w.Write([]byte{0, 0, 0, 1})
	}
	if e.err == nil {
		_, e.err = e.w.Write(b)
	}
}

func (e *esWriter) close() error {
	if e.err == nil {
		e.err = e.w.Flush()
	}
	if err := e.f.Close(); e.err == nil {
		e.err = err
	}
	return e.err
}

// writeParams writes the parameter sets an hvcC record holds.
func writeParams(e *esWriter, cp []byte) {
	if len(cp) <= 23 {
		return
	}
	b := cp[23:]
	for range int(cp[22]) {
		if len(b) < 3 {
			return
		}
		n := int(binary.BigEndian.Uint16(b[1:]))
		b = b[3:]
		for range n {
			if len(b) < 2 || len(b) < 2+int(binary.BigEndian.Uint16(b)) {
				return
			}
			l := int(binary.BigEndian.Uint16(b))
			e.nal(b[2 : 2+l])
			b = b[2+l:]
		}
	}
}

// ffmpeg decodes a file to P010 on its standard output, quietly: a cut
// clip's first pictures fail to decode, and are left out.
func ffmpeg(path string) func() *exec.Cmd {
	return func() *exec.Cmd {
		return exec.CommandContext(context.Background(), "ffmpeg", "-v", "quiet", "-hwaccel", "auto", "-i", path, //nolint:gosec // our own files
			"-pix_fmt", "p010le", "-f", "rawvideo", "-")
	}
}

// decode gives a decoding command's pictures, in display order, and how
// many there are (from a first pass).
func decode(cmd func() *exec.Cmd, w, h int) (func() (*dovi.Picture, error), int, error) {
	size := w * h * 3
	c := cmd()
	out, err := c.StdoutPipe()
	if err != nil {
		return nil, 0, err
	}
	if err := c.Start(); err != nil {
		return nil, 0, err
	}
	total, err := io.Copy(io.Discard, out)
	if werr := c.Wait(); err == nil {
		err = werr
	}
	if err != nil && total == 0 {
		return nil, 0, err
	}
	c = cmd()
	if out, err = c.StdoutPipe(); err != nil {
		return nil, 0, err
	}
	if err := c.Start(); err != nil {
		return nil, 0, err
	}
	r := bufio.NewReaderSize(out, 1<<24)
	return func() (*dovi.Picture, error) {
		b := make([]byte, size)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		return &dovi.Picture{Width: w, Height: h, Y: b[:w*h*2], UV: b[w*h*2:], Pitch: 2 * w}, nil
	}, int(total) / size, nil
}

// skip drops a stream's next n pictures.
func skip(next func() (*dovi.Picture, error), n int) error {
	for range n {
		if _, err := next(); err != nil {
			return err
		}
	}
	return nil
}
