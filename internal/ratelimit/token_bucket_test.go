package ratelimit

import (
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
