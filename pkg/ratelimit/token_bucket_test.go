package ratelimit

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

func TestLimiter_AllowWithinBurst(t *testing.T) {
	t.Parallel()

	l := New(10, 5)
	for i := 0; i < 5; i++ {
		if !l.Allow() {
			t.Fatalf("expected allow on request %d within burst", i+1)
		}
	}
}

func TestLimiter_AllowBlocksWhenDepleted(t *testing.T) {
	t.Parallel()

	l := New(10, 2)
	l.Allow()
	l.Allow()
	if l.Allow() {
		t.Fatal("expected rate limit after burst exhausted")
	}
}

func TestLimiter_AllowRefillsOverTime(t *testing.T) {
	t.Parallel()

	now := time.Unix(0, 0)
	l := New(1000, 1)
	l.SetNowForTest(func() time.Time { return now })
	l.Allow()
	now = now.Add(2 * time.Millisecond)
	if !l.Allow() {
		t.Fatal("expected allow after refill")
	}
}

func TestStore_AllowCreatesPerKeyLimiters(t *testing.T) {
	t.Parallel()

	s := NewStore(100, 10)
	for i := 0; i < 10; i++ {
		if !s.Allow("key-a") {
			t.Fatalf("expected allow on key-a request %d", i+1)
		}
	}
	// Key-b should have its own fresh bucket.
	if !s.Allow("key-b") {
		t.Fatal("expected allow on key-b (fresh limiter)")
	}
}

func TestStore_AllowEvictsAtCap(t *testing.T) {
	t.Parallel()

	// Cap at 2 keys. After filling, a new key must evict the oldest.
	s := NewStoreWithMax(100, 100, 2)
	now := time.Unix(0, 0)
	s.SetNowForTest(func() time.Time { return now })
	s.Allow("key-1")
	now = now.Add(time.Second)
	s.Allow("key-2")
	// Adding key-3 should evict key-1 (oldest).
	s.Allow("key-3")

	s.mu.RLock()
	_, has1 := s.limiters["key-1"]
	_, has3 := s.limiters["key-3"]
	s.mu.RUnlock()

	if has1 {
		t.Error("key-1 should have been evicted at cap")
	}
	if !has3 {
		t.Error("key-3 should be present after insertion")
	}
}

func TestStore_AllowEvictsAtCapWhenAccessTimesTie(t *testing.T) {
	t.Parallel()

	now := time.Unix(0, 0)
	s := NewStoreWithMax(100, 100, 2)
	s.SetNowForTest(func() time.Time { return now })

	s.Allow("key-1")
	s.Allow("key-2")
	s.Allow("key-3")

	s.mu.RLock()
	count := len(s.limiters)
	_, has3 := s.limiters["key-3"]
	s.mu.RUnlock()
	if count != 2 {
		t.Fatalf("limiter count = %d, want cap of 2", count)
	}
	if !has3 {
		t.Fatal("key-3 should be present after insertion")
	}
}

func TestStore_AllowNeverExceedsCap(t *testing.T) {
	t.Parallel()

	const maxKeys = 500
	s := NewStoreWithMax(1e6, 1e6, maxKeys)
	now := time.Unix(0, 0)
	s.SetNowForTest(func() time.Time { return now })

	for i := 0; i < maxKeys*4; i++ {
		now = now.Add(time.Millisecond)
		s.Allow(strconv.Itoa(i))

		s.mu.RLock()
		n := len(s.limiters)
		s.mu.RUnlock()
		if n > maxKeys {
			t.Fatalf("after insert %d: limiter count = %d, want <= %d", i, n, maxKeys)
		}
	}

	// lastSeen must shrink with the limiter map, or the eviction scan walks
	// keys whose limiter is long gone and the store leaks.
	tracked := 0
	s.lastSeen.Range(func(_, _ any) bool {
		tracked++
		return true
	})
	s.mu.RLock()
	n := len(s.limiters)
	s.mu.RUnlock()
	if tracked != n {
		t.Fatalf("lastSeen tracks %d keys, limiter map holds %d", tracked, n)
	}
}

func TestStore_AllowEvictsBatchOfOldest(t *testing.T) {
	t.Parallel()

	// Cap 1000 evicts in batches of 10, so one insert at cap must retire the
	// ten oldest keys and leave every more recent one alone.
	const maxKeys = 1000
	s := NewStoreWithMax(1e6, 1e6, maxKeys)
	now := time.Unix(0, 0)
	s.SetNowForTest(func() time.Time { return now })

	for i := 0; i < maxKeys; i++ {
		s.Allow(strconv.Itoa(i))
		now = now.Add(time.Millisecond)
	}
	s.Allow("newcomer")

	batch := maxKeys / evictBatchDivisor
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := 0; i < batch; i++ {
		if _, ok := s.limiters[strconv.Itoa(i)]; ok {
			t.Errorf("key %d is among the %d oldest and should have been evicted", i, batch)
		}
	}
	for i := batch; i < maxKeys; i++ {
		if _, ok := s.limiters[strconv.Itoa(i)]; !ok {
			t.Errorf("key %d is more recent than the evicted batch and should have been kept", i)
		}
	}
	if _, ok := s.limiters["newcomer"]; !ok {
		t.Error("newcomer should be present after insertion")
	}
}

func BenchmarkStore_AllowInsertAtCap(b *testing.B) {
	// The gateway's default cap. Every iteration inserts a key the store has
	// never seen, which is the shape of traffic from many distinct client IPs.
	const maxKeys = 100_000
	s := NewStoreWithMax(1e9, 1e9, maxKeys)
	for i := 0; i < maxKeys; i++ {
		s.Allow(strconv.Itoa(i))
	}

	keys := make([]string, b.N)
	for i := range keys {
		keys[i] = strconv.Itoa(maxKeys + i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Allow(keys[i])
	}
}

func TestNewStoreWithMax_ZeroIsUnlimited(t *testing.T) {
	t.Parallel()

	s := NewStoreWithMax(100, 100, 0)
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("key-%d", i)
		if !s.Allow(key) {
			t.Fatalf("expected allow for %s", key)
		}
	}
	s.mu.RLock()
	n := len(s.limiters)
	s.mu.RUnlock()
	if n != 50 {
		t.Fatalf("expected 50 limiters, got %d", n)
	}
}

// A bucket that cannot hold one whole token admits nothing, ever: Allow spends
// a whole token and refills clamp at capacity. A rate of 0.5 therefore refused
// every request for the life of the process while reading as a valid config.
func TestNew_FractionalRateAdmitsAtItsRate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rate  float64
		burst float64
		// wait is long enough for this rate to refill one whole token.
		wait time.Duration
	}{
		{"half a request per second", 0.5, 0, 2 * time.Second},
		{"fractional burst", 100, 0.5, 10 * time.Millisecond},
		// A rate this small legitimately admits one request and then makes the
		// caller wait years. The defect was never admitting one at all.
		{"vanishingly small rate", 1e-9, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			l := newLimiter(tc.rate, tc.burst, func() time.Time { return now })

			if !l.Allow() {
				t.Fatal("first request refused; a limiter must admit at least one")
			}
			if tc.wait == 0 {
				return
			}
			if l.Allow() {
				t.Error("second request admitted immediately; the rate is not being applied")
			}
			now = now.Add(tc.wait)
			if !l.Allow() {
				t.Errorf("still refusing after %s; the bucket never refills past its floor", tc.wait)
			}
		})
	}
}
