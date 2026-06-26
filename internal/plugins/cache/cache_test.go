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
		if c.TTL != 300*time.Second {
			t.Errorf("expected default TTL 300s, got %v", c.TTL)
		}
		if c.Capacity != 1000 {
			t.Errorf("expected default Capacity 1000, got %d", c.Capacity)
		}
	})

	t.Run("custom max_age", func(t *testing.T) {
		c := initCache(t, map[string]interface{}{"max_age": 60})
		if c.TTL != 60*time.Second {
			t.Errorf("expected TTL 60s, got %v", c.TTL)
		}
	})

	t.Run("custom max_entries", func(t *testing.T) {
		c := initCache(t, map[string]interface{}{"max_entries": 50})
		if c.Capacity != 50 {
			t.Errorf("expected Capacity 50, got %d", c.Capacity)
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
	// Use a 10ms TTL so we can let it expire without a long sleep.
	c := initCache(t, map[string]interface{}{"max_age": float64(0)})
	// Override TTL directly since Init clamps 0 to 0s (immediate expiry isn't
	// easily configurable via the int config; set a short but nonzero duration).
	c.TTL = 10 * time.Millisecond

	req := testRequest("gpt-4", "hello")
	resp := testResponse()

	storePctx := plugin.NewContext(req)
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	lookupPctx := plugin.NewContext(req)
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.Skip {
		t.Error("expected cache miss for expired entry")
	}
}

// TestCachePlugin_LRUEviction verifies that adding a new entry beyond capacity
// evicts the least-recently-used entry, not the earliest-expiring one.
func TestCachePlugin_LRUEviction(t *testing.T) {
	c := initCache(t, map[string]interface{}{"max_entries": 2})
	resp := testResponse()

	store := func(content string) {
		pctx := plugin.NewContext(testRequest("gpt-4", content))
		pctx.Response = resp
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute (store %q) error: %v", content, err)
		}
	}
	hit := func(content string) bool {
		pctx := plugin.NewContext(testRequest("gpt-4", content))
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute (lookup %q) error: %v", content, err)
		}
		return pctx.Skip
	}

	store("msg-0") // LRU list: [msg-0]
	store("msg-1") // LRU list: [msg-1, msg-0]

	// Access msg-0 to make it recently used; msg-1 becomes LRU.
	if !hit("msg-0") {
		t.Fatal("expected hit for msg-0 before eviction")
	}
	// LRU list: [msg-0, msg-1]

	store("msg-2") // capacity exceeded → evicts msg-1 (LRU back)

	if hit("msg-1") {
		t.Error("expected msg-1 to be evicted (was LRU)")
	}
	if !hit("msg-0") {
		t.Error("expected msg-0 to be present (recently accessed)")
	}
	if !hit("msg-2") {
		t.Error("expected msg-2 to be present (just inserted)")
	}
}

