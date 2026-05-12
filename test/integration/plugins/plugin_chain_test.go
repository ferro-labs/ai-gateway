//go:build integration
// +build integration

// Package plugins_test provides integration tests for the gateway plugin chain.
// Tests boot a full gateway with real plugin registrations and verify plugin
// lifecycle behavior (before_request short-circuits, cache hits, on_error stage).
package plugins_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers/core"

	// Register built-in plugin factories via side-effect imports.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/logger"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

// stubProv is a minimal Provider that lets tests hook response/error behavior.
type stubProv struct {
	name         string
	models       []string
	CompleteFunc func(ctx context.Context, req core.Request) (*core.Response, error)
}

var (
	_ core.Provider       = (*stubProv)(nil)
	_ core.StreamProvider = (*stubProv)(nil)
)

func (s *stubProv) Name() string              { return s.name }
func (s *stubProv) SupportedModels() []string { return s.models }
func (s *stubProv) SupportsModel(m string) bool {
	for _, n := range s.models {
		if n == m {
			return true
		}
	}
	return false
}
func (s *stubProv) Models() []core.ModelInfo { return core.ModelsFromList(s.name, s.models) }
func (s *stubProv) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	if s.CompleteFunc != nil {
		return s.CompleteFunc(ctx, req)
	}
	return &core.Response{
		ID:      "stub-id",
		Object:  "chat.completion",
		Model:   req.Model,
		Created: time.Now().Unix(),
		Choices: []core.Choice{
			{Index: 0, Message: core.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
		},
	}, nil
}
func (s *stubProv) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	ch := make(chan core.StreamChunk, 1)
	go func() {
		defer close(ch)
		resp, err := s.Complete(ctx, req)
		if err != nil {
			ch <- core.StreamChunk{Error: err}
			return
		}
		ch <- core.StreamChunk{
			ID:      resp.ID,
			Model:   req.Model,
			Created: resp.Created,
			Choices: []core.StreamChoice{{Delta: core.MessageDelta{Content: "ok"}, FinishReason: "stop"}},
		}
	}()
	return ch, nil
}

const pluginTestModel = "plugin-model-v1"

func newWordFilterGateway(t *testing.T, blockedWords []string, completeFunc func(context.Context, core.Request) (*core.Response, error)) *aigateway.Gateway {
	t.Helper()
	prov := &stubProv{name: "wfstub", models: []string{pluginTestModel}, CompleteFunc: completeFunc}
	cfg := aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "wfstub"}},
		Plugins: []aigateway.PluginConfig{
			{
				Name:    "word-filter",
				Type:    "guardrail",
				Stage:   "before_request",
				Enabled: true,
				Config:  map[string]interface{}{"blocked_words": wordsToInterface(blockedWords)},
			},
		},
	}
	gw, err := aigateway.New(cfg)
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	gw.RegisterProvider(prov)
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}
	return gw
}

func wordsToInterface(words []string) []interface{} {
	out := make([]interface{}, len(words))
	for i, w := range words {
		out[i] = w
	}
	return out
}

// TestPluginChain_WordFilter_BlockedWord verifies that a request containing a
// blocked word is rejected before reaching the provider (before_request stage).
func TestPluginChain_WordFilter_BlockedWord(t *testing.T) {
	called := false
	gw := newWordFilterGateway(t, []string{"secret"}, func(_ context.Context, _ core.Request) (*core.Response, error) {
		called = true
		return nil, nil
	})

	req := core.Request{
		Model:    pluginTestModel,
		Messages: []core.Message{{Role: "user", Content: "my secret password"}},
	}
	_, err := gw.Route(t.Context(), req)
	if err == nil {
		t.Fatal("expected rejection error, got nil")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("error %q should mention rejection", err.Error())
	}
	if called {
		t.Error("provider was called despite blocked word — plugin did not short-circuit")
	}
}

