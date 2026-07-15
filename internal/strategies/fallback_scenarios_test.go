package strategies

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestFallback_FirstSucceeds(t *testing.T) {
	mp := &mockProvider{name: "a", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "a-ok"}}
	f := NewFallback([]Target{{VirtualKey: "a"}}, newLookup(mp))

	resp, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "a-ok" {
		t.Errorf("got %q", resp.ID)
	}
}

func TestFallback_FallsToSecond(t *testing.T) {
	bad := &mockProvider{name: "bad", models: []string{"gpt-4o"}, err: fmt.Errorf("down")}
	good := &mockProvider{name: "good", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "recovered"}}

	f := NewFallback(
		[]Target{{VirtualKey: "bad"}, {VirtualKey: "good"}},
		newLookup(bad, good),
	)

	resp, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "recovered" {
		t.Errorf("got %q, want recovered", resp.ID)
	}
}

func TestFallback_AllFail(t *testing.T) {
	bad1 := &mockProvider{name: "a", models: []string{"gpt-4o"}, err: fmt.Errorf("fail1")}
	bad2 := &mockProvider{name: "b", models: []string{"gpt-4o"}, err: fmt.Errorf("fail2")}

	f := NewFallback(
		[]Target{{VirtualKey: "a"}, {VirtualKey: "b"}},
		newLookup(bad1, bad2),
	)

	_, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
}

func TestFallback_NoTargets(t *testing.T) {
	f := NewFallback(nil, newLookup())
	_, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error for no targets")
	}
}

func TestFallback_SkipsUnsupportedModel(t *testing.T) {
	// First provider doesn't support the model, second does.
	wrong := &mockProvider{name: "wrong", models: []string{"claude-3"}, resp: &providers.Response{ID: "wrong"}}
	right := &mockProvider{name: "right", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "right"}}

	f := NewFallback(
		[]Target{{VirtualKey: "wrong"}, {VirtualKey: "right"}},
		newLookup(wrong, right),
	)

	resp, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "right" {
		t.Errorf("expected right, got %s", resp.ID)
	}
	if wrong.calls != 0 {
		t.Error("unsupported provider should not have been called")
	}
}

func TestFallback_WithMaxRetries(t *testing.T) {
	// Provider fails on first 2 attempts, never succeeds. With 3 retries, all 3 attempts are made.
	bad := &mockProvider{name: "a", models: []string{"gpt-4o"}, err: fmt.Errorf("fail")}

	f := NewFallback(
		[]Target{{VirtualKey: "a"}},
		newLookup(bad),
	).WithMaxRetries(3)

	_, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	if bad.calls != 3 {
		t.Errorf("expected 3 retry attempts, got %d", bad.calls)
	}
}

func TestFallback_SkipsMissingProvider(t *testing.T) {
	good := &mockProvider{name: "good", models: []string{"gpt-4o"}, resp: &providers.Response{ID: "good"}}

	// First target's provider is not registered, should skip to second.
	f := NewFallback(
		[]Target{{VirtualKey: "missing"}, {VirtualKey: "good"}},
		newLookup(good),
	)

	resp, err := f.Execute(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "good" {
		t.Errorf("expected good, got %s", resp.ID)
	}
}

func TestFallback_ContextCancelled(t *testing.T) {
	bad := &mockProvider{name: "a", models: []string{"gpt-4o"}, err: fmt.Errorf("fail")}

	f := NewFallback(
		[]Target{{VirtualKey: "a"}},
		newLookup(bad),
	).WithMaxRetries(5)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := f.Execute(ctx, providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if bad.calls != 0 {
		t.Fatalf("cancelled context should not call provider, got %d calls", bad.calls)
	}
}
