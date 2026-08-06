package cache

import (
	"context"
	"fmt"
	"sync"
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

func initCache(t *testing.T, config map[string]any) *ResponseCache {
	t.Helper()
	c := &ResponseCache{}
	if err := c.Init(config); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return c
}

func TestResponseCache_Init(t *testing.T) {
	t.Parallel()

	t.Run("default config", func(t *testing.T) {
		c := initCache(t, map[string]any{})
		if c.TTL != 300*time.Second {
			t.Errorf("expected default TTL 300s, got %v", c.TTL)
		}
		if c.Capacity != 1000 {
			t.Errorf("expected default Capacity 1000, got %d", c.Capacity)
		}
	})

	t.Run("custom max_age", func(t *testing.T) {
		c := initCache(t, map[string]any{"max_age": 60})
		if c.TTL != 60*time.Second {
			t.Errorf("expected TTL 60s, got %v", c.TTL)
		}
	})

	t.Run("custom max_entries", func(t *testing.T) {
		c := initCache(t, map[string]any{"max_entries": 50})
		if c.Capacity != 50 {
			t.Errorf("expected Capacity 50, got %d", c.Capacity)
		}
	})
}

func TestResponseCache_CacheMiss(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
	pctx := plugin.NewContext(testRequest("gpt-4", "hello"))
	pctx.Stage = plugin.StageBeforeRequest

	if err := c.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.SkipProvider {
		t.Error("expected SkipProvider to be false on cache miss")
	}
	if pctx.Response != nil {
		t.Error("expected Response to be nil on cache miss")
	}
}

func TestResponseCache_CacheHitAfterStore(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
	req := testRequest("gpt-4", "hello")
	resp := testResponse()

	// Simulate after_request: store response
	storePctx := plugin.NewContext(req)
	storePctx.Stage = plugin.StageAfterRequest
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	// Simulate before_request: lookup
	lookupPctx := plugin.NewContext(req)
	lookupPctx.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if !lookupPctx.SkipProvider {
		t.Error("expected SkipProvider to be true on cache hit")
	}
	if lookupPctx.Response == nil {
		t.Fatal("expected a cached response")
	}
	if lookupPctx.Response == resp {
		t.Error("expected cache hit to return a distinct copy, not the cached pointer (shared-pointer data race)")
	}
	if lookupPctx.Response.ID != resp.ID {
		t.Errorf("expected cached response content to match stored response, got ID %q want %q", lookupPctx.Response.ID, resp.ID)
	}
}

// TestResponseCache_ConcurrentCacheHits_NoDataRace reproduces the scenario where
// Route/RouteStream stamp Object/Created/OverheadMs on a cache-hit response
// (see gateway_route.go and gateway_stream.go). Before the fix, every hit
// returned the same cached *providers.Response pointer, so concurrent callers
// raced on these writes. Run with -race.
func TestResponseCache_ConcurrentCacheHits_NoDataRace(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
	req := testRequest("gpt-4", "hello")

	storePctx := plugin.NewContext(req)
	storePctx.Stage = plugin.StageAfterRequest
	storePctx.Response = testResponse()
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	const concurrency = 50
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()
			pctx := plugin.NewContext(testRequest("gpt-4", "hello"))
			pctx.Stage = plugin.StageBeforeRequest
			if err := c.Execute(context.Background(), pctx); err != nil {
				t.Errorf("Execute (lookup) error: %v", err)
				return
			}
			if pctx.Response == nil {
				t.Error("expected cache hit response")
				return
			}
			// Mirrors the per-caller mutations Route/RouteStream apply to an
			// early cache-hit response.
			if pctx.Response.Created == 0 {
				pctx.Response.Created = time.Now().Unix()
			}
			pctx.Response.OverheadMs = float64(time.Now().UnixNano())
			pctx.Response.Object = "chat.completion"
		}()
	}
	wg.Wait()
}

