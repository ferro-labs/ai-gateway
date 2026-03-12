package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestAllowWithinBurst(t *testing.T) {
	l := New(10, 5)
	for i := 0; i < 5; i++ {
		if !l.Allow() {
			t.Fatalf("expected allow on request %d within burst", i+1)
		}
	}
}

func TestBlockWhenDepleted(t *testing.T) {
	l := New(10, 2)
	l.Allow()
	l.Allow()
	if l.Allow() {
		t.Fatal("expected rate limit after burst exhausted")
	}
}

func TestRefillOverTime(t *testing.T) {
	l := New(1000, 1) // 1000 rps, burst 1
	l.Allow()         // exhaust the burst
	time.Sleep(2 * time.Millisecond)
	if !l.Allow() {
		t.Fatal("expected allow after refill")
	}
}

func TestStoreCreatesPerKeyLimiters(t *testing.T) {
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

func TestStoreWithMax_EvictsAtCap(t *testing.T) {
	// Cap at 2 keys. After filling, a new key must evict the oldest.
	s := NewStoreWithMax(100, 100, 2)
	s.Allow("key-1")
	time.Sleep(time.Millisecond) // ensure distinct lastSeen timestamps
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

func TestStoreWithMax_Zero_IsUnlimited(t *testing.T) {
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
