package aigateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
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

func TestGateway_Route_EventsDoNotShareMetadataMap(t *testing.T) {
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
	attempts := eventsWithSubject(events, observability.SubjectRoutingAttempt)
	if len(attempts) < 2 {
		t.Fatalf("attempt events = %d, want at least 2", len(attempts))
	}

	// Mutate the first attempt's envelope Metadata and confirm no sibling
	// event — including its own RoutingAttempt payload and other attempts —
	// observes the change.
	attempts[0].Metadata["team"] = "mutated"
	if attempts[0].RoutingAttempt.Metadata["team"] == "mutated" {
		// Same-event envelope/payload sharing one copy is allowed by the
		// fix, so this is expected — assert it explicitly for clarity, not
		// as a bug.
		t.Log("envelope and payload of the same event intentionally share one copy")
	}
	for i := 1; i < len(attempts); i++ {
		if attempts[i].Metadata["team"] == "mutated" {
			t.Errorf("attempt %d Metadata observed mutation of a sibling event's map", i)
		}
		if attempts[i].RoutingAttempt.Metadata["team"] == "mutated" {
			t.Errorf("attempt %d RoutingAttempt.Metadata observed mutation of a sibling event's map", i)
		}
	}
	completed := eventsWithSubject(events, "gateway.request.completed")
	if len(completed) != 1 {
		t.Fatalf("completed events = %d, want 1", len(completed))
	}
	if completed[0].Metadata["team"] == "mutated" {
		t.Error("completed event Metadata observed mutation of a routing-attempt event's map")
	}
}

func TestGateway_Route_AnonymousEventHasNilMetadata(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{Targets: []config.Target{{VirtualKey: "mock"}}})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)
	gw.RegisterProvider(&mockProvider{name: "mock", models: []string{testModel},
		resp: &providers.Response{ID: "ok", Provider: "mock", Model: testModel}})

	if _, err := gw.Route(context.Background(), providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Route: %v", err)
	}

	completed := eventsWithSubject(ep.capturedEvents(), "gateway.request.completed")
	if len(completed) != 1 {
		t.Fatalf("completed events = %d, want 1", len(completed))
	}
	if completed[0].Metadata != nil {
		t.Errorf("completed event Metadata = %#v, want nil for an anonymous request", completed[0].Metadata)
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

func TestGateway_Route_RootSpanCarriesContextIdentity(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{Targets: []config.Target{{VirtualKey: "mock"}}})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	fp := &fakeProvider{}
	gw.SetObservability(fp)
	gw.RegisterProvider(&mockProvider{name: "mock", models: []string{testModel},
		resp: &providers.Response{ID: "ok", Provider: "mock", Model: testModel}})

	if _, err := gw.Route(identityContext(t), providers.Request{Model: testModel, Messages: []providers.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Route: %v", err)
	}
	assertEventIdentity(t, "root span attrs", fp.attrs.User, fp.attrs.SessionID, fp.attrs.Metadata)
}

func TestGateway_Route_BodyUserOutranksContextUser(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{Targets: []config.Target{{VirtualKey: "mock"}}})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	fp := &fakeProvider{}
	gw.SetObservability(fp)
	gw.RegisterProvider(&mockProvider{name: "mock", models: []string{testModel},
		resp: &providers.Response{ID: "ok", Provider: "mock", Model: testModel}})

	req := providers.Request{Model: testModel, User: "body-user", Messages: []providers.Message{{Role: "user", Content: "hi"}}}
	if _, err := gw.Route(identityContext(t), req); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if fp.attrs.User != "body-user" {
		t.Errorf("root span user = %q, want the body's %q", fp.attrs.User, "body-user")
	}
	if fp.attrs.SessionID != testIdentity.SessionID {
		t.Errorf("root span session = %q, want the context's %q kept", fp.attrs.SessionID, testIdentity.SessionID)
	}
}

func TestGateway_Route_BodyUserAloneIsRecorded(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{Targets: []config.Target{{VirtualKey: "mock"}}})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	fp := &fakeProvider{}
	gw.SetObservability(fp)
	gw.RegisterProvider(&mockProvider{name: "mock", models: []string{testModel},
		resp: &providers.Response{ID: "ok", Provider: "mock", Model: testModel}})

	req := providers.Request{Model: testModel, User: "body-user", Messages: []providers.Message{{Role: "user", Content: "hi"}}}
	if _, err := gw.Route(context.Background(), req); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if fp.attrs.User != "body-user" || fp.attrs.SessionID != "" || fp.attrs.Metadata != nil {
		t.Errorf("root span identity = %q/%q/%v, want body-user with nothing else", fp.attrs.User, fp.attrs.SessionID, fp.attrs.Metadata)
	}
}