// TestResponseCache_ConcurrentStoreAndHit_NoDataRace reproduces a second,
// distinct race: Route/RouteStream mutate their own resp.OverheadMs *after*
// RunAfter (and therefore after the cache plugin's Set) returns (see
// gateway_route.go's `resp.OverheadMs = ...` following the after-plugins
// call, and gateway_stream.go's analogous self-assignment). If Set stores the
// caller's live pointer, that post-store mutation races with any concurrent
// Get on the same key. Run with -race.
func TestResponseCache_ConcurrentStoreAndHit_NoDataRace(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
	req := testRequest("gpt-4", "hello")

	const concurrency = 50
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()

			resp := testResponse()
			storePctx := plugin.NewContext(req)
			storePctx.Stage = plugin.StageAfterRequest
			storePctx.Response = resp
			if err := c.Execute(context.Background(), storePctx); err != nil {
				t.Errorf("Execute (store) error: %v", err)
				return
			}
			// Mirrors gateway_route.go stamping OverheadMs on its own resp
			// variable after the after-request plugins (including the cache
			// store) have already run.
			resp.OverheadMs = float64(time.Now().UnixNano())

			lookupPctx := plugin.NewContext(req)
			lookupPctx.Stage = plugin.StageBeforeRequest
			if err := c.Execute(context.Background(), lookupPctx); err != nil {
				t.Errorf("Execute (lookup) error: %v", err)
				return
			}
			if lookupPctx.Response != nil {
				lookupPctx.Response.OverheadMs = float64(time.Now().UnixNano())
			}
		}()
	}
	wg.Wait()
}

func TestResponseCache_DifferentKeys(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
	resp := testResponse()

	// Store with model "gpt-4"
	storePctx := plugin.NewContext(testRequest("gpt-4", "hello"))
	storePctx.Stage = plugin.StageAfterRequest
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	// Lookup with different model
	lookupPctx := plugin.NewContext(testRequest("gpt-3.5", "hello"))
	lookupPctx.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.SkipProvider {
		t.Error("expected cache miss for different model")
	}

	// Lookup with different message
	lookupPctx2 := plugin.NewContext(testRequest("gpt-4", "goodbye"))
	lookupPctx2.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookupPctx2); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx2.SkipProvider {
		t.Error("expected cache miss for different message")
	}
}

func TestResponseCache_MessageOrderAffectsCacheKey(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
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
	storePctx.Stage = plugin.StageAfterRequest
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	lookupPctx := plugin.NewContext(reqB)
	lookupPctx.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.SkipProvider {
		t.Error("expected cache miss for same messages in different order")
	}
}

func TestResponseCache_DelimiterCharactersDoNotCollide(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
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

	if cacheKey(reqA, "") == cacheKey(reqB, "") {
		t.Fatal("expected distinct cache keys for messages containing delimiter characters")
	}

	storePctx := plugin.NewContext(reqA)
	storePctx.Stage = plugin.StageAfterRequest
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	lookupPctx := plugin.NewContext(reqB)
	lookupPctx.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.SkipProvider {
		t.Error("expected cache miss for distinct requests with embedded delimiters")
	}
}

func TestResponseCache_ExecuteExpiresEntry(t *testing.T) {
	t.Parallel()

	now := time.Unix(0, 0)
	c := initCache(t, map[string]any{"max_age": 10})
	c.SetNowForTest(func() time.Time { return now })

	req := testRequest("gpt-4", "hello")
	resp := testResponse()

	storePctx := plugin.NewContext(req)
	storePctx.Stage = plugin.StageAfterRequest
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	now = now.Add(11 * time.Second)

	lookupPctx := plugin.NewContext(req)
	lookupPctx.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.SkipProvider {
		t.Error("expected cache miss for expired entry")
	}
}

// TestResponseCache_LRUEviction verifies that adding a new entry beyond capacity
// evicts the least-recently-used entry, not the earliest-expiring one.
func TestResponseCache_LRUEviction(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{"max_entries": 2})
	resp := testResponse()

	store := func(content string) {
		pctx := plugin.NewContext(testRequest("gpt-4", content))
		pctx.Stage = plugin.StageAfterRequest
		pctx.Response = resp
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute (store %q) error: %v", content, err)
		}
	}
	hit := func(content string) bool {
		pctx := plugin.NewContext(testRequest("gpt-4", content))
		pctx.Stage = plugin.StageBeforeRequest
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute (lookup %q) error: %v", content, err)
		}
		return pctx.SkipProvider
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

