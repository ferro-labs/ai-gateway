package strategies

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/pkg/circuitbreaker"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// ── Retry hygiene: default retryable set, full jitter, Retry-After (#278) ──────

func TestNormalizeBackoffMs(t *testing.T) {
	tests := []struct {
		name string
		ms   int
		want int
	}{
		{"zero falls back to the default", 0, defaultBackoffMs},
		{"negative falls back to the default", -5, defaultBackoffMs},
		{"positive value passes through unchanged", 250, 250},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeBackoffMs(tt.ms); got != tt.want {
				t.Errorf("NormalizeBackoffMs(%d) = %d, want %d", tt.ms, got, tt.want)
			}
		})
	}
}

// TestWaitBeforeRetry_NormalizesUnsetBackoff guards the single-source fix for
// the streaming retry regression: an unset InitialBackoffMs (0) must produce
// the same non-zero, jittered-exponential wait that resolveRetry already
// applies for /v1/chat/completions — not an immediate retry.
func TestWaitBeforeRetry_NormalizesUnsetBackoff(t *testing.T) {
	start := time.Now()
	proceed, err := WaitBeforeRetry(context.Background(), 1, 0, errors.New("transient"))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("WaitBeforeRetry error = %v, want nil", err)
	}
	if !proceed {
		t.Fatal("WaitBeforeRetry proceed = false, want true for a plain transient error")
	}
	// attempt=1, normalized backoff=defaultBackoffMs ⇒ exponential ceiling of
	// 100ms; full jitter picks uniformly from [0, ceiling), so this can be 0,
	// but it must never be anywhere near the immediate-retry regression this
	// guards (hundreds of retries with 0ms backoff).
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("WaitBeforeRetry with unset InitialBackoffMs took %v, want within the jittered 100ms ceiling", elapsed)
	}
}

func TestWaitBeforeRetry_AbandonsWhenRetryAfterExceedsCap(t *testing.T) {
	prev := &core.HTTPStatusError{StatusCode: 503, Message: "unavailable", RetryAfter: maxRetryAfter + time.Second}
	proceed, err := WaitBeforeRetry(context.Background(), 1, 0, prev)
	if err != nil {
		t.Fatalf("WaitBeforeRetry error = %v, want nil", err)
	}
	if proceed {
		t.Fatal("WaitBeforeRetry proceed = true, want false when Retry-After exceeds the cap")
	}
}

func TestWaitBeforeRetry_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	proceed, err := WaitBeforeRetry(ctx, 1, 1000, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitBeforeRetry error = %v, want context.Canceled", err)
	}
	if proceed {
		t.Fatal("WaitBeforeRetry proceed = true, want false on cancellation")
	}
}

// upstream builds the typed provider error a real provider returns for an
// upstream status. Every retry decision reads the status through errors.As, so a
// test that hand-formats "provider error (503)" into a plain error is testing
// nothing: it carries no status at all.
func upstream(status int) error {
	return core.APIError("provider", status, []byte(`{"error":{"message":"upstream said no"}}`))
}

func TestShouldRetry_ExportedMatchesInternal(t *testing.T) {
	if !ShouldRetry(upstream(503), nil) {
		t.Error("ShouldRetry(503, nil) = false, want true")
	}
	if ShouldRetry(circuitbreaker.ErrCircuitOpen, nil) {
		t.Error("ShouldRetry(ErrCircuitOpen, nil) = true, want false")
	}
}

