package convert

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brunoga/bdtools/mvc"
)

// These drive the real decoder against real MVC elementary streams. Asserting
// on generated argv proves the shape of a command; only decoding proves the
// pipeline's first stage, and a mistake there surfaces hours into a
// conversion.
//
// The fixtures are mvc-source's: a demuxed pair and the combined stream they
// came from, small enough to live in the repository.
func fixtureDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", "testdata", "mvc-source"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func readFixture(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Skipf("fixture %s missing: %v", name, err)
	}
	return b
}

// y4mHeader returns the first line of a Y4M stream, which carries the frame size.
func y4mHeader(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i > 0 {
		return string(b[:i])
	}
	return ""
}

// decodeSource runs the pipeline's decode stage in process and returns the
// side-by-side Y4M it streams to the encoder.
func decodeSource(t *testing.T, src mvc.Source, swap bool) []byte {
	t.Helper()
	var out bytes.Buffer
	y4m := mvc.NewY4MWriter(&out, mvc.LayoutSideBySide)
	y4m.SwapViews = swap
	dec := mvc.NewDecoder(mvc.Options{})
	st, err := dec.DecodeStream(src, mvc.DecodeOptions{}, y4m.Write)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if err := y4m.Flush(); err != nil {
		t.Fatal(err)
	}
	if st.Errors != 0 {
		t.Errorf("%d access units had errors", st.Errors)
	}
	return out.Bytes()
}

func decodePair(t *testing.T, base, dep []byte, swap bool) []byte {
	t.Helper()
	return decodeSource(t, mvc.Source{Format: mvc.FormatSplit, R: bytes.NewReader(base), Dependent: bytes.NewReader(dep)}, swap)
}

// The decoder takes a demuxed pair, the base and dependent views as two
// elementary streams, which is what mvcdec reads from a split source.
func TestDecoderTakesTheDemuxedPair(t *testing.T) {
	dir := fixtureDir(t)
	got := decodePair(t, readFixture(t, dir, "mvc_base.264"), readFixture(t, dir, "mvc_dependent.mvc"), false)
	if len(got) == 0 {
		t.Fatal("decoded to nothing")
	}
	h := y4mHeader(got)
	if !strings.HasPrefix(h, "YUV4MPEG2") {
		t.Fatalf("not a Y4M stream: %q", h)
	}
	// The eyes are stacked, so the frame is double the single-view width.
	if !strings.Contains(h, "W1280 H480") {
		t.Errorf("header = %q, want a 1280x480 side-by-side frame", h)
	}
}

// Decoding the pair must give exactly what decoding the equivalent combined
// stream gives. This is the check that pairing the views is right rather than
// merely plausible.
func TestDemuxedPairMatchesACombinedStream(t *testing.T) {
	dir := fixtureDir(t)
	viaPair := decodePair(t, readFixture(t, dir, "mvc_base.264"), readFixture(t, dir, "mvc_dependent.mvc"), false)
	viaCombined := decodeSource(t, mvc.Source{Format: mvc.FormatAnnexB,
		R: bytes.NewReader(readFixture(t, dir, "mvc_combined.264"))}, false)
	if !bytes.Equal(viaPair, viaCombined) {
		t.Errorf("pair decode differs from combined decode: %d vs %d bytes", len(viaPair), len(viaCombined))
	}
}

// firstFrameLuma returns the luma plane of the first frame of a 1280x480 Y4M.
func firstFrameLuma(t *testing.T, y4m []byte) []byte {
	t.Helper()
	h := y4mHeader(y4m)
	if !strings.Contains(h, "W1280 H480") {
		t.Skipf("unexpected geometry %q", h)
	}
	i := bytes.IndexByte(y4m, '\n') + 1
	if !bytes.HasPrefix(y4m[i:], []byte("FRAME")) {
		t.Fatal("no frame in the stream")
	}
	k := bytes.IndexByte(y4m[i:], '\n')
	if k < 0 {
		t.Fatal("truncated frame header")
	}
	i += k + 1
	const w, ht = 1280, 480
	if len(y4m) < i+w*ht {
		t.Fatal("truncated frame")
	}
	return y4m[i : i+w*ht]
}

// The two halves must differ. If the dependent view silently failed to decode,
// the dimensions would still be right and the output would be a 3D file with no
// depth — the one failure nothing else here would notice.
func TestDecodedEyesDiffer(t *testing.T) {
	dir := fixtureDir(t)
	luma := firstFrameLuma(t, decodePair(t, readFixture(t, dir, "mvc_base.264"), readFixture(t, dir, "mvc_dependent.mvc"), false))
	const w = 1280
	differing := 0
	for row := 0; row < 480; row++ {
		if !bytes.Equal(luma[row*w:row*w+w/2], luma[row*w+w/2:row*w+w]) {
			differing++
		}
	}
	if differing == 0 {
		t.Error("the left and right halves are identical: the dependent view did not decode")
	}
}

// --swap-lr is done by the decoder: the halves come out the other way round.
func TestSwapExchangesTheHalves(t *testing.T) {
	dir := fixtureDir(t)
	base, dep := readFixture(t, dir, "mvc_base.264"), readFixture(t, dir, "mvc_dependent.mvc")
	plain := firstFrameLuma(t, decodePair(t, base, dep, false))
	swapped := firstFrameLuma(t, decodePair(t, base, dep, true))
	const w = 1280
	for row := 0; row < 480; row++ {
		l, r := plain[row*w:row*w+w/2], plain[row*w+w/2:row*w+w]
		sl, sr := swapped[row*w:row*w+w/2], swapped[row*w+w/2:row*w+w]
		if !bytes.Equal(l, sr) || !bytes.Equal(r, sl) {
			t.Fatalf("row %d: the swapped frame is not the plain one with its halves exchanged", row)
		}
	}
}