// TestPluginChain_WordFilter_CleanRequest passes a clean request through and
// confirms the provider is reached and the response returned.
func TestPluginChain_WordFilter_CleanRequest(t *testing.T) {
	called := false
	gw := newWordFilterGateway(t, []string{"secret"}, func(_ context.Context, req core.Request) (*core.Response, error) {
		called = true
		return &core.Response{
			ID: "clean-resp", Object: "chat.completion", Model: req.Model,
			Choices: []core.Choice{{Message: core.Message{Role: "assistant", Content: "hello"}, FinishReason: "stop"}},
		}, nil
	})

	req := core.Request{
		Model:    pluginTestModel,
		Messages: []core.Message{{Role: "user", Content: "hello world"}},
	}
	resp, err := gw.Route(t.Context(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("provider was not called for clean request")
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "hello" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

// TestPluginChain_ResponseCache_Hit verifies that identical requests are served
// from cache on the second call. The same plugin instance is registered at both
// before_request (lookup) and after_request (store) so they share the cache map.
func TestPluginChain_ResponseCache_Hit(t *testing.T) {
	prov := &stubProv{name: "cachestub", models: []string{pluginTestModel}}
	callCount := 0
	prov.CompleteFunc = func(_ context.Context, req core.Request) (*core.Response, error) {
		callCount++
		return &core.Response{
			ID: "cached-resp", Object: "chat.completion", Model: req.Model,
			Choices: []core.Choice{{Message: core.Message{Role: "assistant", Content: "cached"}, FinishReason: "stop"}},
		}, nil
	}

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "cachestub"}},
	})
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	gw.RegisterProvider(prov)

	// Register the same cache instance at both stages so lookup and storage share state.
	factory, ok := plugin.GetFactory("response-cache")
	if !ok {
		t.Fatal("response-cache factory not found — missing blank import?")
	}
	cachePlugin := factory()
	if initErr := cachePlugin.Init(map[string]interface{}{"max_age": 60, "max_entries": 10}); initErr != nil {
		t.Fatalf("cache Init: %v", initErr)
	}
	if regErr := gw.RegisterPlugin(plugin.StageBeforeRequest, cachePlugin); regErr != nil {
		t.Fatalf("RegisterPlugin before: %v", regErr)
	}
	if regErr := gw.RegisterPlugin(plugin.StageAfterRequest, cachePlugin); regErr != nil {
		t.Fatalf("RegisterPlugin after: %v", regErr)
	}

	req := core.Request{
		Model:    pluginTestModel,
		Messages: []core.Message{{Role: "user", Content: "what is 2+2?"}},
	}

	resp1, err := gw.Route(t.Context(), req)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	resp2, err := gw.Route(t.Context(), req)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	if callCount != 1 {
		t.Errorf("provider called %d times; want 1 (second should be cache hit)", callCount)
	}
	if resp1.ID != resp2.ID {
		t.Errorf("response IDs differ: %q vs %q; cache miss", resp1.ID, resp2.ID)
	}
}

// TestPluginChain_OnError_Fires confirms that a failing provider produces an
// error that propagates correctly through the gateway (on_error path).
func TestPluginChain_OnError_Fires(t *testing.T) {
	plugins := []aigateway.PluginConfig{
		{
			Name:    "request-logger",
			Type:    "logging",
			Stage:   "after_request",
			Enabled: true,
			Config:  map[string]interface{}{},
		},
	}
	prov := &stubProv{name: "errstub", models: []string{pluginTestModel}}
	prov.CompleteFunc = func(_ context.Context, _ core.Request) (*core.Response, error) {
		return nil, errors.New("upstream provider error: internal server error")
	}

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeFallback},
		Targets:  []aigateway.Target{{VirtualKey: "errstub"}},
		Plugins:  plugins,
	})
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	gw.RegisterProvider(prov)
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}

	req := core.Request{
		Model:    pluginTestModel,
		Messages: []core.Message{{Role: "user", Content: "trigger error"}},
	}
	_, err = gw.Route(t.Context(), req)
	if err == nil {
		t.Fatal("expected error from failing provider, got nil")
	}
	if !strings.Contains(err.Error(), "all providers failed") {
		t.Errorf("unexpected error message: %v", err)
	}
}