func TestResponseCache_MaxEntriesUpdateDoesNotEvict(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{"max_entries": 2})

	store := func(content, id string) {
		resp := testResponse()
		resp.ID = id
		pctx := plugin.NewContext(testRequest("gpt-4", content))
		pctx.Stage = plugin.StageAfterRequest
		pctx.Response = resp
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute (store %s) error: %v", content, err)
		}
	}

	store("msg-0", "resp-0")
	store("msg-1", "resp-1")
	store("msg-1", "resp-1-updated") // update existing key — must not evict msg-0

	lookup0 := plugin.NewContext(testRequest("gpt-4", "msg-0"))
	lookup0.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookup0); err != nil {
		t.Fatalf("Execute (lookup msg-0) error: %v", err)
	}
	if !lookup0.SkipProvider {
		t.Fatal("expected cache hit for msg-0; existing-key update should not evict another entry")
	}

	lookup1 := plugin.NewContext(testRequest("gpt-4", "msg-1"))
	lookup1.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookup1); err != nil {
		t.Fatalf("Execute (lookup msg-1) error: %v", err)
	}
	if !lookup1.SkipProvider {
		t.Fatal("expected cache hit for updated msg-1 entry")
	}
	if lookup1.Response == nil || lookup1.Response.ID != "resp-1-updated" {
		t.Fatalf("expected updated response for msg-1, got %#v", lookup1.Response)
	}
}

func TestResponseCache_MaxEntriesZeroDisablesStore(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{"max_entries": 0})
	pctx := plugin.NewContext(testRequest("gpt-4", "hello"))
	pctx.Stage = plugin.StageAfterRequest
	pctx.Response = testResponse()
	if err := c.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	lookup := plugin.NewContext(testRequest("gpt-4", "hello"))
	lookup.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookup); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookup.SkipProvider {
		t.Fatal("expected cache miss when max_entries=0")
	}
	if c.Len() != 0 {
		t.Fatalf("expected no cached entries when max_entries=0, got %d", c.Len())
	}
}

func TestResponseCache_CacheHitMetadata(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
	req := testRequest("gpt-4", "hello")
	resp := testResponse()

	// Store
	storePctx := plugin.NewContext(req)
	storePctx.Stage = plugin.StageAfterRequest
	storePctx.Response = resp
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	// Lookup
	lookupPctx := plugin.NewContext(req)
	lookupPctx.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}

	hit, ok := lookupPctx.Metadata["cache_hit"].(bool)
	if !ok || !hit {
		t.Errorf("expected cache_hit=true in metadata, got %v", lookupPctx.Metadata["cache_hit"])
	}
}

// --- credential scoping ---

// storeAs primes the cache with resp for req, as the credential apiKey.
func storeAs(t *testing.T, c *ResponseCache, req *providers.Request, apiKey string, resp *providers.Response) {
	t.Helper()
	pctx := plugin.NewContext(req)
	pctx.Stage = plugin.StageAfterRequest
	pctx.Response = resp
	if apiKey != "" {
		pctx.Metadata["api_key"] = apiKey
	}
	if err := c.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute (store as %q) error: %v", apiKey, err)
	}
}

// lookupAs performs a before_request lookup for req as the credential apiKey and
// reports whether it hit.
func lookupAs(t *testing.T, c *ResponseCache, req *providers.Request, apiKey string) bool {
	t.Helper()
	pctx := plugin.NewContext(req)
	pctx.Stage = plugin.StageBeforeRequest
	if apiKey != "" {
		pctx.Metadata["api_key"] = apiKey
	}
	if err := c.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute (lookup as %q) error: %v", apiKey, err)
	}
	return pctx.SkipProvider
}

// TestResponseCache_ScopedToCredential is the regression guard for the cache
// serving one credential's response to another. The store is process-global and
// identical prompts across tenants are the common case, so a key built from
// request content alone let key B inherit an answer primed by key A — along with
// every guardrail, budget and rate-limit decision that was made for A's call and
// never made for B's.
//
// Both halves matter. Different credentials must NOT share an entry, and the
// same credential must still hit: the credential belongs in the key to separate
// entries, not to disable caching.
func TestResponseCache_ScopedToCredential(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
	req := testRequest("gpt-4", "hello")

	storeAs(t, c, req, "key-alice", testResponse())

	if lookupAs(t, c, req, "key-bob") {
		t.Error("credential bob was served a response primed by alice: cache key ignores the caller")
	}
	if !lookupAs(t, c, req, "key-alice") {
		t.Error("credential alice missed its own cached response: the key separates callers instead of just isolating them")
	}
}

