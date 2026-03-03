package strategies

import (
	"context"
	"fmt"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

// ── Retry policy tests ────────────────────────────────────────────────────────

// errProvider returns an error on every call with the given formatted message.
type errProvider struct {
	name   string
	models []string
	errMsg string
	calls  int
}

func (e *errProvider) Name() string                  { return e.name }
func (e *errProvider) SupportedModels() []string     { return e.models }
func (e *errProvider) Models() []providers.ModelInfo { return nil }
func (e *errProvider) SupportsModel(m string) bool {
	for _, mm := range e.models {
		if mm == m {
			return true
		}
	}
	return false
}
func (e *errProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	e.calls++
	return nil, fmt.Errorf("%s", e.errMsg)
}

func TestFallback_PerTargetRetry_Attempts(t *testing.T) {
	ep := &errProvider{
		name:   "openai",
		models: []string{"gpt-4o"},
		errMsg: "provider error (429): rate limited",
	}
	targets := []Target{{VirtualKey: "openai"}}
	fb := NewFallback(targets, newLookup(ep)).
		WithTargetRetry("openai", 3, nil, 0)

	fb.Execute(context.Background(), providers.Request{Model: "gpt-4o"}) //nolint:errcheck,gosec

	if ep.calls != 3 {
		t.Errorf("expected 3 attempts, got %d", ep.calls)
	}
}

func TestFallback_StatusCodeFilter_RetriesOnAllowedCode(t *testing.T) {
	ep := &errProvider{
		name:   "openai",
		models: []string{"gpt-4o"},
		errMsg: "provider error (429): rate limited",
	}
	targets := []Target{{VirtualKey: "openai"}}
	fb := NewFallback(targets, newLookup(ep)).
		WithTargetRetry("openai", 3, []int{429}, 0)

	fb.Execute(context.Background(), providers.Request{Model: "gpt-4o"}) //nolint:errcheck,gosec

	if ep.calls != 3 {
		t.Errorf("expected 3 attempts for allowed 429, got %d", ep.calls)
	}
}

func TestFallback_StatusCodeFilter_StopsOnDisallowedCode(t *testing.T) {
	ep := &errProvider{
		name:   "openai",
		models: []string{"gpt-4o"},
		errMsg: "provider error (400): bad request",
	}
	targets := []Target{{VirtualKey: "openai"}}
	// Only retry on 429 — 400 should stop immediately.
	fb := NewFallback(targets, newLookup(ep)).
		WithTargetRetry("openai", 3, []int{429}, 0)

	fb.Execute(context.Background(), providers.Request{Model: "gpt-4o"}) //nolint:errcheck,gosec

	if ep.calls != 1 {
		t.Errorf("expected 1 attempt for disallowed 400, got %d", ep.calls)
	}
}

func TestFallback_FallsThrough_ToNextTarget(t *testing.T) {
	ep1 := &errProvider{name: "p1", models: []string{"gpt-4o"}, errMsg: "provider error (500): oops"}
	ep2 := &mockProvider{name: "p2", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "ok"}}

	targets := []Target{{VirtualKey: "p1"}, {VirtualKey: "p2"}}
	fb := NewFallback(targets, newLookup(ep1, ep2))

	resp, err := fb.Execute(context.Background(), providers.Request{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("expected success from p2, got error: %v", err)
	}
	if resp.ID != "ok" {
		t.Errorf("got %q, want ok", resp.ID)
	}
}

func TestFallback_NoTargets_RetryPolicy(t *testing.T) {
	fb := NewFallback(nil, newLookup())
	_, err := fb.Execute(context.Background(), providers.Request{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error for no targets")
	}
}

func TestShouldRetry(t *testing.T) {
	tests := []struct {
		name          string
		errMsg        string
		onStatusCodes []int
		want          bool
	}{
		{"empty codes — always retry", "error (500): oops", nil, true},
		{"matching code", "provider error (429): limit", []int{429, 503}, true},
		{"non-matching code", "provider error (400): bad", []int{429, 503}, false},
		{"no parseable code — treat as retryable", "network timeout", []int{429}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRetry(fmt.Errorf("%s", tt.errMsg), tt.onStatusCodes)
			if got != tt.want {
				t.Errorf("shouldRetry = %v, want %v", got, tt.want)
			}
		})
	}
}
