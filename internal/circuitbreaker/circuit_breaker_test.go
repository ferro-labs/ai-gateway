package circuitbreaker

import (
	"testing"
	"time"
)

func TestInitialStateClosed(t *testing.T) {
	cb := New(3, 1, 1, 10*time.Second)
	if cb.State() != StateClosed {
		t.Fatalf("expected closed, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Fatal("expected Allow=true when closed")
	}
}

func TestOpensAfterThreshold(t *testing.T) {
	cb := New(3, 1, 1, 10*time.Second)
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
	cb := New(1, 1, 1, 1*time.Millisecond)
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
	cb := New(1, 1, 1, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State() // trigger half-open transition
	cb.Allow()
	cb.RecordSuccess()
	if cb.State() != StateClosed {
		t.Fatalf("expected closed after success in half_open, got %s", cb.State())
	}
}

func TestReopensOnFailureInHalfOpen(t *testing.T) {
	cb := New(1, 1, 1, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State() // trigger half-open transition
	cb.Allow()
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Fatalf("expected open after failure in half_open, got %s", cb.State())
	}
}

func TestSuccessResetFailureCount(t *testing.T) {
	cb := New(3, 1, 1, 10*time.Second)
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess()
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != StateClosed {
		t.Fatalf("expected still closed (failure count reset), got %s", cb.State())
	}
}

func TestHalfOpenProbeCap(t *testing.T) {
	cb := New(1, 1, 2, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State() // trigger half-open transition

	// First two probes should be allowed (cap = 2)
	if !cb.Allow() {
		t.Fatal("expected first probe allowed")
	}
	if !cb.Allow() {
		t.Fatal("expected second probe allowed")
	}
	// Third probe must be rejected — cap reached
	if cb.Allow() {
		t.Fatal("expected third probe rejected, cap is 2")
	}

	// After one probe completes successfully, a slot opens
	cb.RecordSuccess()
	if !cb.Allow() {
		t.Fatal("expected probe allowed after slot freed by RecordSuccess")
	}
}

func TestHalfOpenProbeCapReleasedOnFailure(t *testing.T) {
	// cap=2: fill both slots, then one probe fails; after re-entering half-open
	// both slots must be available again, proving RecordFailure decremented before reset.
	cb := New(1, 1, 2, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State()

	if !cb.Allow() {
		t.Fatal("expected first probe allowed")
	}
	if !cb.Allow() {
		t.Fatal("expected second probe allowed")
	}
	if cb.Allow() {
		t.Fatal("expected third probe rejected, cap is 2")
	}

	cb.RecordFailure() // one probe fails → circuit reopens, halfOpenProbes decremented then zeroed

	if cb.State() != StateOpen {
		t.Fatalf("expected open after failure, got %s", cb.State())
	}

	time.Sleep(5 * time.Millisecond)
	_ = cb.State() // re-enter half-open

	// Both slots must be free — counter was cleanly reset by RecordFailure
	if !cb.Allow() {
		t.Fatal("expected slot 1 available after re-entering half-open")
	}
	if !cb.Allow() {
		t.Fatal("expected slot 2 available after re-entering half-open")
	}
	if cb.Allow() {
		t.Fatal("expected third probe rejected, cap still 2")
	}
}

func TestHalfOpenProbesResetOnReopen(t *testing.T) {
	cb := New(1, 2, 1, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State() // trigger half-open transition

	cb.Allow()
	cb.RecordFailure() // probe fails → back to open

	if cb.State() != StateOpen {
		t.Fatalf("expected open, got %s", cb.State())
	}
	// Simulate timeout expiry and re-entry into half-open
	time.Sleep(5 * time.Millisecond)
	_ = cb.State() // transition to half-open again

	// Probe counter should be reset — first probe must be allowed
	if !cb.Allow() {
		t.Fatal("expected Allow=true after re-entering half-open")
	}
}

// TestDefaultMaxHalfThreshold verifies that passing maxHalfThreshold=0 is
// normalized to 1, so a second concurrent probe is rejected.
func TestDefaultMaxHalfThreshold(t *testing.T) {
	cb := New(1, 1, 0, 1*time.Millisecond) // 0 → normalized to 1
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State()

	if !cb.Allow() {
		t.Fatal("expected first probe allowed with default cap 1")
	}
	if cb.Allow() {
		t.Fatal("expected second probe rejected: default cap is 1, not unlimited")
	}
}

func TestReleaseProbeFreesHalfOpenSlotWithoutRecordingOutcome(t *testing.T) {
	cb := New(1, 1, 1, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State()

	if !cb.Allow() {
		t.Fatal("expected first half-open probe allowed")
	}
	if cb.Allow() {
		t.Fatal("expected second half-open probe rejected before release")
	}

	cb.ReleaseProbe()

	if cb.State() != StateHalfOpen {
		t.Fatalf("expected release to keep half-open state, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Fatal("expected released half-open slot to admit another probe")
	}
}

// TestHalfOpenSuccessThresholdWithSlotRecycling verifies that when
// successThreshold > 1, each RecordSuccess frees a probe slot so that new
// probes can be admitted before the circuit closes.
func TestHalfOpenSuccessThresholdWithSlotRecycling(t *testing.T) {
	// cap=2, need 2 successes to close
	cb := New(1, 2, 2, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State()

	if !cb.Allow() {
		t.Fatal("expected probe 1 allowed")
	}
	if !cb.Allow() {
		t.Fatal("expected probe 2 allowed")
	}
	if cb.Allow() {
		t.Fatal("expected probe 3 rejected: cap reached")
	}

	// Probe 1 succeeds: slot freed, successCount=1 (not yet at threshold=2)
	cb.RecordSuccess()
	if cb.State() != StateHalfOpen {
		t.Fatalf("expected still half_open after first success, got %s", cb.State())
	}

	// Freed slot allows a new probe
	if !cb.Allow() {
		t.Fatal("expected new probe allowed after slot freed by first RecordSuccess")
	}
	if cb.Allow() {
		t.Fatal("expected cap enforced again after refill")
	}

	// Second success closes the circuit
	cb.RecordSuccess()
	if cb.State() != StateClosed {
		t.Fatalf("expected closed after second success, got %s", cb.State())
	}
}

// TestRecordSuccessAfterConcurrentReopenIsNoop verifies that a late
// RecordSuccess arriving after a concurrent probe already reopened the circuit
// does not corrupt state.
func TestRecordSuccessAfterConcurrentReopenIsNoop(t *testing.T) {
	// cap=2: probe A and probe B both admitted; probe B fails first (reopens);
	// probe A's RecordSuccess arrives after the reopen.
	cb := New(1, 1, 2, 1*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State()

	cb.Allow() // probe A admitted
	cb.Allow() // probe B admitted

	cb.RecordFailure() // probe B fails → circuit reopens, halfOpenProbes zeroed

	if cb.State() != StateOpen {
		t.Fatalf("expected open after probe B failure, got %s", cb.State())
	}

	// Probe A's result arrives late — circuit is already Open
	cb.RecordSuccess() // must be a silent no-op

	if cb.State() != StateOpen {
		t.Fatalf("expected still open after stale RecordSuccess, got %s", cb.State())
	}

	// After timeout the circuit should re-enter half-open cleanly
	time.Sleep(5 * time.Millisecond)
	if cb.State() != StateHalfOpen {
		t.Fatalf("expected half_open after timeout, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Fatal("expected probe slot available after clean re-entry into half_open")
	}
}

// TestHalfOpenProbesZeroOnClosedToOpenTransition asserts that halfOpenProbes
// is never left non-zero when transitioning Closed → Open, ensuring subsequent
// half-open entry always starts with a clean counter.
func TestHalfOpenProbesZeroOnClosedToOpenTransition(t *testing.T) {
	cb := New(1, 1, 1, 1*time.Millisecond)

	// Drive through a full cycle: closed→open→half-open→closed
	cb.RecordFailure()
	time.Sleep(5 * time.Millisecond)
	_ = cb.State()     // →half-open
	cb.Allow()         // probe admitted (halfOpenProbes=1)
	cb.RecordSuccess() // closes (halfOpenProbes zeroed)

	if cb.State() != StateClosed {
		t.Fatalf("expected closed, got %s", cb.State())
	}

	// Now open again from closed — probe counter must still be 0
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Fatalf("expected open, got %s", cb.State())
	}

	// Re-enter half-open: both slots must be free
	time.Sleep(5 * time.Millisecond)
	_ = cb.State()
	if !cb.Allow() {
		t.Fatal("expected probe allowed: halfOpenProbes must be 0 after Closed→Open transition")
	}
}