func TestShouldRetry_DefaultPolicy(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"408 request timeout is retryable", upstream(408), true},
		{"429 throttling is retryable", upstream(429), true},
		{"500 is retryable", upstream(500), true},
		{"503 is retryable", upstream(503), true},
		{"400 client error is not retryable", upstream(400), false},
		{"401 is not retryable", upstream(401), false},
		{"404 is not retryable", upstream(404), false},
		{"422 is not retryable", upstream(422), false},
		{"transport error carrying no status is retryable", errors.New("connection reset by peer"), true},
		// A status that only ever appeared in the text is not a status. Before the
		// typed contract this scraped to 400 and stopped retrying; the same shape
		// arriving from an SDK scraped to 0 and retried a deterministic failure.
		{"a status in the message alone is not a status", errors.New("provider error (400): bad request"), true},
		{"cancellation is never retryable", context.Canceled, false},
		{"deadline expiry is never retryable", context.DeadlineExceeded, false},
		{"open circuit is never retryable", circuitbreaker.ErrCircuitOpen, false},
		{"saturation is never retryable", core.ErrProviderSaturated, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRetry(tt.err, nil); got != tt.want {
				t.Errorf("shouldRetry = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryDelay_HonorsUpstreamRetryAfter(t *testing.T) {
	prev := &core.HTTPStatusError{StatusCode: 429, Message: "rate limited", RetryAfter: 2 * time.Second}
	if got := retryDelay(1, 100, prev); got != 2*time.Second {
		t.Errorf("retryDelay = %v, want the upstream 2s Retry-After hint", got)
	}
}

func TestRetryDelay_RetryAfterBeyondCapAbandonsTarget(t *testing.T) {
	prev := &core.HTTPStatusError{StatusCode: 503, Message: "unavailable", RetryAfter: maxRetryAfter + time.Second}
	if got := retryDelay(1, 100, prev); got >= 0 {
		t.Errorf("retryDelay = %v, want a negative value signalling the target should be abandoned", got)
	}
}

func TestRetryDelay_FullJitterStaysWithinExponentialAndVaries(t *testing.T) {
	const initialBackoffMs = 100
	// attempt 3 ⇒ exponential ceiling of 100ms · 2^2 = 400ms; full jitter picks
	// uniformly from [0, ceiling).
	const ceiling = 400 * time.Millisecond

	seen := make(map[time.Duration]struct{})
	for range 200 {
		d := retryDelay(3, initialBackoffMs, errors.New("transport failure"))
		if d < 0 || d >= ceiling {
			t.Fatalf("jittered delay %v outside [0, %v)", d, ceiling)
		}
		seen[d] = struct{}{}
	}
	if len(seen) < 2 {
		t.Error("full jitter must vary the delay; got a constant value across 200 samples")
	}
}

// TestFallback_SelectTargets_DeclaredOrder pins the whole of what Fallback now
// decides: the declared order, unfiltered. Retry, skipping an unregistered
// target, advancing past a failure and cancellation all moved to the request
// pipeline, where gateway_pipeline_test.go exercises them once for every mode
// instead of once for this one.
func TestFallback_SelectTargets_DeclaredOrder(t *testing.T) {
	fb := NewFallback([]Target{{VirtualKey: "a"}, {VirtualKey: "b"}, {VirtualKey: "c"}})
	keys, err := fb.SelectTargets(providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	assertKeys(t, keys, "a", "b", "c")
}

func TestFallback_SelectTargets_NoTargets(t *testing.T) {
	keys, err := NewFallback(nil).SelectTargets(providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Errorf("got %v, want no keys", keys)
	}
}

func TestShouldRetry(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		onStatusCodes []int
		want          bool
	}{
		{"empty codes — always retry", upstream(500), nil, true},
		{"matching code", upstream(429), []int{429, 503}, true},
		{"non-matching code", upstream(400), []int{429, 503}, false},
		{"status survives a wrapper", fmt.Errorf("provider openai attempt 2: %w", upstream(400)), []int{429, 503}, false},
		{"no status at all — treat as retryable", errors.New("network timeout"), []int{429}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRetry(tt.err, tt.onStatusCodes)
			if got != tt.want {
				t.Errorf("shouldRetry = %v, want %v", got, tt.want)
			}
		})
	}
	nonRetryable := []error{context.Canceled, context.DeadlineExceeded, circuitbreaker.ErrCircuitOpen, core.ErrProviderSaturated}
	for _, err := range nonRetryable {
		if shouldRetry(err, nil) {
			t.Fatalf("shouldRetry(%v, nil) = true, want false", err)
		}
		if shouldRetry(err, []int{429}) {
			t.Fatalf("shouldRetry(%v, [429]) = true, want false", err)
		}
	}

}
