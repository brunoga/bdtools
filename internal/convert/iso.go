package convert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golift.io/udf"
)

// A Blu-ray disc image is a UDF 2.50 filesystem, and mounting one needs root —
// which rules it out for an unattended conversion. Reading it directly does
// not: this extracts the few files tsMuxeR needs straight out of the image.
//
// What a 3D disc holds, and why only some of it matters:
//
//	BDMV/PLAYLIST/*.mpls      which clips make up a title; tiny
//	BDMV/CLIPINF/*.clpi       stream metadata for each clip; tiny
//	BDMV/STREAM/*.m2ts        the base view alone — a 2D copy of the film
//	BDMV/STREAM/SSIF/*.ssif   base and dependent views interleaved — the 3D one
//
// Only the SSIF is extracted. On the disc it shares extents with the matching
// .m2ts, so copying both out would write the base view twice; and tsMuxeR reads
// a playlist perfectly well with the .m2ts absent, which is what makes skipping
// it safe rather than merely frugal.
//
// BACKUP, AUXDATA, CERTIFICATE, JAR, BDJO and META are skipped outright: a
// duplicate of the metadata and the disc's Java menus, none of which a
// conversion touches.

// isoWanted reports whether a path inside a disc image is worth extracting.
// The path is slash-separated and rooted at the image's root, e.g.
// "/BDMV/PLAYLIST/00800.mpls".
func isoWanted(path string) bool {
	p := strings.ToUpper(strings.TrimPrefix(path, "/"))
	if !strings.HasPrefix(p, "BDMV/") {
		return false
	}
	rest := strings.TrimPrefix(p, "BDMV/")
	// A second copy of the metadata; the primary is already taken.
	if strings.HasPrefix(rest, "BACKUP/") {
		return false
	}
	switch {
	case rest == "INDEX.BDMV", rest == "MOVIEOBJECT.BDMV":
		return true
	case strings.HasPrefix(rest, "PLAYLIST/") && strings.HasSuffix(rest, ".MPLS"):
		return true
	case strings.HasPrefix(rest, "CLIPINF/") && strings.HasSuffix(rest, ".CLPI"):
		return true
	case strings.HasPrefix(rest, "STREAM/SSIF/") && strings.HasSuffix(rest, ".SSIF"):
		return true
	}
	return false
}

// LooksLikeISO reports whether a path is a disc image this can read.
func LooksLikeISO(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".iso")
}

// ErrNoBDMV says an image carried no Blu-ray structure.
var ErrNoBDMV = errors.New("no BDMV structure in the image")

