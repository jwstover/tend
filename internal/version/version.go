// Package version reports the build version of tend. The version string is
// stamped in at release time via ldflags; outside a release build it falls
// back to the module version recorded in the binary's build info.
package version

import "runtime/debug"

// version is set at release time via
// -ldflags "-X github.com/jwstover/tend/internal/version.version=v0.1.0".
var version = ""

// String returns the build version: the ldflags-stamped value when present,
// otherwise the module version from build info (a real tag for
// `go install @tag`, a pseudo-version or "(devel)" for local builds).
func String() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "unknown"
}
