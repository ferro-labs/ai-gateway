package aigateway

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// chainGateway routes pipelineModel through a conditional rule whose chain is
// [a, b]; c is a configured target the rule does not name.
func ruleChainGateway(t *testing.T, targets []config.Target) *Gateway {
	t.Helper()
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode:       config.ModeConditional,
			Conditions: []config.Condition{{Key: config.ConditionKeyModel, Value: pipelineModel, TargetKeys: []string{"a", "b"}}},
		},
		Targets: targets,
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	return gw
}

func serving(id string) func() (*providers.Response, error) {
	return func() (*providers.Response, error) { return &providers.Response{ID: id, Model: pipelineModel}, nil }
}

func failing(status int) func() (*providers.Response, error) {
	return func() (*providers.Response, error) { return nil, core.StatusError("t", status, "boom") }
}

// TestChain_RetryBeforeAdvance: a chain member's own retry budget is spent
// before the walk moves to the next member.
func TestChain_RetryBeforeAdvance(t *testing.T) {
	gw := ruleChainGateway(t, []config.Target{
		{VirtualKey: "a", Retry: &config.RetryConfig{Attempts: 2}}, {VirtualKey: "b"}, {VirtualKey: "c"},
	})
	a := newCountingProvider("a", failing(http.StatusServiceUnavailable))
	b := newCountingProvider("b", serving("served-by-b"))
	c := newCountingProvider("c", serving("served-by-c"))
	gw.RegisterProvider(a)
	gw.RegisterProvider(b)
	gw.RegisterProvider(c)

	resp, err := gw.Route(context.Background(), pipelineRequest())
	if err != nil || resp.ID != "served-by-b" {
		t.Fatalf("resp=%v err=%v, want b after a's retries", resp, err)
	}
	if a.calls.Load() != 2 || c.calls.Load() != 0 {
		t.Fatalf("a=%d c=%d calls, want a retried twice and c never asked", a.calls.Load(), c.calls.Load())
	}
}

// TestChain_NoEscapeOutsideTheChain: when every member fails the request
// fails; a target the rule did not name is never asked.
func TestChain_NoEscapeOutsideTheChain(t *testing.T) {
	gw := ruleChainGateway(t, []config.Target{{VirtualKey: "a"}, {VirtualKey: "b"}, {VirtualKey: "c"}})
	gw.RegisterProvider(newCountingProvider("a", failing(http.StatusBadGateway)))
	gw.RegisterProvider(newCountingProvider("b", failing(http.StatusBadGateway)))
	c := newCountingProvider("c", serving("served-by-c"))
	gw.RegisterProvider(c)

	if _, err := gw.Route(context.Background(), pipelineRequest()); err == nil {
		t.Fatal("Route succeeded; every chain member failed")
	}
	if c.calls.Load() != 0 {
		t.Fatalf("c was asked %d times; it is outside the chain", c.calls.Load())
	}
}

// TestChain_SkipsUnavailableMembersInsideTheChain: an open circuit or a parked
// member is passed over for the next member, never for a target outside.
func TestChain_SkipsUnavailableMembersInsideTheChain(t *testing.T) {
	gw := ruleChainGateway(t, []config.Target{openableTarget("a"), {VirtualKey: "b"}, {VirtualKey: "c"}})
	a := newCountingProvider("a", failing(http.StatusInternalServerError))
	b := newCountingProvider("b", serving("served-by-b"))
	c := newCountingProvider("c", serving("served-by-c"))
	gw.RegisterProvider(a)
	gw.RegisterProvider(b)
	gw.RegisterProvider(c)
	tripBreaker(t, gw, "a")
	before := a.calls.Load()

	resp, err := gw.Route(context.Background(), pipelineRequest())
	if err != nil || resp.ID != "served-by-b" {
		t.Fatalf("resp=%v err=%v, want b while a's circuit is open", resp, err)
	}
	if a.calls.Load() != before || c.calls.Load() != 0 {
		t.Fatalf("a=%d (was %d) c=%d calls; the open member is skipped, not called, and c is outside", a.calls.Load(), before, c.calls.Load())
	}

}