// TestResponseCache_UnauthenticatedSharesOneBucket pins the chosen handling of a
// request with no credential: unauthenticated callers share one bucket with each
// other and share with nobody authenticated.
//
// Sharing rather than refusing to cache, because with no identity there is
// nothing to separate on — the same reason the request log folds every
// unattributable row under api_key_id=none — and refusing would silently turn
// caching off for an entire unauthenticated deployment rather than isolate
// anything.
func TestResponseCache_UnauthenticatedSharesOneBucket(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
	req := testRequest("gpt-4", "hello")

	storeAs(t, c, req, "", testResponse())

	if !lookupAs(t, c, req, "") {
		t.Error("an unauthenticated request missed an entry another unauthenticated request primed")
	}
	if lookupAs(t, c, req, "key-alice") {
		t.Error("credential alice was served a response primed by an unauthenticated caller")
	}
}

// TestResponseCache_CredentialDoesNotLeakAcrossStages proves the two stages of
// one request agree on the key. A store that read the credential and a lookup
// that did not would still pass the tests above in one direction and never hit
// in the other.
func TestResponseCache_CredentialDoesNotLeakAcrossStages(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})

	for _, apiKey := range []string{"", "key-alice", "key-bob"} {
		req := testRequest("gpt-4", "hello-"+apiKey)
		storeAs(t, c, req, apiKey, testResponse())
		if !lookupAs(t, c, req, apiKey) {
			t.Errorf("credential %q could not read back its own entry", apiKey)
		}
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

	if cacheKey(withLogprobs, "") == cacheKey(withoutLogprobs, "") {
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

	if cacheKey(req3, "") == cacheKey(req5, "") {
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
			if cacheKey(reqA, "") == cacheKey(reqB, "") {
				t.Fatalf("requests with different %s must produce distinct cache keys", tt.name)
			}
		})
	}
}

// --- message-body cache-key coverage ---

// multimodalToolRequest is a conversation that exercises every answer-affecting
// Message field beyond Role/Name/Content: an image part, an assistant turn that
// issued a tool call, and the tool result answering it.
func multimodalToolRequest() *providers.Request {
	index := 0
	return &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{
				Role:    "user",
				Content: "What is in this image?",
				ContentParts: []providers.ContentPart{
					{Type: "text", Text: "What is in this image?"},
					{Type: "image_url", ImageURL: &providers.ImageURLPart{URL: "https://example.com/cat.jpg", Detail: "auto"}},
				},
			},
			{
				Role: "assistant",
				ToolCalls: []providers.ToolCall{
					{Index: &index, ID: "call_a", Type: "function", Function: providers.FunctionCall{Name: "book_flight", Arguments: `{"to":"LHR"}`}},
				},
				ReasoningContent: "the user wants a flight",
			},
			{Role: "tool", ToolCallID: "call_a", Content: "booked"},
		},
	}
}

// TestCacheKey_MessageBodyProducesDistinctKeys covers the Message fields that
// change the answer without changing Content. Message.UnmarshalJSON collapses
// only text parts into Content, so an image URL reaches the provider leaving no
// trace in Content; tool calls and tool_call_id never touch Content at all.
func TestCacheKey_MessageBodyProducesDistinctKeys(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*providers.Request)
	}{
		{
			name: "image url",
			mutate: func(req *providers.Request) {
				req.Messages[0].ContentParts[1].ImageURL.URL = "https://example.com/dog.jpg"
			},
		},
		{
			name: "image detail",
			mutate: func(req *providers.Request) {
				req.Messages[0].ContentParts[1].ImageURL.Detail = "high"
			},
		},
		{
			name: "content part text",
			mutate: func(req *providers.Request) {
				req.Messages[0].ContentParts[0].Text = "Describe this image."
			},
		},
		{
			name: "content part dropped",
			mutate: func(req *providers.Request) {
				req.Messages[0].ContentParts = nil
			},
		},
		{
			name: "tool call function name",
			mutate: func(req *providers.Request) {
				req.Messages[1].ToolCalls[0].Function.Name = "cancel_flight"
			},
		},
		{
			name: "tool call arguments",
			mutate: func(req *providers.Request) {
				req.Messages[1].ToolCalls[0].Function.Arguments = `{"to":"CDG"}`
			},
		},
		{
			name: "tool call id",
			mutate: func(req *providers.Request) {
				req.Messages[1].ToolCalls[0].ID = "call_b"
			},
		},
		{
			name: "tool call index",
			mutate: func(req *providers.Request) {
				index := 1
				req.Messages[1].ToolCalls[0].Index = &index
			},
		},
		{
			name: "tool call dropped",
			mutate: func(req *providers.Request) {
				req.Messages[1].ToolCalls = nil
			},
		},
		{
			name: "reasoning content",
			mutate: func(req *providers.Request) {
				req.Messages[1].ReasoningContent = "the user wants a refund"
			},
		},
		{
			name: "tool result tool_call_id",
			mutate: func(req *providers.Request) {
				req.Messages[2].ToolCallID = "call_b"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqA := multimodalToolRequest()
			reqB := multimodalToolRequest()
			tt.mutate(reqB)
			if cacheKey(reqA, "") == cacheKey(reqB, "") {
				t.Fatalf("requests with different %s must produce distinct cache keys", tt.name)
			}
		})
	}
}

