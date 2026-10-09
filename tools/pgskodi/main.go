// Command pgskodi rewrites a PGS subtitle file (.sup) so that Kodi draws
// its wide subtitles whole in a side-by-side 3D film: each object wider
// than half the plane becomes two side by side. bdtools writes the flat
// subtitles of a 3D conversion so; this fixes files made before it did.
//
//	pgskodi in.sup out.sup
//
// It exits 0 having written out.sup, or 3 when nothing needed splitting
// (out.sup is not written).
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/brunoga/bdtools/internal/convert"
	"github.com/brunoga/bdtools/internal/mkv"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: pgskodi in.sup out.sup")
		os.Exit(2)
	}
	n, err := run(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgskodi:", err)
		os.Exit(1)
	}
	if n == 0 {
		fmt.Println("nothing to split")
		os.Exit(3)
	}
	fmt.Printf("%d display sets changed\n", n)
}

func run(in, out string) (int, error) {
	f, err := os.Open(in) //nolint:gosec // the file named
	if err != nil {
		return 0, err
	}
	defer f.Close() //nolint:errcheck // read only
	src := mkv.NewPGSSource(f, "")
	orig := &recorder{src: src}
	fixed := convert.KodiPGS(orig)
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	changed := 0
	for {
		fr, err := fixed.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		if !bytes.Equal(fr.Data, orig.last) {
			changed++
		}
		if err := writeSet(w, fr.PTS, fr.Data); err != nil {
			return 0, err
		}
	}
	if changed == 0 {
		return 0, nil
	}
	if err := w.Flush(); err != nil {
		return 0, err
	}
	return changed, os.WriteFile(out, buf.Bytes(), 0o644) //nolint:gosec // a subtitle file
}

// recorder keeps the last display set read, to tell what changed.
type recorder struct {
	src  mkv.Source
	last []byte
}

func (r *recorder) Track() mkv.Track { return r.src.Track() }

func (r *recorder) Next() (mkv.Frame, error) {
	f, err := r.src.Next()
	r.last = append(r.last[:0], f.Data...)
	return f, err
}

// writeSet writes a display set's segments, each with the .sup header
// (its presentation time; no decoding time).
func writeSet(w io.Writer, pts time.Duration, ds []byte) error {
	t := uint32(pts * 90000 / time.Second) //nolint:gosec // a 33-bit clock
	for len(ds) >= 3 {
		n := 3 + int(binary.BigEndian.Uint16(ds[1:]))
		var h [10]byte
		h[0], h[1] = 'P', 'G'
		binary.BigEndian.PutUint32(h[2:], t)
		if _, err := w.Write(h[:]); err != nil {
			return err
		}
		if _, err := w.Write(ds[:n]); err != nil {
			return err
		}
		ds = ds[n:]
	}
	return nil
}