func TestCachePlugin_MaxEntriesUpdateDoesNotEvict(t *testing.T) {
	c := initCache(t, map[string]interface{}{"max_entries": 2})

	store := func(content, id string) {
		resp := testResponse()
		resp.ID = id
		pctx := plugin.NewContext(testRequest("gpt-4", content))
		pctx.Response = resp
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute (store %s) error: %v", content, err)
		}
	}

	store("msg-0", "resp-0")
	store("msg-1", "resp-1")
	store("msg-1", "resp-1-updated") // update existing key — must not evict msg-0

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
	if c.Len() != 0 {
		t.Fatalf("expected no cached entries when max_entries=0, got %d", c.Len())
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

// --- logprobs cache-key coverage (issue #152) ---

func TestCacheKey_LogProbsProducesDistinctKey(t *testing.T) {
	top := 5
	withLogprobs := &providers.Request{
		Model:       "gpt-4",
		Messages:    []providers.Message{{Role: "user", Content: "hello"}},
		LogProbs:    true,
		TopLogProbs: &top,
	}
	withoutLogprobs := &providers.Request{
		Model:    "gpt-4",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
		LogProbs: false,
	}

	if cacheKey(withLogprobs) == cacheKey(withoutLogprobs) {
		t.Fatal("logprobs=true and logprobs=false must produce distinct cache keys")
	}
}

func TestCacheKey_DifferentTopLogProbsProducesDistinctKey(t *testing.T) {
	top3, top5 := 3, 5
	req3 := &providers.Request{
		Model:       "gpt-4",
		Messages:    []providers.Message{{Role: "user", Content: "hello"}},
		LogProbs:    true,
		TopLogProbs: &top3,
	}
	req5 := &providers.Request{
		Model:       "gpt-4",
		Messages:    []providers.Message{{Role: "user", Content: "hello"}},
		LogProbs:    true,
		TopLogProbs: &top5,
	}

	if cacheKey(req3) == cacheKey(req5) {
		t.Fatal("requests with different top_logprobs must produce distinct cache keys")
	}
}

func TestCacheKey_OutputParamsProduceDistinctKeys(t *testing.T) {
	base := func() *providers.Request {
		temp := 0.2
		topP := 0.9
		seed := int64(7)
		maxTokens := 64
		return &providers.Request{
			Model:       "gpt-4",
			Messages:    []providers.Message{{Role: "user", Content: "hello"}},
			Temperature: &temp,
			TopP:        &topP,
			Seed:        &seed,
			MaxTokens:   &maxTokens,
			Stop:        []string{"END"},
		}
	}

	tests := []struct {
		name   string
		mutate func(*providers.Request)
	}{
		{
			name: "temperature",
			mutate: func(req *providers.Request) {
				v := 0.8
				req.Temperature = &v
			},
		},
		{
			name: "top_p",
			mutate: func(req *providers.Request) {
				v := 0.5
				req.TopP = &v
			},
		},
		{
			name: "seed",
			mutate: func(req *providers.Request) {
				v := int64(42)
				req.Seed = &v
			},
		},
		{
			name: "max_tokens",
			mutate: func(req *providers.Request) {
				v := 128
				req.MaxTokens = &v
			},
		},
		{
			name: "stop",
			mutate: func(req *providers.Request) {
				req.Stop = []string{"DONE"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqA := base()
			reqB := base()
			tt.mutate(reqB)
			if cacheKey(reqA) == cacheKey(reqB) {
				t.Fatalf("requests with different %s must produce distinct cache keys", tt.name)
			}
		})
	}
}

func TestCacheKey_LogProbsNilTopLogProbsDoesNotPanic(_ *testing.T) {
	req := &providers.Request{
		Model:       "gpt-4",
		Messages:    []providers.Message{{Role: "user", Content: "hello"}},
		LogProbs:    true,
		TopLogProbs: nil,
	}
	// Must not panic.
	_ = cacheKey(req)
}

// TestCachePlugin_LogProbsCacheMissWithoutLogprobs verifies that a cached
// response from a logprobs=true request is not served to a logprobs=false
// request for the same model/messages.
func TestCachePlugin_LogProbsCacheMissWithoutLogprobs(t *testing.T) {
	c := initCache(t, map[string]interface{}{})
	top := 5

	withLogprobs := &providers.Request{
		Model:       "gpt-4",
		Messages:    []providers.Message{{Role: "user", Content: "what is 2+2"}},
		LogProbs:    true,
		TopLogProbs: &top,
	}
	withoutLogprobs := &providers.Request{
		Model:    "gpt-4",
		Messages: []providers.Message{{Role: "user", Content: "what is 2+2"}},
		LogProbs: false,
	}

	// Store a response for the logprobs=true request.
	storePctx := plugin.NewContext(withLogprobs)
	storePctx.Response = testResponse()
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	// A logprobs=false request must not receive the logprobs=true cached entry.
	lookupPctx := plugin.NewContext(withoutLogprobs)
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.Skip {
		t.Error("logprobs=false request must not hit a logprobs=true cache entry")
	}
}

// TestCachePlugin_LogProbsCacheHit verifies that a cached response from a
// logprobs=true request is served to an identical logprobs=true request.
func TestCachePlugin_LogProbsCacheHit(t *testing.T) {
	c := initCache(t, map[string]interface{}{})
	top := 5

	withLogprobs := func() *providers.Request {
		return &providers.Request{
			Model:       "gpt-4",
			Messages:    []providers.Message{{Role: "user", Content: "what is 2+2"}},
			LogProbs:    true,
			TopLogProbs: &top,
		}
	}

	storePctx := plugin.NewContext(withLogprobs())
	storePctx.Response = testResponse()
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	lookupPctx := plugin.NewContext(withLogprobs())
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if !lookupPctx.Skip {
		t.Error("expected cache hit for identical logprobs=true request")
	}
}

func TestCachePlugin_MaxEntriesMany(t *testing.T) {
	const maxEntries = 5
	c := initCache(t, map[string]interface{}{"max_entries": maxEntries})
	resp := testResponse()

	for i := 0; i < maxEntries; i++ {
		pctx := plugin.NewContext(testRequest("gpt-4", fmt.Sprintf("msg-%d", i)))
		pctx.Response = resp
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("store %d: %v", i, err)
		}
	}
	if c.Len() != maxEntries {
		t.Fatalf("expected %d entries, got %d", maxEntries, c.Len())
	}

	// Adding one more must not grow beyond maxEntries.
	overflow := plugin.NewContext(testRequest("gpt-4", "overflow"))
	overflow.Response = resp
	if err := c.Execute(context.Background(), overflow); err != nil {
		t.Fatalf("store overflow: %v", err)
	}
	if c.Len() != maxEntries {
		t.Fatalf("expected %d entries after overflow, got %d", maxEntries, c.Len())
	}
}