func TestGateway_Embed_RootSpanCarriesContextIdentity(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{Targets: []config.Target{{VirtualKey: "mock"}}})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	fp := &fakeProvider{}
	gw.SetObservability(fp)
	gw.RegisterProvider(&mockEmbeddingProvider{mockProvider: mockProvider{name: "mock", models: []string{testModel}}})

	_, _ = gw.Embed(identityContext(t), providers.EmbeddingRequest{Model: testModel, Input: []string{"hi"}})
	assertEventIdentity(t, "embed root span attrs", fp.attrs.User, fp.attrs.SessionID, fp.attrs.Metadata)
}

// The body's `user` is the gateway core's own overlay onto RequestIdentity —
// EmbeddingRequest.User, not any HTTP header — so it must reach the request
// span exactly as chat's does (TestGateway_Route_BodyUserOutranksContextUser).
func TestGateway_Embed_BodyUserOutranksContextUser(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{Targets: []config.Target{{VirtualKey: "mock"}}})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	fp := &fakeProvider{}
	gw.SetObservability(fp)
	gw.RegisterProvider(&mockEmbeddingProvider{mockProvider: mockProvider{name: "mock", models: []string{testModel}}})

	_, _ = gw.Embed(identityContext(t), providers.EmbeddingRequest{Model: testModel, Input: []string{"hi"}, User: "body-user"})
	if fp.attrs.User != "body-user" {
		t.Errorf("embed root span user = %q, want the body's %q", fp.attrs.User, "body-user")
	}
	if fp.attrs.SessionID != testIdentity.SessionID {
		t.Errorf("embed root span session = %q, want the context's %q kept", fp.attrs.SessionID, testIdentity.SessionID)
	}
}

func TestGateway_GenerateImage_RootSpanCarriesContextIdentity(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{Targets: []config.Target{{VirtualKey: "mock"}}})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	fp := &fakeProvider{}
	gw.SetObservability(fp)
	gw.RegisterProvider(&mockImageProvider{mockProvider: mockProvider{name: "mock", models: []string{testModel}}})

	_, _ = gw.GenerateImage(identityContext(t), providers.ImageRequest{Model: testModel, Prompt: "cat"})
	assertEventIdentity(t, "generate image root span attrs", fp.attrs.User, fp.attrs.SessionID, fp.attrs.Metadata)
}

// The body's `user` is the gateway core's own overlay onto RequestIdentity —
// ImageRequest.User, not any HTTP header — so it must reach the request span
// exactly as chat's does (TestGateway_Route_BodyUserOutranksContextUser).
func TestRequestIdentity_UnusableBodyUserIsIgnored(t *testing.T) {
	for _, tc := range []struct {
		name     string
		bodyUser string
	}{
		{name: "longer than the id limit", bodyUser: strings.Repeat("a", 257)},
		{name: "carrying a control character", bodyUser: "user\x00name"},
		{name: "whitespace only", bodyUser: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			newCtx, id := requestIdentity(ctx, tc.bodyUser)
			if id.User != "" {
				t.Errorf("id.User = %q, want empty for a body user %s", id.User, tc.name)
			}
			if newCtx != ctx {
				t.Error("context reallocated for a rejected body user, want unchanged")
			}
		})
	}
}

func TestRequestIdentity_ValidBodyUserOverlaysContext(t *testing.T) {
	ctx := observability.ContextWithRequestIdentity(context.Background(), observability.RequestIdentity{User: "ctx-user"})
	_, id := requestIdentity(ctx, "body-user")
	if id.User != "body-user" {
		t.Errorf("id.User = %q, want %q", id.User, "body-user")
	}
}

func TestGateway_GenerateImage_BodyUserOutranksContextUser(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{Targets: []config.Target{{VirtualKey: "mock"}}})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	fp := &fakeProvider{}
	gw.SetObservability(fp)
	gw.RegisterProvider(&mockImageProvider{mockProvider: mockProvider{name: "mock", models: []string{testModel}}})

	_, _ = gw.GenerateImage(identityContext(t), providers.ImageRequest{Model: testModel, Prompt: "cat", User: "body-user"})
	if fp.attrs.User != "body-user" {
		t.Errorf("generate image root span user = %q, want the body's %q", fp.attrs.User, "body-user")
	}
	if fp.attrs.SessionID != testIdentity.SessionID {
		t.Errorf("generate image root span session = %q, want the context's %q kept", fp.attrs.SessionID, testIdentity.SessionID)
	}
}
