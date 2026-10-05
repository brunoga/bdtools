package convert

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Only the files a conversion reads come out of the image. The base-view .m2ts
// is deliberately skipped: it shares extents with the SSIF on the disc, so
// copying both would write the base view twice, and tsMuxeR reads a playlist
// with the .m2ts absent.
func TestISOExtractsOnlyWhatIsNeeded(t *testing.T) {
	want := []string{
		"/BDMV/index.bdmv",
		"/BDMV/MovieObject.bdmv",
		"/BDMV/PLAYLIST/00800.mpls",
		"/BDMV/CLIPINF/00800.clpi",
		"/BDMV/STREAM/SSIF/00800.ssif",
	}
	for _, p := range want {
		if !isoWanted(p) {
			t.Errorf("%s should be extracted", p)
		}
	}
	skip := []string{
		"/BDMV/STREAM/00800.m2ts",          // the base view alone; the SSIF has it
		"/BDMV/BACKUP/PLAYLIST/00800.mpls", // a duplicate of the metadata
		"/BDMV/BACKUP/index.bdmv",
		"/BDMV/JAR/00000.jar",
		"/BDMV/BDJO/00000.bdjo",
		"/BDMV/AUXDATA/sound.bdmv",
		"/BDMV/META/DL/bdmt_eng.xml",
		"/CERTIFICATE/id.bdmv",
		"/AACS/Unit_Key_RO.inf",
		"/README.txt",
	}
	for _, p := range skip {
		if isoWanted(p) {
			t.Errorf("%s should not be extracted", p)
		}
	}
}

// A disc authored on a case-insensitive system may use any case.
func TestISOWantedIgnoresCase(t *testing.T) {
	for _, p := range []string{
		"/bdmv/playlist/00800.mpls",
		"/BDMV/Playlist/00800.MPLS",
		"/BDMV/stream/ssif/00800.ssif",
	} {
		if !isoWanted(p) {
			t.Errorf("%s should be extracted regardless of case", p)
		}
	}
	if isoWanted("/BDMV/backup/PLAYLIST/00800.mpls") {
		t.Error("BACKUP should be skipped in any case")
	}
}

func TestLooksLikeISO(t *testing.T) {
	for _, p := range []string{"/x/disc.iso", "/x/DISC.ISO", "relative.Iso"} {
		if !LooksLikeISO(p) {
			t.Errorf("%s should look like an image", p)
		}
	}
	for _, p := range []string{"/x/disc.m2ts", "/x/disc.mkv", "/x/BDMV", "/x/iso"} {
		if LooksLikeISO(p) {
			t.Errorf("%s should not look like an image", p)
		}
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"Duration: 02:07:33.480":  2*time.Hour + 7*time.Minute + 33*time.Second + 480*time.Millisecond,
		"Duration: 00:00:00.375":  375 * time.Millisecond,
		"Duration: 00:01:05":      65 * time.Second,
		"Duration: 123:00:00.000": 123 * time.Hour,
		"nothing here":            0,
	}
	for in, want := range cases {
		if got := ParseDuration(in); got != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", in, got, want)
		}
	}
	// It has to find the line in a full listing, not just on its own.
	full := "Track ID:    4113\nStream lang: eng\n\nDuration: 01:58:00.000\nMarks: 0\n"
	if got := ParseDuration(full); got != 118*time.Minute {
		t.Errorf("got %v, want 1h58m from a full listing", got)
	}
}

// Most discs put the left eye in the base view; some do not, and the
// difference is the difference between 3D and a headache.
func TestBaseViewIsRightEye(t *testing.T) {
	left := "Duration: 01:58:00.000\nBase view: left-eye\nMarks: 0\n"
	if isRight, known := BaseViewIsRightEye(left); isRight || !known {
		t.Errorf("left-eye: got (%v,%v), want (false,true)", isRight, known)
	}
	right := "Base view: right-eye\n"
	if isRight, known := BaseViewIsRightEye(right); !isRight || !known {
		t.Errorf("right-eye: got (%v,%v), want (true,true)", isRight, known)
	}
	if _, known := BaseViewIsRightEye("Duration: 01:58:00.000\n"); known {
		t.Error("a listing with no Base view line must report unknown, not left")
	}
}

// --- playlist selection ------------------------------------------------------

