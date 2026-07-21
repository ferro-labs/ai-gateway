package aigateway

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/providers"
)

// fakeUpstreamKey has the legacy OpenAI sk- key shape. It is not a real credential.
var fakeUpstreamKey = buildFakeKey("sk-", "abc123DEF456ghi789JKL012mno345")

// buildFakeKey joins prefix and body at runtime to avoid committing credential-shaped literals.
func buildFakeKey(prefix, body string) string { return prefix + body }

// upstreamKeyError is the shape a provider returns when the upstream 401 body
// quotes back the credential the gateway presented.
func upstreamKeyError() error {
	return errors.New("openai API error (401): Incorrect API key provided: " + fakeUpstreamKey)
}

// captureLogs redirects the package logger into a buffer for the duration of
// the test and returns it.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := logging.Logger
	logging.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { logging.Logger = previous })
	return &buf
}

// The chat failure log line carries the provider's own error text, so it is
// filtered before it reaches the operator's log sink.
func TestRoute_FailureLogRedactsUpstreamError(t *testing.T) {
	logs := captureLogs(t)

	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   mockProviderName,
		models: []string{testModel},
		err:    upstreamKeyError(),
	})

	_, routeErr := gw.Route(context.Background(), providers.Request{
		Model:    testModel,
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if routeErr == nil {
		t.Fatal("expected the route to fail")
	}

	if strings.Contains(logs.String(), fakeUpstreamKey) {
		t.Fatalf("failure log leaked the upstream key: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "request failed") {
		t.Fatalf("expected a request-failed log line, got: %s", logs.String())
	}
}

// The embeddings surface logs through recordSurfaceError, which returns the
// message its caller logs. That returned message must already be filtered.
func TestEmbed_FailureLogRedactsUpstreamError(t *testing.T) {
	logs := captureLogs(t)

	gw, err := newTestGateway(t, Config{
		Strategy: StrategyConfig{Mode: ModeSingle},
		Targets:  []Target{{VirtualKey: mockProviderName}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockEmbeddingProvider{
		mockProvider: mockProvider{name: mockProviderName, models: []string{testModel}},
		err:          upstreamKeyError(),
	})

	_, embedErr := gw.Embed(context.Background(), providers.EmbeddingRequest{
		Model: testModel,
		Input: "hi",
	})
	if embedErr == nil {
		t.Fatal("expected the embedding request to fail")
	}

	if strings.Contains(logs.String(), fakeUpstreamKey) {
		t.Fatalf("embedding failure log leaked the upstream key: %s", logs.String())
	}
}
