// Package version holds build-time version information for FerroGateway
// binaries. The variables are injected by GoReleaser (and the Makefile dev
// targets) via -ldflags:
//
// -X github.com/ferro-labs/ai-gateway/internal/version.Version=v0.1.0
// -X github.com/ferro-labs/ai-gateway/internal/version.Commit=abc1234
// -X github.com/ferro-labs/ai-gateway/internal/version.Date=2026-02-25T00:00:00Z
//
// so local builds without ldflags still produce sensible output.
package version

import "fmt"

// Variables set at link time by GoReleaser / Makefile. Default to dev values
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a single-line human-readable version string, e.g.:
//
// v0.1.0 (commit abc1234, built 2026-02-25T12:00:00Z)
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}

// Short returns just the version tag, e.g. "v0.1.0" or "dev".
func Short() string {
	return Version
}
