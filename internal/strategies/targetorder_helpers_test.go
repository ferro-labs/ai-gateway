package strategies

import "testing"

func assertKeys(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keys = %v, want %v", got, want)
		}
	}
}

// TestLoadBalance_SelectTargets_MatchesExecuteCandidates asserts SelectTargets
// (the streaming path) draws from the same candidate set as Execute: only
// model-compatible, registered targets. A high-weight target that is
// incompatible or unregistered must never appear, otherwise streaming selection
// would skew toward a provider Route never picks.