// fakeDisc writes a BDMV tree with the given playlist names, so selection can
// be tested without a disc.
func fakeDisc(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	pl := filepath.Join(dir, "BDMV", "PLAYLIST")
	if err := os.MkdirAll(pl, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(pl, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "BDMV")
}

func listing(tracks string, dur string, eye string) string {
	s := tracks
	if dur != "" {
		s += "\nDuration: " + dur
	}
	if eye != "" {
		s += "\nBase view: " + eye
	}
	return s
}

const tracks3D = `Track ID:    4113
Stream ID:   V_MPEG4/ISO/AVC

Track ID:    4114
Stream ID:   V_MPEG4/ISO/MVC
`

const tracks2D = `Track ID:    4113
Stream ID:   V_MPEG4/ISO/AVC
`

// The feature is tens of times longer than the trailers and menu loops beside
// it, so length is what tells them apart.
func TestChoosePlaylistTakesTheLongest3DTitle(t *testing.T) {
	bdmv := fakeDisc(t, "00000.mpls", "00001.mpls", "00800.mpls")
	probe := func(_ context.Context, p string) (string, error) {
		switch filepath.Base(p) {
		case "00000.mpls":
			return listing(tracks3D, "00:00:30.000", "left-eye"), nil // a trailer
		case "00001.mpls":
			return listing(tracks2D, "02:30:00.000", ""), nil // long but 2D
		default:
			return listing(tracks3D, "01:58:00.000", "left-eye"), nil // the film
		}
	}
	got, err := ChoosePlaylist(context.Background(), probe, bdmv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got.Path) != "00800.mpls" {
		t.Errorf("chose %s, want 00800.mpls", filepath.Base(got.Path))
	}
	if got.Duration != 118*time.Minute {
		t.Errorf("duration = %v, want 1h58m", got.Duration)
	}
}

// The bug this pins: comparing durations alone left the first candidate
// unselected when nothing reported a length, so a disc with one playlist
// converted nothing.
func TestChoosePlaylistTakesASingleTitleWithNoDuration(t *testing.T) {
	bdmv := fakeDisc(t, "00000.mpls")
	probe := func(_ context.Context, _ string) (string, error) {
		return listing(tracks3D, "", "left-eye"), nil
	}
	got, err := ChoosePlaylist(context.Background(), probe, bdmv, nil)
	if err != nil {
		t.Fatalf("a single 3D playlist with no duration must still be chosen: %v", err)
	}
	if filepath.Base(got.Path) != "00000.mpls" {
		t.Errorf("chose %q", got.Path)
	}
}

func TestChoosePlaylistRefusesADiscWithNo3DTitle(t *testing.T) {
	bdmv := fakeDisc(t, "00000.mpls", "00001.mpls")
	probe := func(_ context.Context, _ string) (string, error) {
		return listing(tracks2D, "02:00:00.000", ""), nil
	}
	_, err := ChoosePlaylist(context.Background(), probe, bdmv, nil)
	if err == nil {
		t.Fatal("a 2D disc must be refused")
	}
	for _, want := range []string{"2 playlists", "none", "MVC"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got %q", want, err)
		}
	}
}

// A playlist tsMuxeR cannot read is skipped rather than fatal: a disc carries
// broken and encrypted odds and ends, and one must not stop the conversion.
func TestChoosePlaylistSkipsUnreadableOnes(t *testing.T) {
	bdmv := fakeDisc(t, "00000.mpls", "00800.mpls")
	probe := func(_ context.Context, p string) (string, error) {
		if filepath.Base(p) == "00000.mpls" {
			return "", errors.New("cannot read")
		}
		return listing(tracks3D, "01:58:00.000", "left-eye"), nil
	}
	got, err := ChoosePlaylist(context.Background(), probe, bdmv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got.Path) != "00800.mpls" {
		t.Errorf("chose %q", got.Path)
	}
}

func TestChoosePlaylistCarriesTheEyeOrder(t *testing.T) {
	bdmv := fakeDisc(t, "00800.mpls")
	probe := func(_ context.Context, _ string) (string, error) {
		return listing(tracks3D, "01:58:00.000", "right-eye"), nil
	}
	got, err := ChoosePlaylist(context.Background(), probe, bdmv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.KnownEye || !got.BaseViewIsRight {
		t.Errorf("got KnownEye=%v BaseViewIsRight=%v, want true/true", got.KnownEye, got.BaseViewIsRight)
	}
}

func TestFindPlaylistsOnADiscWithNone(t *testing.T) {
	if _, err := FindPlaylists(fakeDisc(t)); err == nil {
		t.Error("a BDMV with no playlists must be an error")
	}
}
