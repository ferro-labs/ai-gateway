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
	timeout          time.Duration
	openUntil        time.Time
}

// New creates a CircuitBreaker with the given thresholds and open timeout.
// Defaults are applied for zero/negative values: failureThreshold=5,
// successThreshold=1, timeout=30s.
func New(failureThreshold, successThreshold int, timeout time.Duration) *CircuitBreaker {
	if failureThreshold <= 0 {
		failureThreshold = 5
	}
	if successThreshold <= 0 {
		successThreshold = 1
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
		timeout:          timeout,
	}
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
	if cb.state == StateOpen && time.Now().After(cb.openUntil) {
		cb.state = StateHalfOpen
		cb.successCount = 0
	}
	return cb.state
}

// Allow returns true if the request should proceed (circuit is Closed or
// HalfOpen), false if it should be rejected (circuit is Open).
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.resolveState() != StateOpen
}

// RecordSuccess notifies the breaker that a call succeeded.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.state {
	case StateHalfOpen:
		cb.successCount++
		if cb.successCount >= cb.successThreshold {
			cb.state = StateClosed
			cb.failureCount = 0
			cb.successCount = 0
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
			cb.openUntil = time.Now().Add(cb.timeout)
		}
	case StateHalfOpen:
		cb.state = StateOpen
		cb.openUntil = time.Now().Add(cb.timeout)
		cb.successCount = 0
	}
}
