package cache

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func testRequest(model, content string) *providers.Request {
	return &providers.Request{
		Model: model,
		Messages: []providers.Message{
			{Role: "user", Content: content},
		},
	}
}

func testResponse() *providers.Response {
	return &providers.Response{
		ID:       "resp-1",
		Model:    "test-model",
		Provider: "test",
		Choices: []providers.Choice{
			{Index: 0, Message: providers.Message{Role: "assistant", Content: "hello"}, FinishReason: "stop"},
		},
		Usage: providers.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	}
}

func initCache(t *testing.T, config map[string]interface{}) *ResponseCache {
	t.Helper()
	c := &ResponseCache{}
	if err := c.Init(config); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return c
}

func TestCachePlugin_Init(t *testing.T) {
	t.Run("default config", func(t *testing.T) {
		c := initCache(t, map[string]interface{}{})
		if c.maxAge != 300*time.Second {
			t.Errorf("expected default maxAge 300s, got %v", c.maxAge)
		}
		if c.maxEntries != 1000 {
			t.Errorf("expected default maxEntries 1000, got %d", c.maxEntries)
		}
	})

	t.Run("custom max_age", func(t *testing.T) {
		c := initCache(t, map[string]interface{}{"max_age": 60})
		if c.maxAge != 60*time.Second {
			t.Errorf("expected maxAge 60s, got %v", c.maxAge)
		}
	})

	t.Run("custom max_entries", func(t *testing.T) {
		c := initCache(t, map[string]interface{}{"max_entries": 50})
		if c.maxEntries != 50 {
			t.Errorf("expected maxEntries 50, got %d", c.maxEntries)
		}
	})
}

func TestCachePlugin_CacheMiss(t *testing.T) {
	c := initCache(t, map[string]interface{}{})
	pctx := plugin.NewContext(testRequest("gpt-4", "hello"))

	if err := c.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Skip {
		t.Error("expected Skip to be false on cache miss")
	}
	if pctx.Response != nil {
		t.Error("expected Response to be nil on cache miss")
	}
}

func TestCachePlugin_CacheHitAfterStore(t *testing.T) {
	c := initCache(t, map[string]interface{}{})
	req := testRequest("gpt-4", "hello")
	resp := testResponse()

	// Simulate after_request: store response
	storePctx := plugin.NewContext(req)
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	// Simulate before_request: lookup
	lookupPctx := plugin.NewContext(req)
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if !lookupPctx.Skip {
		t.Error("expected Skip to be true on cache hit")
	}
	if lookupPctx.Response != resp {
		t.Error("expected cached response to match stored response")
	}
}

func TestCachePlugin_DifferentKeys(t *testing.T) {
	c := initCache(t, map[string]interface{}{})
	resp := testResponse()

	// Store with model "gpt-4"
	storePctx := plugin.NewContext(testRequest("gpt-4", "hello"))
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	// Lookup with different model
	lookupPctx := plugin.NewContext(testRequest("gpt-3.5", "hello"))
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.Skip {
		t.Error("expected cache miss for different model")
	}

	// Lookup with different message
	lookupPctx2 := plugin.NewContext(testRequest("gpt-4", "goodbye"))
	if err := c.Execute(context.Background(), lookupPctx2); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx2.Skip {
		t.Error("expected cache miss for different message")
	}
}

func TestCachePlugin_Expiration(t *testing.T) {
	c := initCache(t, map[string]interface{}{"max_age": 300})
	req := testRequest("gpt-4", "hello")
	resp := testResponse()

	// Store response
	storePctx := plugin.NewContext(req)
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	// Manually expire the entry
	key := cacheKey(req)
	c.mu.Lock()
	entry := c.entries[key]
	entry.expiresAt = time.Now().Add(-1 * time.Second)
	c.entries[key] = entry
	c.mu.Unlock()

	// Lookup should miss
	lookupPctx := plugin.NewContext(req)
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.Skip {
		t.Error("expected cache miss for expired entry")
	}
}

func TestCachePlugin_MaxEntries(t *testing.T) {
	c := initCache(t, map[string]interface{}{"max_entries": 2})
	resp := testResponse()

	// Fill cache to max
	for i := 0; i < 2; i++ {
		pctx := plugin.NewContext(testRequest("gpt-4", fmt.Sprintf("msg-%d", i)))
		pctx.Response = resp
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute (store %d) error: %v", i, err)
		}
	}

	// Third entry should not be added
	pctx := plugin.NewContext(testRequest("gpt-4", "msg-overflow"))
	pctx.Response = resp
	if err := c.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute (store overflow) error: %v", err)
	}

	c.mu.RLock()
	count := len(c.entries)
	c.mu.RUnlock()
	if count != 2 {
		t.Errorf("expected 2 entries, got %d", count)
	}

	// Verify the overflow entry is not cached
	lookupPctx := plugin.NewContext(testRequest("gpt-4", "msg-overflow"))
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.Skip {
		t.Error("expected cache miss for entry beyond max_entries")
	}
}

func TestCachePlugin_CacheHitMetadata(t *testing.T) {
	c := initCache(t, map[string]interface{}{})
	req := testRequest("gpt-4", "hello")
	resp := testResponse()

	// Store
	storePctx := plugin.NewContext(req)
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	// Lookup
	lookupPctx := plugin.NewContext(req)
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}

	hit, ok := lookupPctx.Metadata["cache_hit"].(bool)
	if !ok || !hit {
		t.Errorf("expected cache_hit=true in metadata, got %v", lookupPctx.Metadata["cache_hit"])
	}
}
