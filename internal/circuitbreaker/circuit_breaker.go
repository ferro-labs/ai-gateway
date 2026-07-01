// Package circuitbreaker implements the circuit-breaker pattern for provider
// calls. Each provider should have its own CircuitBreaker instance.
//
// State transitions:
//
//	Closed → Open        when consecutive failures ≥ FailureThreshold
//	Open   → HalfOpen   after Timeout elapses
//	HalfOpen → Closed   when consecutive successes ≥ SuccessThreshold
//	HalfOpen → Open     on any failure
package circuitbreaker

import (
	"errors"
	"sync"
	"time"
)

// State represents the circuit breaker's current state.
type State int

const (
	// StateClosed — normal operation; requests pass through.
	StateClosed State = iota
	// StateOpen — provider is considered failing; requests are rejected immediately.
	StateOpen
	// StateHalfOpen — circuit is testing recovery with a limited number of requests.
	StateHalfOpen
)

// String implements fmt.Stringer.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// ErrCircuitOpen is returned when a call is rejected because the circuit is open.
var ErrCircuitOpen = errors.New("circuit breaker open")

// CircuitBreaker guards a single downstream provider.
type CircuitBreaker struct {
	mu               sync.Mutex
	state            State
	failureCount     int
	successCount     int
	failureThreshold int
	successThreshold int
	maxHalfThreshold int // cap on concurrent in-flight probes while half-open
	halfOpenProbes   int // current number of in-flight probes
	timeout          time.Duration
	openUntil        time.Time
	now              func() time.Time // clock seam; defaults to time.Now, overridable in tests
}

// New creates a CircuitBreaker with the given thresholds and open timeout.
// Defaults are applied for zero/negative values: failureThreshold=5,
// successThreshold=1, timeout=30s.
func New(failureThreshold, successThreshold int, maxHalfThreshold int, timeout time.Duration) *CircuitBreaker {
	if failureThreshold <= 0 {
		failureThreshold = 5
	}
	if successThreshold <= 0 {
		successThreshold = 1
	}
	if maxHalfThreshold <= 0 {
		maxHalfThreshold = 1
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
		maxHalfThreshold: maxHalfThreshold,
		timeout:          timeout,
		now:              time.Now,
	}
}

// SetNowForTest overrides the internal clock used for Open→HalfOpen timeout
// transitions so tests (including those in other packages) can advance virtual
// time deterministically instead of sleeping. Passing nil restores time.Now.
func (cb *CircuitBreaker) SetNowForTest(fn func() time.Time) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if fn == nil {
		fn = time.Now
	}
	cb.now = fn
}

// State returns the current state, transitioning Open→HalfOpen if the timeout
// has elapsed.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.resolveState()
}

// resolveState must be called with cb.mu held.
func (cb *CircuitBreaker) resolveState() State {
	if cb.state == StateOpen && cb.now().After(cb.openUntil) {
		cb.state = StateHalfOpen
		cb.successCount = 0
		cb.halfOpenProbes = 0
	}
	return cb.state
}

// Allow returns true if the request should proceed (circuit is Closed or
// HalfOpen), false if it should be rejected (circuit is Open).
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.resolveState() == StateOpen {
		return false
	}
	if cb.state == StateHalfOpen {
		if cb.halfOpenProbes >= cb.maxHalfThreshold {
			return false
		}
		cb.halfOpenProbes++
	}
	return true
}

// ReleaseProbe releases an admitted half-open probe without recording success
// or failure. It is used when the gateway intentionally ignores an outcome,
// such as rate limits or caller-side cancellation.
func (cb *CircuitBreaker) ReleaseProbe() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == StateHalfOpen && cb.halfOpenProbes > 0 {
		cb.halfOpenProbes--
	}
}

// RecordSuccess notifies the breaker that a call succeeded.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case StateHalfOpen:
		if cb.halfOpenProbes > 0 {
			cb.halfOpenProbes--
		}
		cb.successCount++
		if cb.successCount >= cb.successThreshold {
			cb.state = StateClosed
			cb.failureCount = 0
			cb.successCount = 0
			cb.halfOpenProbes = 0
		}
	case StateClosed:
		cb.failureCount = 0
	}
}

// RecordFailure notifies the breaker that a call failed.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case StateClosed:
		cb.failureCount++
		if cb.failureCount >= cb.failureThreshold {
			cb.state = StateOpen
			cb.openUntil = cb.now().Add(cb.timeout)
		}
	case StateHalfOpen:
		cb.state = StateOpen
		cb.openUntil = cb.now().Add(cb.timeout)
		cb.successCount = 0
		cb.halfOpenProbes = 0
	}
}