// TestCacheKey_MessageBodyIsStable guards the other direction: an unchanged
// request must keep hashing to the same key, or the cache never hits.
func TestCacheKey_MessageBodyIsStable(t *testing.T) {
	req := multimodalToolRequest()
	key := cacheKey(req, "")

	if again := cacheKey(req, ""); again != key {
		t.Fatalf("cacheKey is not deterministic: %q then %q", key, again)
	}
	if equivalent := cacheKey(multimodalToolRequest(), ""); equivalent != key {
		t.Fatalf("equivalent requests must share a cache key: %q vs %q", key, equivalent)
	}
}

// TestResponseCache_ImageCacheMissForDifferentImage exercises the same
// collision through the plugin: two requests whose prompts are identical and
// whose only difference is the attached image must not share a response.
func TestResponseCache_ImageCacheMissForDifferentImage(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})

	catReq := multimodalToolRequest()
	storePctx := plugin.NewContext(catReq)
	storePctx.Stage = plugin.StageAfterRequest
	storePctx.Response = testResponse()
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	dogReq := multimodalToolRequest()
	dogReq.Messages[0].ContentParts[1].ImageURL.URL = "https://example.com/dog.jpg"
	lookupPctx := plugin.NewContext(dogReq)
	lookupPctx.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.SkipProvider {
		t.Error("a request for a different image must not hit another image's cache entry")
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
	_ = cacheKey(req, "")
}

// TestResponseCache_LogProbsCacheMissWithoutLogprobs verifies that a cached
// response from a logprobs=true request is not served to a logprobs=false
// request for the same model/messages.
func TestResponseCache_LogProbsCacheMissWithoutLogprobs(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
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
	storePctx.Stage = plugin.StageAfterRequest
	storePctx.Response = testResponse()
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	// A logprobs=false request must not receive the logprobs=true cached entry.
	lookupPctx := plugin.NewContext(withoutLogprobs)
	lookupPctx.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if lookupPctx.SkipProvider {
		t.Error("logprobs=false request must not hit a logprobs=true cache entry")
	}
}

// TestResponseCache_LogProbsCacheHit verifies that a cached response from a
// logprobs=true request is served to an identical logprobs=true request.
func TestResponseCache_LogProbsCacheHit(t *testing.T) {
	t.Parallel()

	c := initCache(t, map[string]any{})
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
	storePctx.Stage = plugin.StageAfterRequest
	storePctx.Response = testResponse()
	if err := c.Execute(context.Background(), storePctx); err != nil {
		t.Fatalf("Execute (store) error: %v", err)
	}

	lookupPctx := plugin.NewContext(withLogprobs())
	lookupPctx.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), lookupPctx); err != nil {
		t.Fatalf("Execute (lookup) error: %v", err)
	}
	if !lookupPctx.SkipProvider {
		t.Error("expected cache hit for identical logprobs=true request")
	}
}

