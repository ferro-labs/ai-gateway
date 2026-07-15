package bootstrap

import (
	"testing"
	"time"
)

// withRateLimitMaxKeys temporarily shrinks defaultRateLimitMaxKeys so tests
// can exercise the key-map eviction cap cheaply, restoring it afterward.
func withRateLimitMaxKeys(t *testing.T, n int) {
	t.Helper()
	orig := defaultRateLimitMaxKeys
	defaultRateLimitMaxKeys = n
	t.Cleanup(func() { defaultRateLimitMaxKeys = orig })
}

func TestNewRateLimitStore_DefaultEnabled(t *testing.T) {
	t.Setenv("RATE_LIMIT_RPS", "")
	t.Setenv("RATE_LIMIT_BURST", "")

	store := NewRateLimitStore()
	if store == nil {
		t.Fatal("expected rate limiting to be enabled by default")
	}

	for i := range int(defaultRateLimitBurst) {
		if !store.Allow("1.2.3.4") {
			t.Fatalf("expected allow on request %d within default burst", i+1)
		}
	}
	if store.Allow("1.2.3.4") {
		t.Fatal("expected the request beyond the default burst to be rejected")
	}
}

func TestNewRateLimitStore_ExplicitZero_Disables(t *testing.T) {
	t.Setenv("RATE_LIMIT_RPS", "0")

	if store := NewRateLimitStore(); store != nil {
		t.Fatal("expected RATE_LIMIT_RPS=0 to disable rate limiting")
	}
}

func TestNewRateLimitStore_InvalidRPS_FallsBackToDefault(t *testing.T) {
	for _, v := range []string{"not-a-number", "-5", "NaN", "Inf"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("RATE_LIMIT_RPS", v)

			store := NewRateLimitStore()
			if store == nil {
				t.Fatal("expected fallback to the default (non-nil) store for invalid RATE_LIMIT_RPS")
			}
		})
	}
}

func TestNewRateLimitStore_CustomRPSOverridesDefault(t *testing.T) {
	t.Setenv("RATE_LIMIT_RPS", "5")
	t.Setenv("RATE_LIMIT_BURST", "3")

	store := NewRateLimitStore()
	if store == nil {
		t.Fatal("expected a non-nil store")
	}
	for i := range 3 {
		if !store.Allow("9.9.9.9") {
			t.Fatalf("expected allow on request %d within custom burst", i+1)
		}
	}
	if store.Allow("9.9.9.9") {
		t.Fatal("expected the request beyond the custom burst to be rejected")
	}
}

// TestNewRateLimitStore_CapsKeyMap proves the per-IP store is bounded
// (ratelimit.NewStoreWithMax), not unbounded (ratelimit.NewStore): with the
// cap shrunk to 2, a 3rd distinct key must evict the least-recently-seen
// entry rather than growing the map indefinitely.
func TestNewRateLimitStore_CapsKeyMap(t *testing.T) {
	withRateLimitMaxKeys(t, 2)
	t.Setenv("RATE_LIMIT_RPS", "")
	t.Setenv("RATE_LIMIT_BURST", "2")

	store := NewRateLimitStore()
	if store == nil {
		t.Fatal("expected a non-nil store")
	}

	now := time.Unix(0, 0)
	store.SetNowForTest(func() time.Time { return now })
	store.Allow("ip-1")
	store.Allow("ip-1")
	if store.Allow("ip-1") {
		t.Fatal("expected ip-1's burst to be exhausted")
	}
	now = now.Add(time.Second)

	store.Allow("ip-2")
	store.Allow("ip-2")
	now = now.Add(time.Second)

	// A 3rd distinct key at cap=2 must evict the least-recently-seen entry
	// (ip-1, last touched before ip-2).
	store.Allow("ip-3")

	if !store.Allow("ip-1") {
		t.Fatal("expected ip-1 to have been evicted and replaced with a fresh limiter (key map is not capped)")
	}
}
