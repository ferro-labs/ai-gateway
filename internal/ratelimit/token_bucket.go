// Package ratelimit provides a simple in-memory token-bucket rate limiter.
// It is used both as a standalone HTTP middleware (rate-limit by IP or API key)
// and by the rate-limit plugin (per-provider limiting).
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a single token-bucket rate limiter.
type Limiter struct {
	mu         sync.Mutex
	rate       float64 // tokens added per second
	burst      float64 // maximum token capacity
	tokens     float64 // current token count
	lastRefill time.Time
}

// New creates a Limiter allowing ratePerSecond requests/s with a burst capacity.
// If burst <= 0, it defaults to ratePerSecond (no extra burst).
func New(ratePerSecond, burst float64) *Limiter {
	if burst <= 0 {
		burst = ratePerSecond
	}
	return &Limiter{
		rate:       ratePerSecond,
		burst:      burst,
		tokens:     burst,
		lastRefill: time.Now(),
	}
}

// Allow consumes one token and returns true if the request is permitted.
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(l.lastRefill).Seconds()
	l.tokens += elapsed * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.lastRefill = now

	if l.tokens >= 1.0 {
		l.tokens--
		return true
	}
	return false
}

// Store maintains per-key Limiter instances.
type Store struct {
	mu       sync.RWMutex
	limiters map[string]*Limiter
	rate     float64
	burst    float64
}

// NewStore creates a Store whose per-key limiters share the same rate/burst.
func NewStore(ratePerSecond, burst float64) *Store {
	return &Store{
		limiters: make(map[string]*Limiter),
		rate:     ratePerSecond,
		burst:    burst,
	}
}

// Allow checks (and creates if needed) the limiter for key.
func (s *Store) Allow(key string) bool {
	// Fast path — limiter already exists.
	s.mu.RLock()
	l, ok := s.limiters[key]
	s.mu.RUnlock()
	if ok {
		return l.Allow()
	}

	// Slow path — create new limiter.
	s.mu.Lock()
	defer s.mu.Unlock()
	// Double-check after acquiring write lock.
	if l, ok = s.limiters[key]; ok {
		return l.Allow()
	}
	l = New(s.rate, s.burst)
	s.limiters[key] = l
	return l.Allow()
}
