package strategies

import (
	"fmt"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"
)

func stickyLoadBalance(t *testing.T, sticky Sticky) *LoadBalance {
	t.Helper()
	a := &mockProvider{name: "a", models: []string{"gpt-4o"}}
	b := &mockProvider{name: "b", models: []string{"gpt-4o"}}
	drained := &mockProvider{name: "drained", models: []string{"gpt-4o"}}
	targets := []Target{{VirtualKey: "a", Weight: 50}, {VirtualKey: "b", Weight: 50}, {VirtualKey: "drained", Weight: 0}}
	return NewLoadBalance(targets, newLookup(a, b, drained)).WithSticky(sticky)
}

func firstFor(t *testing.T, s Strategy, user string) string {
	t.Helper()
	keys, err := s.SelectTargets(providers.Request{Model: "gpt-4o", User: user})
	if err != nil || len(keys) == 0 {
		t.Fatalf("SelectTargets = %v, %v", keys, err)
	}
	return keys[0]
}

// The same user lands on the same target every time, different users spread
// across the pool, a drained target is never chosen, and a request with no
// user still draws at random.
func TestLoadBalance_StickyOnUserPinsTheStart(t *testing.T) {
	lb := stickyLoadBalance(t, Sticky{On: StickyOnUser})

	pinned := firstFor(t, lb, "alice")
	for range 100 {
		if got := firstFor(t, lb, "alice"); got != pinned {
			t.Fatalf("alice moved from %q to %q", pinned, got)
		}
	}
	seen := map[string]int{}
	for i := range 200 {
		seen[firstFor(t, lb, fmt.Sprintf("user-%d", i))]++
	}
	if seen["a"] < 50 || seen["b"] < 50 || seen["drained"] != 0 {
		t.Fatalf("200 users landed on %v; want both live targets used and the drained one never", seen)
	}
	anonymous := map[string]bool{}
	for range 200 {
		anonymous[firstFor(t, lb, "")] = true
	}
	if len(anonymous) != 2 {
		t.Fatalf("requests with no user landed only on %v; want a random draw", anonymous)
	}
}

// A TTL rotates assignments: within one window a user is pinned; across
// windows the pin may move, so a stuck assignment cannot outlive the TTL.
func TestLoadBalance_StickyTTLRotatesAcrossWindows(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	lb := stickyLoadBalance(t, Sticky{On: StickyOnUser, TTL: time.Hour, now: func() time.Time { return now }})

	users := 64
	before := make([]string, users)
	for i := range users {
		before[i] = firstFor(t, lb, fmt.Sprintf("user-%d", i))
	}
	now = now.Add(30 * time.Minute)
	for i := range users {
		if got := firstFor(t, lb, fmt.Sprintf("user-%d", i)); got != before[i] {
			t.Fatalf("user-%d moved inside the window", i)
		}
	}
	now = now.Add(time.Hour)
	moved := 0
	for i := range users {
		if firstFor(t, lb, fmt.Sprintf("user-%d", i)) != before[i] {
			moved++
		}
	}
	if moved == 0 {
		t.Fatal("no user moved after the TTL window rolled over")
	}
}

// A sticky A/B test keeps a session on its variant.
func TestABTest_StickyOnUserPinsTheVariant(t *testing.T) {
	control := &mockProvider{name: "control", models: []string{"gpt-4o"}}
	challenger := &mockProvider{name: "challenger", models: []string{"gpt-4o"}}
	ab, err := NewABTest([]ABTestVariant{
		{Target: Target{VirtualKey: "control"}, Weight: 50, Label: "control"},
		{Target: Target{VirtualKey: "challenger"}, Weight: 50, Label: "challenger"},
	}, newLookup(control, challenger))
	if err != nil {
		t.Fatal(err)
	}
	ab.WithSticky(Sticky{On: StickyOnUser})

	pinned := firstFor(t, ab, "session-7")
	for range 100 {
		if got := firstFor(t, ab, "session-7"); got != pinned {
			t.Fatalf("session moved from %q to %q", pinned, got)
		}
	}
	seen := map[string]bool{}
	for i := range 100 {
		seen[firstFor(t, ab, fmt.Sprintf("session-%d", i))] = true
	}
	if len(seen) != 2 {
		t.Fatalf("100 sessions landed only on %v; want both variants used", seen)
	}
	anonymous := map[string]bool{}
	for range 200 {
		anonymous[firstFor(t, ab, "")] = true
	}
	if len(anonymous) != 2 {
		t.Fatalf("requests with no user landed only on %v; want a random draw", anonymous)
	}
}
