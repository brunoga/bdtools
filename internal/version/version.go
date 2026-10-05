// Package version resolves the version string a binary reports.
//
// There are two ways a build gets one, and a binary has to handle both:
//
//   - A release build passes it explicitly, with
//     -ldflags="-X main.version=1.46.0". This is what the Dockerfiles do, and
//     it is the only way to get the exact tag being released.
//   - `go install module/cmd/x@v1.46.0` passes no ldflags at all, but the Go
//     toolchain embeds the module version it resolved, which Resolve reads
//     back. Without this a go-installed binary reports "dev" and gives its
//     user no way to tell what they are running.
//
// A plain `go build` from a working tree gets no ldflags either, but the
// toolchain stamps a version derived from VCS — "v1.46.0+dirty" for a tree
// with uncommitted changes — which is more use than a placeholder, so it is
// reported as-is. Only when there is no version to derive, as with
// -buildvcs=false or outside a repository, does the toolchain report
// "(devel)"; that is not a version and the placeholder stands instead.
//
// Verified against all four paths: ldflags wins; `go install ...@v1.46.0`
// reports v1.46.0; `go build` reports v1.46.0+dirty; `go build
// -buildvcs=false` reports the placeholder.
package version

import "runtime/debug"

// Placeholder is what a main package's version variable holds until a build
// overrides it. Resolve treats it as "nothing was passed".
const Placeholder = "dev"

// readBuildInfo is indirected so the embedded-version path can be tested; a
// test binary's own build info reports "(devel)", which would otherwise make
// that branch unreachable.
var readBuildInfo = debug.ReadBuildInfo

// Resolve returns the version to report. ldflagsVersion is the main package's
// version variable: it wins when a build set it, and otherwise the version the
// Go toolchain embedded is used.
func Resolve(ldflagsVersion string) string {
	if ldflagsVersion != "" && ldflagsVersion != Placeholder {
		return ldflagsVersion
	}
	if info, ok := readBuildInfo(); ok && info != nil {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	if ldflagsVersion == "" {
		return Placeholder
	}
	return ldflagsVersion
}
