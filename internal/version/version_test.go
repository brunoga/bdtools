package version

import (
	"runtime/debug"
	"testing"
)

// stubBuildInfo makes Resolve see a given embedded module version.
func stubBuildInfo(t *testing.T, v string, ok bool) {
	t.Helper()
	orig := readBuildInfo
	t.Cleanup(func() { readBuildInfo = orig })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		if !ok {
			return nil, false
		}
		return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
	}
}

// A release build passes the tag explicitly and that must win: it is the only
// source that knows the exact tag being released.
func TestLdflagsVersionWins(t *testing.T) {
	stubBuildInfo(t, "v9.9.9", true)
	if got := Resolve("1.46.0"); got != "1.46.0" {
		t.Errorf("Resolve = %q, want the ldflags value", got)
	}
}

// `go install module/cmd/x@v1.46.0` passes no ldflags, so the version the
// toolchain embedded is the only thing that distinguishes one install from
// another. This is the case mvc2sbs was missing, which made every go-installed
// copy report "dev".
func TestEmbeddedVersionUsedWhenNoLdflags(t *testing.T) {
	stubBuildInfo(t, "v1.46.0", true)
	if got := Resolve(Placeholder); got != "v1.46.0" {
		t.Errorf("Resolve = %q, want the embedded module version", got)
	}
}

// "(devel)" is what the toolchain reports when it has no version to derive —
// -buildvcs=false, or a build outside a repository. It is not a version, and
// showing it to a user would be worse than admitting to the placeholder. A
// VCS-derived version like "v1.46.0+dirty" is a different case and is
// reported as-is; TestVCSDerivedVersionIsReported covers it.
func TestDevelIsNotAVersion(t *testing.T) {
	for _, embedded := range []string{"(devel)", ""} {
		stubBuildInfo(t, embedded, true)
		if got := Resolve(Placeholder); got != Placeholder {
			t.Errorf("embedded %q: Resolve = %q, want %q", embedded, got, Placeholder)
		}
	}
}

func TestMissingBuildInfoFallsBack(t *testing.T) {
	stubBuildInfo(t, "", false)
	if got := Resolve(Placeholder); got != Placeholder {
		t.Errorf("Resolve = %q, want %q", got, Placeholder)
	}
	// An empty variable is still answered with something printable.
	if got := Resolve(""); got != Placeholder {
		t.Errorf("Resolve(\"\") = %q, want %q", got, Placeholder)
	}
}

// The real build info must not panic or return junk when consulted for real,
// whatever this test binary happens to have been built from.
func TestResolveWorksAgainstRealBuildInfo(t *testing.T) {
	if got := Resolve(Placeholder); got == "" {
		t.Error("Resolve returned an empty string")
	}
	if got := Resolve("1.2.3"); got != "1.2.3" {
		t.Errorf("Resolve = %q, want the explicit value", got)
	}
}

// A working-tree build is stamped from VCS by modern Go toolchains, and that
// string identifies exactly what was built — including whether the tree was
// dirty — so it is worth more than the placeholder.
func TestVCSDerivedVersionIsReported(t *testing.T) {
	stubBuildInfo(t, "v1.46.0+dirty", true)
	if got := Resolve(Placeholder); got != "v1.46.0+dirty" {
		t.Errorf("Resolve = %q, want the VCS-derived version", got)
	}
}