func TestResponseCache_MaxEntriesMany(t *testing.T) {
	t.Parallel()

	const maxEntries = 5
	c := initCache(t, map[string]any{"max_entries": maxEntries})
	resp := testResponse()

	for i := 0; i < maxEntries; i++ {
		pctx := plugin.NewContext(testRequest("gpt-4", fmt.Sprintf("msg-%d", i)))
		pctx.Stage = plugin.StageAfterRequest
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
	overflow.Stage = plugin.StageAfterRequest
	overflow.Response = resp
	if err := c.Execute(context.Background(), overflow); err != nil {
		t.Fatalf("store overflow: %v", err)
	}
	if c.Len() != maxEntries {
		t.Fatalf("expected %d entries after overflow, got %d", maxEntries, c.Len())
	}
}

// TestCacheKey_ParallelToolCallsProducesDistinctKeys covers a parameter that
// changes what a provider returns and left no trace in the key: two requests
// differing only in parallel_tool_calls were served each other's answer, so a
// caller that disabled parallel calls could receive a cached response holding
// several of them.
//
// Unset is a third value, not a synonym for false: it leaves the upstream
// default in place, which providers document as true.
func TestCacheKey_ParallelToolCallsProducesDistinctKeys(t *testing.T) {
	t.Parallel()

	base := func(parallel *bool) *providers.Request {
		return &providers.Request{
			Model:             "gpt-4",
			Messages:          []providers.Message{{Role: "user", Content: "hello"}},
			Tools:             []providers.Tool{{Type: "function", Function: providers.Function{Name: "book_flight"}}},
			ParallelToolCalls: parallel,
		}
	}
	enabled, disabled := true, false

	keys := map[string]string{
		"unset": cacheKey(base(nil), ""),
		"true":  cacheKey(base(&enabled), ""),
		"false": cacheKey(base(&disabled), ""),
	}
	for a, ka := range keys {
		for b, kb := range keys {
			if a < b && ka == kb {
				t.Errorf("parallel_tool_calls %s and %s must produce distinct cache keys", a, b)
			}
		}
	}

	// Same value still hits: the field separates entries, it does not disable
	// caching for requests that carry it.
	other := true
	if cacheKey(base(&enabled), "") != cacheKey(base(&other), "") {
		t.Error("identical parallel_tool_calls requests must produce the same cache key")
	}
}

// The cache stands down on the non-chat surfaces, and the reason is not that
// caching an embedding would be useless — it is that the request it would key on
// is a PROJECTION of the embeddings input onto the chat request shape, so it
// hashes identically to the chat request asking the same question while the
// response it carries has no Choices at all.
func TestExecute_StandsDownOnNonChatSurfaces(t *testing.T) {
	t.Parallel()

	chatRequest := &providers.Request{
		Model:    "gpt-4",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	}
	// What runSurfaceGovernance hands the plugin stage for an embeddings request
	// whose input is "hello". Identical by construction — that is the hazard.
	surfaceRequest := &providers.Request{
		Model:    "gpt-4",
		Messages: []providers.Message{{Role: "user", Content: "hello"}},
	}
	if cacheKey(chatRequest, "") != cacheKey(surfaceRequest, "") {
		t.Fatal("this test asserts nothing unless the two requests collide")
	}

	c := &ResponseCache{}
	if err := c.Init(map[string]any{}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// after_request on the surface must store nothing.
	store := plugin.NewContext(surfaceRequest)
	defer plugin.PutContext(store)
	store.Stage = plugin.StageAfterRequest
	store.Metadata[plugin.MetadataSurface] = "embeddings"
	store.Response = &providers.Response{Model: "gpt-4"} // the projection: no Choices
	if err := c.Execute(context.Background(), store); err != nil {
		t.Fatalf("Execute (after_request): %v", err)
	}
	if _, ok := c.Get(cacheKey(surfaceRequest, "")); ok {
		t.Error("the surface response was stored; the next chat request with this text is served an empty completion")
	}

	// before_request on the surface must not serve one either, even when the
	// chat side has primed that key with a real answer.
	c.Set(cacheKey(chatRequest, ""), &providers.Response{
		Choices: []providers.Choice{{Message: providers.Message{Role: "assistant", Content: "a real answer"}}},
	})
	lookup := plugin.NewContext(surfaceRequest)
	defer plugin.PutContext(lookup)
	lookup.Stage = plugin.StageBeforeRequest
	lookup.Metadata[plugin.MetadataSurface] = "embeddings"
	if err := c.Execute(context.Background(), lookup); err != nil {
		t.Fatalf("Execute (before_request): %v", err)
	}
	if lookup.SkipProvider || lookup.Response != nil {
		t.Error("a chat completion was offered in place of the embeddings call")
	}

	// The chat surface is untouched by all of this.
	chat := plugin.NewContext(chatRequest)
	defer plugin.PutContext(chat)
	chat.Stage = plugin.StageBeforeRequest
	if err := c.Execute(context.Background(), chat); err != nil {
		t.Fatalf("Execute (chat): %v", err)
	}
	if !chat.SkipProvider || chat.Response == nil {
		t.Fatal("the chat cache hit was lost")
	}
}
