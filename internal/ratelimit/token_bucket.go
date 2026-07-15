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
	now        func() time.Time
}

// New creates a Limiter allowing ratePerSecond requests/s with a burst capacity.
// If burst <= 0, it defaults to ratePerSecond (no extra burst).
func New(ratePerSecond, burst float64) *Limiter {
	return newLimiter(ratePerSecond, burst, time.Now)
}

func newLimiter(ratePerSecond, burst float64, now func() time.Time) *Limiter {
	if burst <= 0 {
		burst = ratePerSecond
	}
	return &Limiter{
		rate:       ratePerSecond,
		burst:      burst,
		tokens:     burst,
		lastRefill: now(),
		now:        now,
	}
}

// SetNowForTest overrides the clock used for token refills. Passing nil
// restores time.Now.
func (l *Limiter) SetNowForTest(fn func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if fn == nil {
		fn = time.Now
	}
	l.now = fn
	l.lastRefill = fn()
}

// Allow consumes one token and returns true if the request is permitted.
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
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

// Store maintains per-key Limiter instances with an optional max-size cap.
// When maxKeys > 0, inserting a new key that would exceed the cap evicts the
// least recently accessed entry, preventing unbounded memory growth.
type Store struct {
	mu       sync.RWMutex
	limiters map[string]*Limiter
	lastSeen sync.Map // map[string]time.Time — updated on every access
	rate     float64
	burst    float64
	maxKeys  int // 0 = unlimited
	now      func() time.Time
}

// NewStore creates a Store whose per-key limiters share the same rate/burst.
func NewStore(ratePerSecond, burst float64) *Store {
	return &Store{
		limiters: make(map[string]*Limiter),
		rate:     ratePerSecond,
		burst:    burst,
		now:      time.Now,
	}
}

// SetNowForTest overrides the clock used for access ordering and token
// refills. Passing nil restores time.Now.
func (s *Store) SetNowForTest(fn func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn == nil {
		fn = time.Now
	}
	s.now = fn
	for _, limiter := range s.limiters {
		limiter.SetNowForTest(fn)
	}
}

// NewStoreWithMax creates a Store like NewStore but caps the number of tracked
// keys at maxKeys. When the cap is reached, a new key causes the least recently
// accessed entry to be evicted. Use maxKeys=0 for unlimited (same as NewStore).
func NewStoreWithMax(ratePerSecond, burst float64, maxKeys int) *Store {
	s := NewStore(ratePerSecond, burst)
	s.maxKeys = maxKeys
	return s
}

// Allow checks (and creates if needed) the limiter for key.
func (s *Store) Allow(key string) bool {
	// Fast path — limiter already exists.
	s.mu.RLock()
	l, ok := s.limiters[key]
	now := s.now()
	s.mu.RUnlock()
	if ok {
		s.lastSeen.Store(key, now)
		return l.Allow()
	}

	// Slow path — create new limiter.
	s.mu.Lock()
	defer s.mu.Unlock()
	now = s.now()
	// Double-check after acquiring write lock.
	if l, ok = s.limiters[key]; ok {
		s.lastSeen.Store(key, now)
		return l.Allow()
	}
	// Evict least recently seen entry when at cap.
	if s.maxKeys > 0 && len(s.limiters) >= s.maxKeys {
		oldest := ""
		var oldestTime time.Time
		s.lastSeen.Range(func(k, v any) bool {
			t := v.(time.Time) //nolint:forcetypeassert // lastSeen only ever stores time.Time values
			if oldest == "" || t.Before(oldestTime) {
				oldest = k.(string) //nolint:forcetypeassert // lastSeen keys are always string
				oldestTime = t
			}
			return true
		})
		if oldest != "" {
			delete(s.limiters, oldest)
			s.lastSeen.Delete(oldest)
		}
	}
	l = newLimiter(s.rate, s.burst, s.now)
	s.limiters[key] = l
	s.lastSeen.Store(key, now)
	return l.Allow()
}
