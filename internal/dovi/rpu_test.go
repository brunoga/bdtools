package dovi

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// splitRPUs splits a dovi_tool RPU file — each RPU's payload, escaped,
// behind a start code — into NAL units.
func splitRPUs(b []byte) [][]byte {
	var out [][]byte
	for _, p := range nalUnits(b) {
		out = append(out, append([]byte{NALRPU << 1, 1}, p...))
	}
	return out
}

// checkRPUs parses each RPU of a file, checks that writing it back gives the
// same bytes, and that converting it to profile 8.1 gives what dovi_tool's
// mode 2 gives (the file with .m2 before the extension).
func checkRPUs(t *testing.T, path string) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := os.ReadFile(path[:len(path)-len(".bin")] + ".m2.bin")
	if err != nil {
		t.Fatal(err)
	}
	in, want := splitRPUs(src), splitRPUs(ref)
	if len(in) == 0 || len(in) != len(want) {
		t.Fatalf("%d RPUs, %d converted", len(in), len(want))
	}
	bad := 0
	for i, nal := range in {
		u, err := ParseNAL(nal)
		if err != nil {
			t.Fatalf("RPU %d: %v", i, err)
		}
		if got := u.NAL(); !bytes.Equal(got, nal) {
			if bad++; bad <= 3 {
				t.Errorf("RPU %d written back differs:\n% x\nwant\n% x", i, got, nal)
			}
			continue
		}
		if err := u.ToProfile81(); err != nil && u.Profile() != 8 {
			t.Fatalf("RPU %d: %v", i, err)
		}
		if got := u.NAL(); !bytes.Equal(got, want[i]) {
			if bad++; bad <= 3 {
				t.Errorf("RPU %d (profile %d) converted differs from dovi_tool's:\n% x\nwant\n% x", i, u.Profile(), got, want[i])
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d RPUs wrong", bad, len(in))
	}
	t.Logf("%d RPUs", len(in))
}

func TestRPUFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*[^2].bin"))
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) { checkRPUs(t, f) })
	}
}

// TestRPUSamples runs over whole streams' RPUs, extracted with dovi_tool
// into $BDTOOLS_RPU_SAMPLES (name.bin and name.m2.bin).
func TestRPUSamples(t *testing.T) {
	dir := os.Getenv("BDTOOLS_RPU_SAMPLES")
	if dir == "" {
		t.Skip("BDTOOLS_RPU_SAMPLES not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*[^2].bin"))
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) { checkRPUs(t, f) })
	}
}
