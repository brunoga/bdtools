package bdmv

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golift.io/udf"
)

// Disc is a BDMV directory tree, wherever it lives. Paths are relative to
// the BDMV directory, with forward slashes, and matched case-insensitively:
// a disc authored on a case-sensitive filesystem may use either case.
type Disc interface {
	// List returns the file names in a directory, in name order.
	List(dir string) ([]string, error)
	// ReadFile returns a whole (small) file.
	ReadFile(path string) ([]byte, error)
	// Open returns a reader over a stream file and its size.
	Open(path string) (io.ReadSeekCloser, int64, error)
	// Exists reports whether the file is there.
	Exists(path string) bool
	// Describe names the disc for messages.
	Describe() string
	// Close releases what the disc holds open: the image file Open opened.
	// A disc read from a caller's reader (OpenImage) leaves it open.
	Close() error
}

// Open opens a disc image (.iso), a BDMV directory, or the directory holding
// one.
func Open(path string) (Disc, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		if strings.EqualFold(filepath.Ext(path), ".iso") {
			return openISO(path)
		}
		return nil, fmt.Errorf("%s is neither a disc image nor a directory", path)
	}
	if strings.EqualFold(filepath.Base(path), "BDMV") {
		return &dirDisc{root: path}, nil
	}
	// The BDMV of a disc, or of an AVCHD recording (PRIVATE/AVCHD/BDMV).
	for _, sub := range [][]string{{"BDMV"}, {"PRIVATE", "AVCHD", "BDMV"}, {"AVCHD", "BDMV"}} {
		if p, ok := findDir(path, sub); ok {
			return &dirDisc{root: p}, nil
		}
	}
	return nil, fmt.Errorf("%s is a directory but holds no BDMV", path)
}

// findDir follows a path of directory names, case-insensitively.
func findDir(root string, parts []string) (string, bool) {
	cur := root
	for _, part := range parts {
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", false
		}
		next := ""
		for _, e := range entries {
			if e.IsDir() && strings.EqualFold(e.Name(), part) {
				next = filepath.Join(cur, e.Name())
				break
			}
		}
		if next == "" {
			return "", false
		}
		cur = next
	}
	return cur, true
}

// Playlists returns the playlist file names of a disc, in name order.
func Playlists(d Disc) ([]string, error) {
	names, err := d.List("PLAYLIST")
	if err != nil {
		return nil, fmt.Errorf("no playlists: %w", err)
	}
	var out []string
	for _, n := range names {
		// .mpls on a Blu-ray, .mpl in an AVCHD recording's 8.3 names.
		if e := strings.ToLower(filepath.Ext(n)); e == ".mpls" || e == ".mpl" {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no playlists under PLAYLIST")
	}
	return out, nil
}

// ReadPlaylist parses PLAYLIST/<name>.
func ReadPlaylist(d Disc, name string) (*Playlist, error) {
	b, err := d.ReadFile("PLAYLIST/" + name)
	if err != nil {
		return nil, err
	}
	return ParseMPLS(b)
}

// StreamPath returns where a clip's stream is on the disc: the SSIF, which
// interleaves the base and dependent views, when the item is 3D and the disc
// has one, else the plain .m2ts.
func StreamPath(d Disc, it PlayItem) (path string, ssif bool) {
	if it.DependentClip != "" {
		p := "STREAM/SSIF/" + it.Clip + ".ssif"
		if d.Exists(p) {
			return p, true
		}
	}
	return clipStream(d, it.Clip), false
}

// DependentStreamPath returns the dependent view's own stream file, for a
// 3D item on a disc without an SSIF.
func DependentStreamPath(d Disc, it PlayItem) string {
	return clipStream(d, it.DependentClip)
}

// clipStream is a clip's stream file: .m2ts on a Blu-ray, .mts in AVCHD.
func clipStream(d Disc, clip string) string {
	p := "STREAM/" + clip + ".m2ts"
	if !d.Exists(p) && d.Exists("STREAM/"+clip+".mts") {
		return "STREAM/" + clip + ".mts"
	}
	return p
}

// ClipInfo returns a clip's info file path: .clpi, or .cpi in AVCHD.
func ClipInfo(d Disc, clip string) string {
	p := "CLIPINF/" + clip + ".clpi"
	if !d.Exists(p) && d.Exists("CLIPINF/"+clip+".cpi") {
		return "CLIPINF/" + clip + ".cpi"
	}
	return p
}

// --- directory ---------------------------------------------------------------

type dirDisc struct{ root string }

func (d *dirDisc) Describe() string { return d.root }

// Close does nothing: a directory holds nothing open.
func (d *dirDisc) Close() error { return nil }

// resolve maps a relative path onto the filesystem, matching each component
// case-insensitively.
func (d *dirDisc) resolve(path string) (string, error) {
	cur := d.root
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", err
		}
		found := ""
		for _, e := range entries {
			if e.Name() == part {
				found = part
				break
			}
			if strings.EqualFold(e.Name(), part) {
				found = e.Name()
			}
		}
		if found == "" {
			return "", os.ErrNotExist
		}
		cur = filepath.Join(cur, found)
	}
	return cur, nil
}

