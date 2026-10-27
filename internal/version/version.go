// Package version reports cbztool's version, which comes from the git tag
// rather than from the source. `go build` and `go install` stamp it into the
// binary: the tag on a tagged commit (v1.0.0), a pseudo-version between tags,
// and a "+dirty" suffix when built with uncommitted changes. Builds without
// git metadata (Nix builds from a source tarball) pass it in instead:
//
//	-ldflags "-X github.com/AeBiCe/cbztool/internal/version.version=1.0.0"
package version

import (
	"runtime/debug"
	"strings"
)

// version is set by -ldflags -X; empty means "ask the build info".
var version string

// String is the version without a leading "v" (e.g. "1.0.0"), or "dev" when
// the build carries no version at all.
func String() string {
	v := version
	if v == "" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
	}
	if v == "" {
		return "dev"
	}
	return strings.TrimPrefix(v, "v")
}