// ExtractBDMV copies the files tsMuxeR needs out of a disc image into dest,
// preserving their layout, and returns the BDMV directory it wrote.
//
// It reports as it goes: the SSIF is the whole film and on a real disc that is
// tens of gigabytes, so a silent several minutes would look like a hang.
func ExtractBDMV(ctx context.Context, isoPath, dest string, report Reporter) (string, error) {
	f, err := os.Open(isoPath) //nolint:gosec // the path is the operator's input
	if err != nil {
		return "", fmt.Errorf("opening the image: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only

	u, err := udf.NewUdfFromReader(f)
	if err != nil {
		return "", fmt.Errorf("reading %s as a UDF image: %w", filepath.Base(isoPath), err)
	}
	root, err := u.ReadDir(nil)
	if err != nil {
		return "", fmt.Errorf("reading the image root: %w", err)
	}

	var found, copied int
	var bytes int64
	err = walkISO(ctx, root, "", func(file *udf.File, path string) error {
		found++
		if !isoWanted(path) {
			return nil
		}
		out := filepath.Join(dest, filepath.FromSlash(strings.TrimPrefix(path, "/")))
		// A crafted image could name a file with a separator or "..": nothing
		// may be written outside the destination.
		if rel, err := filepath.Rel(dest, out); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("the image names a file outside its own tree: %q", path)
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil { //nolint:gosec // contained: checked above
			return err
		}
		n, err := copyISOFile(file, out)
		if err != nil {
			return fmt.Errorf("extracting %s: %w", path, err)
		}
		copied++
		bytes += n
		// Only the streams are big enough to be worth announcing.
		if n > 1<<30 {
			report.Report("extracted %s (%.1f GiB)", path, float64(n)/(1<<30))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if copied == 0 {
		return "", fmt.Errorf("%w (%d files in the image)", ErrNoBDMV, found)
	}
	report.Report("extracted %d of %d files from the image (%.1f GiB)",
		copied, found, float64(bytes)/(1<<30))
	return filepath.Join(dest, "BDMV"), nil
}

// walkISO visits every file in the image, depth-first.
func walkISO(ctx context.Context, entries []udf.File, prefix string, visit func(*udf.File, string) error) error {
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := &entries[i]
		name := e.Name()
		if name == "." || name == ".." || name == "" {
			continue
		}
		path := prefix + "/" + name
		if e.IsDir() {
			// A directory that cannot be read is reported rather than fatal:
			// a disc carries menus and Java archives this does not need, and
			// one unreadable corner of them must not stop the conversion.
			kids, err := e.ReadDir()
			if err != nil {
				continue
			}
			if err := walkISO(ctx, kids, path, visit); err != nil {
				return err
			}
			continue
		}
		if err := visit(e, path); err != nil {
			return err
		}
	}
	return nil
}

// copyISOFile writes one file out of the image, returning its size.
func copyISOFile(file *udf.File, dest string) (int64, error) {
	src, err := file.NewReader()
	if err != nil {
		return 0, err
	}
	out, err := os.Create(dest) //nolint:gosec // dest is built under our temp dir
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, src)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dest) //nolint:gosec // the file this function created
		return 0, err
	}
	return n, nil
}

// Playlist is one candidate title inside a BDMV.
type Playlist struct {
	Path     string
	Duration time.Duration
	// ThreeD is true when the title carries an MVC dependent view.
	ThreeD bool
	// BaseViewIsRight is set when the disc says its base view is the right eye,
	// which the stacking has to compensate for. KnownEye says whether the disc
	// said at all.
	BaseViewIsRight bool
	KnownEye        bool
}

// FindPlaylists lists the playlists in a BDMV directory, in filename order.
func FindPlaylists(bdmv string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(bdmv, "PLAYLIST", "*.mpls"))
	if err != nil {
		return nil, err
	}
	// A disc authored on a case-sensitive filesystem may use either case.
	upper, _ := filepath.Glob(filepath.Join(bdmv, "PLAYLIST", "*.MPLS"))
	matches = append(matches, upper...)
	sort.Strings(matches)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no playlists under %s", filepath.Join(bdmv, "PLAYLIST"))
	}
	return matches, nil
}

// ChoosePlaylist picks the title to convert: the longest one that is actually
// 3D.
//
// A disc holds a playlist per title — the feature, its trailers, the menus, and
// often several near-duplicates of the feature itself. Picking by filename or
// by number gets a trailer or a menu loop as often as the film, so each is
// probed and the longest 3D one wins. Length is the signal that works: a
// feature is tens of times longer than anything else on the disc.
func ChoosePlaylist(ctx context.Context, probe func(context.Context, string) (string, error), bdmv string, report Reporter) (Playlist, error) {
	paths, err := FindPlaylists(bdmv)
	if err != nil {
		return Playlist{}, err
	}
	var best Playlist
	var seen3D int
	for _, p := range paths {
		out, err := probe(ctx, p)
		if err != nil && out == "" {
			continue // a playlist tsMuxeR cannot read is not a candidate
		}
		tracks, err := ParseListing(out)
		if err != nil {
			continue
		}
		cand := Playlist{Path: p, Duration: ParseDuration(out)}
		cand.BaseViewIsRight, cand.KnownEye = BaseViewIsRightEye(out)
		if _, err := SelectTracks(tracks); err == nil {
			cand.ThreeD = true
			seen3D++
		}
		if !cand.ThreeD {
			continue
		}
		// The first 3D candidate always wins outright. Comparing durations
		// alone would reject it when every playlist reports the same length,
		// or none reports one at all.
		if best.Path == "" || cand.Duration > best.Duration {
			best = cand
		}
	}
	if best.Path == "" {
		return Playlist{}, fmt.Errorf("none of the %d playlists in %s is 3D "+
			"(no MVC dependent view in any of them)", len(paths), bdmv)
	}
	length := "length unknown"
	if best.Duration > 0 {
		length = best.Duration.Round(time.Second).String()
	}
	report.Report("chose %s (%s) from %d playlists, %d of them 3D",
		filepath.Base(best.Path), length, len(paths), seen3D)
	if best.KnownEye && best.BaseViewIsRight {
		report.Report("the disc's base view is the right eye, so the eyes will be swapped")
	}
	return best, nil
}
