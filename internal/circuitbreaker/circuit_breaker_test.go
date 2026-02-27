package circuitbreaker

import (
	"testing"
	"time"
)

func TestInitialStateClosed(t *testing.T) {
	cb := New(3, 1, 10*time.Second)
	if cb.State() != StateClosed {
		t.Fatalf("expected closed, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Fatal("expected Allow=true when closed")
	}
}

func TestOpensAfterThreshold(t *testing.T) {
	cb := New(3, 1, 10*time.Second)
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}
	if cb.State() != StateOpen {
		t.Fatalf("expected open after 3 failures, got %s", cb.State())
	}
	if cb.Allow() {
		t.Fatal("expected Allow=false when open")
	}
}

func TestTransitionsToHalfOpenAfterTimeout(t *testing.T) {
	cb := New(1, 1, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	if cb.State() != StateHalfOpen {
		t.Fatalf("expected half_open after timeout, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Fatal("expected Allow=true when half_open")
	}
}

func TestClosesAfterSuccessInHalfOpen(t *testing.T) {
	cb := New(1, 1, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State() // trigger half-open transition
	cb.RecordSuccess()
	if cb.State() != StateClosed {
		t.Fatalf("expected closed after success in half_open, got %s", cb.State())
	}
}

func TestReopensOnFailureInHalfOpen(t *testing.T) {
	cb := New(1, 1, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State() // trigger half-open transition
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Fatalf("expected open after failure in half_open, got %s", cb.State())
	}
}

func TestSuccessResetFailureCount(t *testing.T) {
	cb := New(3, 1, 10*time.Second)
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess()
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != StateClosed {
		t.Fatalf("expected still closed (failure count reset), got %s", cb.State())
	}
}
