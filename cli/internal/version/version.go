// Package version holds build metadata injected via -ldflags, mirroring the
// gateway's internal/version so release tooling stays uniform across the two
// products in this repo.
package version

import "fmt"

// Version, Commit, and Date are overwritten at link time by the release
// build. The defaults below are what a plain `go build` or `go run` reports.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String renders the full build stamp for `ferro version`.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
