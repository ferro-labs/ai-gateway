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

func TestCachePlugin_MessageOrderAffectsCacheKey(t *testing.T) {
	c := initCache(t, map[string]interface{}{})
	resp := testResponse()

	reqA := &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "system", Content: "You are concise."},
			{Role: "user", Content: "Explain TLS in one sentence."},
		},
	}
	reqB := &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "user", Content: "Explain TLS in one sentence."},
			{Role: "system", Content: "You are concise."},
		},
	}

	storePctx := plugin.NewContext(reqA)
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	lookupPctx := plugin.NewContext(reqB)
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.Skip {
		t.Error("expected cache miss for same messages in different order")
	}
}

func TestCachePlugin_DelimiterCharactersDoNotCollide(t *testing.T) {
	c := initCache(t, map[string]interface{}{})
	resp := testResponse()

	reqA := &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "user", Name: "", Content: "alpha\x00beta\ngamma"},
		},
	}
	reqB := &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "user", Name: "\x00alpha", Content: "beta\ngamma"},
		},
	}

	if cacheKey(reqA) == cacheKey(reqB) {
		t.Fatal("expected distinct cache keys for messages containing delimiter characters")
	}

	storePctx := plugin.NewContext(reqA)
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	lookupPctx := plugin.NewContext(reqB)
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.Skip {
		t.Error("expected cache miss for distinct requests with embedded delimiters")
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

	for i := 0; i < 2; i++ {
		pctx := plugin.NewContext(testRequest("gpt-4", fmt.Sprintf("msg-%d", i)))
		pctx.Response = resp
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute (store %d) error: %v", i, err)
		}
	}

	// Make msg-0 the oldest entry to ensure deterministic eviction.
	c.mu.Lock()
	entry0 := c.entries[cacheKey(testRequest("gpt-4", "msg-0"))]
	entry1 := c.entries[cacheKey(testRequest("gpt-4", "msg-1"))]
	entry0.expiresAt = time.Now().Add(10 * time.Second)
	entry1.expiresAt = time.Now().Add(20 * time.Second)
	c.entries[cacheKey(testRequest("gpt-4", "msg-0"))] = entry0
	c.entries[cacheKey(testRequest("gpt-4", "msg-1"))] = entry1
	c.mu.Unlock()

	// Third entry should evict the earliest expiring entry (msg-0).
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

	lookupOverflow := plugin.NewContext(testRequest("gpt-4", "msg-overflow"))
	if err := c.Execute(context.Background(), lookupOverflow); err != nil {
		t.Fatalf("Execute (lookup overflow) error: %v", err)
	}
	if !lookupOverflow.Skip {
		t.Error("expected cache hit for newest entry after eviction")
	}

	lookupEvicted := plugin.NewContext(testRequest("gpt-4", "msg-0"))
	if err := c.Execute(context.Background(), lookupEvicted); err != nil {
		t.Fatalf("Execute (lookup evicted) error: %v", err)
	}
	if lookupEvicted.Skip {
		t.Error("expected cache miss for evicted earliest-expiring entry")
	}
}


func TestCachePlugin_MaxEntriesUpdateDoesNotEvict(t *testing.T) {
	c := initCache(t, map[string]interface{}{"max_entries": 2})

	store := func(content, id string, expiresIn time.Duration) {
		resp := testResponse()
		resp.ID = id
		pctx := plugin.NewContext(testRequest("gpt-4", content))
		pctx.Response = resp
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute (store %s) error: %v", content, err)
		}
		key := cacheKey(testRequest("gpt-4", content))
		c.mu.Lock()
		entry := c.entries[key]
		entry.expiresAt = time.Now().Add(expiresIn)
		c.entries[key] = entry
		c.mu.Unlock()
	}

	store("msg-0", "resp-0", 10*time.Second)
	store("msg-1", "resp-1", 20*time.Second)
	store("msg-1", "resp-1-updated", 30*time.Second)

	lookup0 := plugin.NewContext(testRequest("gpt-4", "msg-0"))
	if err := c.Execute(context.Background(), lookup0); err != nil {
		t.Fatalf("Execute (lookup msg-0) error: %v", err)
	}
	if !lookup0.Skip {
		t.Fatal("expected cache hit for msg-0; existing-key update should not evict another entry")
	}

	lookup1 := plugin.NewContext(testRequest("gpt-4", "msg-1"))
	if err := c.Execute(context.Background(), lookup1); err != nil {
		t.Fatalf("Execute (lookup msg-1) error: %v", err)
	}
	if !lookup1.Skip {
		t.Fatal("expected cache hit for updated msg-1 entry")
	}
	if lookup1.Response == nil || lookup1.Response.ID != "resp-1-updated" {
		t.Fatalf("expected updated response for msg-1, got %#v", lookup1.Response)
	}
}

func TestCachePlugin_MaxEntriesZeroDisablesStore(t *testing.T) {
	c := initCache(t, map[string]interface{}{"max_entries": 0})
	pctx := plugin.NewContext(testRequest("gpt-4", "hello"))
	pctx.Response = testResponse()
	if err := c.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	lookup := plugin.NewContext(testRequest("gpt-4", "hello"))
	if err := c.Execute(context.Background(), lookup); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookup.Skip {
		t.Fatal("expected cache miss when max_entries=0")
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.entries) != 0 {
		t.Fatalf("expected no cached entries when max_entries=0, got %d", len(c.entries))
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
