package version

import "testing"

func TestStringAndShort(t *testing.T) {
	origVersion, origCommit, origDate := Version, Commit, Date
	t.Cleanup(func() {
		Version = origVersion
		Commit = origCommit
		Date = origDate
	})

	Version = "v1.0.0-rc.1"
	Commit = "abc1234"
	Date = "2026-03-14T00:00:00Z"

	if got, want := Short(), "v1.0.0-rc.1"; got != want {
		t.Fatalf("Short() = %q, want %q", got, want)
	}

	if got, want := String(), "v1.0.0-rc.1 (commit abc1234, built 2026-03-14T00:00:00Z)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
