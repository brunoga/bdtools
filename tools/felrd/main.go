// Command felrd measures how near encodes of a Dolby Vision profile 7 FEL
// source come to the picture the source's layers compose to:
//
//	felrd SRC.mkv OUT.mkv...
//
// prints, for each output, its video's size (base layer, enhancement
// layer) and its luma and chroma PSNR against SRC's composition over every
// picture. A profile 7 output's layers are composed the same way first;
// any other output is compared as it decodes. SRC may be a clip cut from a
// film: the pictures each stream drops at its start (referring to what was
// cut away) are skipped, every stream lined up by its end. It decodes
// through ffmpeg, with the GPU's decoder when there is one; $FELRD_TMP is
// where the layers are written apart (the system's temporary directory by
// default).
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/brunoga/bdtools/internal/dovi"
	"github.com/brunoga/bdtools/internal/mkv"
)

// stream is a file's pictures, composed when it has two layers.
type stream struct {
	name             string
	blBytes, elBytes int64
	w, h             int
	next             func() (*dovi.Picture, error)
	frames           int // pictures left to read
	seY, seC         float64
	n                int
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: felrd SRC.mkv OUT.mkv...")
		os.Exit(2)
	}
	tmp, err := os.MkdirTemp(os.Getenv("FELRD_TMP"), "felrd")
	check(err)
	defer os.RemoveAll(tmp) //nolint:errcheck // best effort
	var ss []*stream
	for i, p := range os.Args[1:] {
		s, err := open(p, filepath.Join(tmp, fmt.Sprint(i)))
		check(err)
		ss = append(ss, s)
	}
	ref, outs := ss[0], ss[1:]
	n := ref.frames
	for _, o := range outs {
		n = min(n, o.frames)
	}
	for _, o := range ss {
		check(skip(o.next, o.frames-n))
	}
	fmt.Fprintf(os.Stderr, "comparing %d pictures\n", n)
	for k := range n {
		r, err := ref.next()
		check(err)
		var wg sync.WaitGroup
		for _, o := range outs {
			wg.Go(func() {
				p, err := o.next()
				check(err)
				o.seY += se(p.Y, r.Y)
				o.seC += se(p.UV, r.UV)
				o.n++
			})
		}
		wg.Wait()
		if (k+1)%240 == 0 {
			fmt.Fprintf(os.Stderr, "%d pictures\n", k+1)
		}
	}
	ys := ref.w * ref.h
	fmt.Printf("%-40s %9s %9s %7s %7s %6s\n", "output", "BL MB", "EL MB", "Y dB", "C dB", "frames")
	for _, o := range outs {
		fmt.Printf("%-40s %9.1f %9.1f %7.2f %7.2f %6d\n", o.name, float64(o.blBytes)/1e6, float64(o.elBytes)/1e6,
			psnr(o.seY/float64(o.n), ys), psnr(o.seC/float64(o.n), ys/2), o.n)
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "felrd:", err)
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

// open reads a file's video track: a layered one's layers go to files of
// their own, to be decoded and composed; any other is decoded whole.
func open(path, tmp string) (*stream, error) {
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
	if err := os.MkdirAll(tmp, 0o700); err != nil { //nolint:gosec // under our temporary directory
		return nil, err
	}
	blPath, elPath := filepath.Join(tmp, "bl.hevc"), filepath.Join(tmp, "el.hevc")
	bl, err := createES(blPath)
	if err != nil {
		return nil, err
	}
	el, err := createES(elPath)
	if err != nil {
		_ = bl.close()
		return nil, err
	}
	// The parameter sets in hvcC first.
	if cp := tr.CodecPrivate; len(cp) > 23 {
		b := cp[23:]
		for range int(cp[22]) {
			if len(b) < 3 {
				break
			}
			n := int(binary.BigEndian.Uint16(b[1:]))
			b = b[3:]
			for range n {
				if len(b) < 2 || len(b) < 2+int(binary.BigEndian.Uint16(b)) {
					break
				}
				l := int(binary.BigEndian.Uint16(b))
				bl.nal(b[2 : 2+l])
				b = b[2+l:]
			}
		}
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
			_, _ = bl.close(), el.close()
			return nil, err
		}
		if p.Track != tr.Number {
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
	if err := bl.close(); err != nil {
		return nil, err
	}
	if err := el.close(); err != nil {
		return nil, err
	}
	sort.SliceStable(rpus, func(i, j int) bool { return rpus[i].at < rpus[j].at })
	blNext, nb, err := decode(blPath, s.w, s.h)
	if err != nil {
		return nil, err
	}
	if s.elBytes == 0 {
		s.next, s.frames = blNext, nb
		return s, nil
	}
	elNext, ne, err := decode(elPath, s.w/2, s.h/2)
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

// ffmpeg decodes a stream to P010 on its standard output, quietly: a cut
// clip's first pictures fail to decode, and are left out.
func ffmpeg(path string) *exec.Cmd {
	return exec.CommandContext(context.Background(), "ffmpeg", "-v", "quiet", "-hwaccel", "auto", "-i", path, //nolint:gosec // our own files
		"-pix_fmt", "p010le", "-f", "rawvideo", "-")
}

// decode gives a stream's pictures, in display order, as P010, and how
// many there are.
func decode(path string, w, h int) (func() (*dovi.Picture, error), int, error) {
	n, err := count(path, w*h*3)
	if err != nil {
		return nil, 0, err
	}
	cmd := ffmpeg(path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, 0, err
	}
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	r := bufio.NewReaderSize(out, 1<<24)
	return func() (*dovi.Picture, error) {
		b := make([]byte, w*h*3)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		return &dovi.Picture{Width: w, Height: h, Y: b[:w*h*2], UV: b[w*h*2:], Pitch: 2 * w}, nil
	}, n, nil
}

// count is how many pictures of size bytes a stream decodes to.
func count(path string, size int) (int, error) {
	cmd := ffmpeg(path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	n, err := io.Copy(io.Discard, out)
	if werr := cmd.Wait(); err == nil {
		err = werr
	}
	return int(n) / size, err
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
