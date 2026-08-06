package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// repoFile reads a path relative to the repository root.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", rel)) //nolint:gosec // G304: fixed repository-relative path
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// unitTestTimeout finds the -timeout on the unit-test invocation (race + short)
// in the given file and returns it in seconds.
func unitTestTimeout(t *testing.T, rel string) int {
	t.Helper()
	re := regexp.MustCompile(`go test -v -short -race -timeout (\d+)s`)
	m := re.FindStringSubmatch(repoFile(t, rel))
	if m == nil {
		t.Fatalf("%s no longer contains a `go test -v -short -race -timeout <n>s` invocation; "+
			"if it was renamed, update this guard rather than deleting it", rel)
	}
	secs, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("%s: unparsable timeout %q: %v", rel, m[1], err)
	}
	return secs
}

// OPS-013. `make test` and CI run the same suite, so the local gate must not be
// killed by a bound CI already found too tight: the Makefile sat at 180s long
// after ci.yml was raised to 300s for exactly that reason, and the resulting
// timeout — which names no failing test — was investigated as a regression by
// three separate people.
//
// Local may exceed CI (a developer machine is the loaded one); it may not sit
// under it.
func TestLocalTestTimeoutIsNotTighterThanCI(t *testing.T) {
	local := unitTestTimeout(t, "Makefile")
	ci := unitTestTimeout(t, ".github/workflows/ci.yml")

	if local < ci {
		t.Errorf("make test uses -timeout %ds but CI uses -timeout %ds; "+
			"the local gate would fail runs CI passes", local, ci)
	}
}