// TestChain_SkipsParkedMembersInsideTheChain: a member parked after a 429 is
// passed over the same way an open circuit is.
func TestChain_SkipsParkedMembersInsideTheChain(t *testing.T) {
	gw := ruleChainGateway(t, []config.Target{{VirtualKey: "a"}, {VirtualKey: "b"}, {VirtualKey: "c"}})
	a := newCountingProvider("a", serving("served-by-a"))
	b := newCountingProvider("b", serving("served-by-b"))
	c := newCountingProvider("c", serving("served-by-c"))
	gw.RegisterProvider(a)
	gw.RegisterProvider(b)
	gw.RegisterProvider(c)
	gw.parkRateLimited("a", nil)

	resp, err := gw.Route(context.Background(), pipelineRequest())
	if err != nil || resp.ID != "served-by-b" {
		t.Fatalf("resp=%v err=%v, want b while a is parked", resp, err)
	}
	if a.calls.Load() != 0 || c.calls.Load() != 0 {
		t.Fatalf("a=%d c=%d calls; the parked member is skipped and c is outside the chain", a.calls.Load(), c.calls.Load())
	}
}

// TestChain_DeterministicClientErrorStops: a 400 from a member is the answer;
// the next member is not asked, since the request itself is at fault.
func TestChain_DeterministicClientErrorStops(t *testing.T) {
	gw := ruleChainGateway(t, []config.Target{{VirtualKey: "a"}, {VirtualKey: "b"}, {VirtualKey: "c"}})
	gw.RegisterProvider(newCountingProvider("a", failing(http.StatusBadRequest)))
	b := newCountingProvider("b", serving("served-by-b"))
	gw.RegisterProvider(b)
	gw.RegisterProvider(newCountingProvider("c", serving("served-by-c")))

	_, err := gw.Route(context.Background(), pipelineRequest())
	if status, _, _ := apierror.RouteErrorDetails(err); status != http.StatusBadRequest {
		t.Fatalf("status %d (%v), want the member's 400", status, err)
	}
	if b.calls.Load() != 0 {
		t.Fatalf("b was asked %d times after a deterministic 4xx", b.calls.Load())
	}
}

// TestChain_CallerCancellationStops: once the caller has hung up nothing
// advances, inside a chain as anywhere else.
func TestChain_CallerCancellationStops(t *testing.T) {
	gw := ruleChainGateway(t, []config.Target{{VirtualKey: "a"}, {VirtualKey: "b"}, {VirtualKey: "c"}})
	ctx, cancel := context.WithCancel(context.Background())
	gw.RegisterProvider(newCountingProvider("a", func() (*providers.Response, error) {
		cancel()
		return nil, errors.New("connection reset")
	}))
	b := newCountingProvider("b", serving("served-by-b"))
	gw.RegisterProvider(b)
	gw.RegisterProvider(newCountingProvider("c", serving("served-by-c")))

	if _, err := gw.Route(ctx, pipelineRequest()); err == nil {
		t.Fatal("Route succeeded after the caller cancelled")
	}
	if b.calls.Load() != 0 {
		t.Fatalf("b was asked %d times after cancellation", b.calls.Load())
	}
}

// TestChain_NoMidStreamFailover: a stream that fails after it began ends with
// its error; the next chain member is not started, since a half-delivered
// stream cannot be replayed.
func TestChain_NoMidStreamFailover(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode:              config.ModeContentBased,
			ContentConditions: []config.ContentCondition{{Type: config.ContentConditionPromptContains, Value: "hi", TargetKeys: []string{"a", "b"}}},
		},
		Targets: []config.Target{{VirtualKey: "a"}, {VirtualKey: "b"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockStreamProvider{
		mockProvider: mockProvider{name: "a", models: []string{"gpt-4o"}},
		streamFn: func(context.Context, providers.Request) (<-chan providers.StreamChunk, error) {
			ch := make(chan providers.StreamChunk, 2)
			ch <- providers.StreamChunk{ID: "c1"}
			ch <- providers.StreamChunk{Error: errors.New("upstream reset mid-stream")}
			close(ch)
			return ch, nil
		},
	})
	b := newCountingStreamProvider("b", func(context.Context) (<-chan providers.StreamChunk, error) {
		ch := make(chan providers.StreamChunk)
		close(ch)
		return ch, nil
	})
	gw.RegisterProvider(b)

	ch, err := gw.RouteStream(context.Background(), providers.Request{Model: "gpt-4o", Messages: []providers.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("RouteStream: %v", err)
	}
	var sawError bool
	for chunk := range ch {
		if chunk.Error != nil {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("the mid-stream failure must reach the client")
	}
	if b.calls.Load() != 0 {
		t.Fatalf("b was started %d times after a mid-stream failure", b.calls.Load())
	}
}