func (d *dirDisc) List(dir string) ([]string, error) {
	p, err := d.resolve(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

func (d *dirDisc) ReadFile(path string) ([]byte, error) {
	p, err := d.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p) //nolint:gosec // a file of the disc being read
}

func (d *dirDisc) Open(path string) (io.ReadSeekCloser, int64, error) {
	p, err := d.resolve(path)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p) //nolint:gosec // a file of the disc being read
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func (d *dirDisc) Exists(path string) bool {
	_, err := d.resolve(path)
	return err == nil
}

// --- UDF image ---------------------------------------------------------------

// isoDisc reads a BDMV straight out of a disc image: a Blu-ray is a UDF 2.50
// filesystem, and reading it in place needs no mount and no root.
type isoDisc struct {
	path string
	f    io.Closer // the image file Open opened; nil for a caller's reader
	u    *udf.Udf
	bdmv []udf.File // entries of the BDMV directory
}

func openISO(path string) (Disc, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's input
	if err != nil {
		return nil, fmt.Errorf("opening the image: %w", err)
	}
	d, err := openUDF(f, path)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	d.f = f
	return d, nil
}

// OpenImage reads a UDF disc image from r, which holds size bytes; label
// names it in messages. The image may be partial — a pre-download sample
// with only its first and last pieces, say — as long as the reads the
// directory and the files asked for need succeed: a read outside what r has
// is an ordinary error. The caller owns r; Close leaves it open.
func OpenImage(r io.ReaderAt, size int64, label string) (Disc, error) {
	if size <= 0 {
		return nil, fmt.Errorf("%s: an image of %d bytes", label, size)
	}
	// The UDF reader finds the trailing anchors by asking the reader its
	// size, which a bare io.ReaderAt cannot say.
	return openUDF(sizedReader{r, size}, label)
}

type sizedReader struct {
	io.ReaderAt
	size int64
}

func (s sizedReader) Size() int64 { return s.size }

// openUDF reads the image's root and BDMV directories. A malformed or
// partial image is an error, never a panic out of the UDF reader.
func openUDF(r io.ReaderAt, label string) (d *isoDisc, err error) {
	defer func() {
		if p := recover(); p != nil {
			d, err = nil, fmt.Errorf("reading %s as a UDF image: malformed (%v)", filepath.Base(label), p)
		}
	}()
	u, err := udf.NewUdfFromReader(r)
	if err != nil {
		return nil, fmt.Errorf("reading %s as a UDF image: %w", filepath.Base(label), err)
	}
	root, err := u.ReadDir(nil)
	if err != nil {
		return nil, fmt.Errorf("reading the image root: %w", err)
	}
	d = &isoDisc{path: label, u: u}
	for i := range root {
		e := &root[i]
		if e.IsDir() && strings.EqualFold(e.Name(), "BDMV") {
			kids, err := e.ReadDir()
			if err != nil {
				return nil, err
			}
			d.bdmv = kids
			return d, nil
		}
	}
	return nil, fmt.Errorf("no BDMV in %s", filepath.Base(label))
}

func (d *isoDisc) Describe() string { return d.path }

// guard turns a panic out of the UDF reader — a partial or malformed image
// — into an error.
func guard(err *error) {
	if p := recover(); p != nil {
		*err = fmt.Errorf("reading the image: malformed (%v)", p)
	}
}

func (d *isoDisc) find(path string) (_ *udf.File, err error) {
	defer guard(&err)
	entries := d.bdmv
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "" {
			continue
		}
		var hit *udf.File
		for j := range entries {
			e := &entries[j]
			if e.Name() == part {
				hit = e
				break
			}
			if strings.EqualFold(e.Name(), part) {
				hit = e
			}
		}
		if hit == nil {
			return nil, os.ErrNotExist
		}
		if i == len(parts)-1 {
			return hit, nil
		}
		if !hit.IsDir() {
			return nil, os.ErrNotExist
		}
		kids, err := hit.ReadDir()
		if err != nil {
			return nil, err
		}
		entries = kids
	}
	return nil, os.ErrNotExist
}

func (d *isoDisc) List(dir string) (_ []string, err error) {
	defer guard(&err)
	entries := d.bdmv
	if dir != "" && dir != "." {
		f, err := d.find(dir)
		if err != nil {
			return nil, err
		}
		kids, err := f.ReadDir()
		if err != nil {
			return nil, err
		}
		entries = kids
	}
	var out []string
	for i := range entries {
		if n := entries[i].Name(); n != "." && n != ".." && n != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (d *isoDisc) ReadFile(path string) (_ []byte, err error) {
	defer guard(&err)
	f, err := d.find(path)
	if err != nil {
		return nil, err
	}
	r, err := f.NewReader()
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

type sectionCloser struct{ *io.SectionReader }

func (sectionCloser) Close() error { return nil }

func (d *isoDisc) Open(path string) (_ io.ReadSeekCloser, _ int64, err error) {
	defer guard(&err)
	f, err := d.find(path)
	if err != nil {
		return nil, 0, err
	}
	r, err := f.NewReader()
	if err != nil {
		return nil, 0, err
	}
	return sectionCloser{r}, f.Size(), nil
}

func (d *isoDisc) Exists(path string) bool {
	_, err := d.find(path)
	return err == nil
}

// Close releases the image file Open opened.
func (d *isoDisc) Close() error {
	if d.f == nil {
		return nil
	}
	err := d.f.Close()
	d.f = nil
	return err
}
