package aigateway

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

var testIdentity = observability.RequestIdentity{
	User:      "user-42",
	SessionID: "sess-7",
	Metadata:  map[string]string{"team": "search"},
}

func identityContext(t *testing.T) context.Context {
	t.Helper()
	ctx := logger.WithTraceID(context.Background(), "0123456789abcdef0123456789abcdef")
	return observability.ContextWithRequestIdentity(ctx, testIdentity)
}

func assertEventIdentity(t *testing.T, label string, user, sessionID string, metadata map[string]string) {
	t.Helper()
	if user != testIdentity.User || sessionID != testIdentity.SessionID || metadata["team"] != "search" {
		t.Errorf("%s identity = user %q session %q metadata %v, want %+v", label, user, sessionID, metadata, testIdentity)
	}
}

func TestGateway_Route_EventsCarryRequestIdentity(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets: []config.Target{
			{VirtualKey: "primary", Retry: &config.RetryConfig{Attempts: 2, InitialBackoffMs: 1}},
			{VirtualKey: "secondary"},
		},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)
	gw.RegisterProvider(&mockProvider{name: "primary", models: []string{testModel},
		err: core.StatusError("primary", http.StatusServiceUnavailable, "down")})
	gw.RegisterProvider(&mockProvider{name: "secondary", models: []string{testModel},
		resp: &providers.Response{ID: "ok", Provider: "secondary", Model: testModel}})

	if _, err := gw.Route(identityContext(t), providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Route: %v", err)
	}

	events := ep.capturedEvents()
	completed := eventsWithSubject(events, "gateway.request.completed")
	if len(completed) != 1 {
		t.Fatalf("completed events = %d, want 1", len(completed))
	}
	assertEventIdentity(t, "completed event", completed[0].User, completed[0].SessionID, completed[0].Metadata)

	attempts := eventsWithSubject(events, observability.SubjectRoutingAttempt)
	if len(attempts) != 3 {
		t.Fatalf("attempt events = %d, want 3", len(attempts))
	}
	for i, evt := range attempts {
		assertEventIdentity(t, "attempt event envelope", evt.User, evt.SessionID, evt.Metadata)
		if evt.RoutingAttempt == nil {
			t.Fatalf("attempt %d has no payload", i+1)
		}
		assertEventIdentity(t, "attempt payload", evt.RoutingAttempt.User, evt.RoutingAttempt.SessionID, evt.RoutingAttempt.Metadata)
	}
}

func TestGateway_Route_FailedEventCarriesRequestIdentity(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{Targets: []config.Target{{VirtualKey: "mock"}}})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)
	gw.RegisterProvider(&mockProvider{name: "mock", models: []string{testModel}, err: errors.New("dial failed")})

	if _, err := gw.Route(identityContext(t), providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}}); err == nil {
		t.Fatal("Route: expected an error")
	}

	failed := eventsWithSubject(ep.capturedEvents(), "gateway.request.failed")
	if len(failed) != 1 {
		t.Fatalf("failed events = %d, want 1", len(failed))
	}
	assertEventIdentity(t, "failed event", failed[0].User, failed[0].SessionID, failed[0].Metadata)
}
