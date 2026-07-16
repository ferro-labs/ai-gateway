package ratelimit

import (
	"fmt"
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
