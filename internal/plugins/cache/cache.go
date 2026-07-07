// Package cache provides a response-cache plugin that stores LLM responses
// in memory and serves them on exact-match cache hits, reducing provider cost
// and latency for repeated requests. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"sort"
	"strconv"
	"time"

	internalCache "github.com/ferro-labs/ai-gateway/internal/cache"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func init() {
	plugin.RegisterFactory("response-cache", func() plugin.Plugin {
		return &ResponseCache{}
	})
}

// ResponseCache is a transform plugin that caches LLM responses using
// exact-match hashing of the request fields that can affect provider output.
// It aliases Memory from the internal cache package.
type ResponseCache struct {
	*internalCache.Memory
}

// Name returns the plugin identifier.
func (c *ResponseCache) Name() string {
	return "response-cache"
}

// Type returns the plugin lifecycle hook type.
func (c *ResponseCache) Type() plugin.PluginType {
	return plugin.TypeTransform
}

// Init configures the plugin from the provided options map.
func (c *ResponseCache) Init(config map[string]any) error {
	maxAge := 300
	// JSON delivers numeric values as float64; YAML may deliver int. Handle both.
	switch v := config["max_age"].(type) {
	case int:
		maxAge = v
	case float64:
		maxAge = int(v)
	}
	ttl := time.Duration(maxAge) * time.Second

	capacity := 1000
	switch v := config["max_entries"].(type) {
	case int:
		capacity = v
	case float64:
		capacity = int(v)
	}
	c.Memory = internalCache.NewMemory(capacity, ttl)
	return nil
}

// Execute checks for a cache hit (before request) or stores the response (after request) and does maintenance as per LRU policy.
func (c *ResponseCache) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx.Request == nil {
		return nil
	}

	key := cacheKey(pctx.Request)

	if pctx.Response == nil {
		// before_request: lookup
		if resp, ok := c.Get(key); ok {
			pctx.Response = cloneResponse(resp)
			pctx.Skip = true
			pctx.Metadata["cache_hit"] = true
		}
		return nil
	}

	// after_request: store
	if pctx.Metadata["cache_hit"] == true {
		return nil
	}

	if c.Capacity <= 0 {
		return nil
	}

	// Store a private copy: the caller's resp keeps being mutated after this
	// call returns (e.g. Route/RouteStream stamp OverheadMs post-RunAfter), so
	// the cache must not hold onto the same pointer.
	c.Set(key, cloneResponse(pctx.Response))
	return nil
}

// Close releases plugin resources.
func (c *ResponseCache) Close() error { return nil }

// cloneResponse returns a shallow copy of resp so each cache hit gets its own
// top-level struct. Route/RouteStream stamp Object/Created/OverheadMs on the
// response returned from a cache hit; Choices/Metadata/Usage are never
// mutated post-hit, so a shallow copy is sufficient to remove the race on a
// cache entry shared across concurrent callers.
func cloneResponse(resp *providers.Response) *providers.Response {
	clone := *resp
	return &clone
}

func cacheKey(req *providers.Request) string {
	h := sha256.New()
	writeCacheKeyString(h, "model")
	writeCacheKeyString(h, req.Model)
	writeCacheKeyString(h, "messages")
	writeCacheKeyInt(h, len(req.Messages))
	for _, m := range req.Messages {
		writeCacheKeyString(h, m.Role)
		writeCacheKeyString(h, m.Name)
		writeCacheKeyString(h, m.Content)
	}
	writeCacheKeyOptionalFloat64(h, "temperature", req.Temperature)
	writeCacheKeyOptionalFloat64(h, "top_p", req.TopP)
	writeCacheKeyOptionalInt(h, "n", req.N)
	writeCacheKeyOptionalInt64(h, "seed", req.Seed)
	writeCacheKeyOptionalInt(h, "max_tokens", req.MaxTokens)
	writeCacheKeyOptionalInt(h, "max_completion_tokens", req.MaxCompletionTokens)
	writeCacheKeyOptionalFloat64(h, "presence_penalty", req.PresencePenalty)
	writeCacheKeyOptionalFloat64(h, "frequency_penalty", req.FrequencyPenalty)
	writeCacheKeyStringSlice(h, "stop", req.Stop)
	writeCacheKeyJSON(h, "tools", req.Tools)
	writeCacheKeyJSON(h, "tool_choice", req.ToolChoice)
	writeCacheKeyJSON(h, "response_format", req.ResponseFormat)
	writeCacheKeyBool(h, "logprobs", req.LogProbs)
	writeCacheKeyOptionalInt(h, "top_logprobs", req.TopLogProbs)
	writeCacheKeyBool(h, "stream", req.Stream)
	writeCacheKeyString(h, "user")
	writeCacheKeyString(h, req.User)
	writeCacheKeyFloatMap(h, "logit_bias", req.LogitBias)

	return hex.EncodeToString(h.Sum(nil))
}

func writeCacheKeyBool(h hash.Hash, label string, v bool) {
	writeCacheKeyString(h, label)
	writeCacheKeyString(h, strconv.FormatBool(v))
}

func writeCacheKeyInt(h hash.Hash, v int) {
	writeCacheKeyString(h, strconv.Itoa(v))
}

func writeCacheKeyOptionalInt(h hash.Hash, label string, v *int) {
	writeCacheKeyString(h, label)
	if v == nil {
		writeCacheKeyString(h, "<nil>")
		return
	}
	writeCacheKeyString(h, strconv.Itoa(*v))
}

func writeCacheKeyOptionalInt64(h hash.Hash, label string, v *int64) {
	writeCacheKeyString(h, label)
	if v == nil {
		writeCacheKeyString(h, "<nil>")
		return
	}
	writeCacheKeyString(h, strconv.FormatInt(*v, 10))
}

func writeCacheKeyOptionalFloat64(h hash.Hash, label string, v *float64) {
	writeCacheKeyString(h, label)
	if v == nil {
		writeCacheKeyString(h, "<nil>")
		return
	}
	writeCacheKeyString(h, strconv.FormatFloat(*v, 'g', -1, 64))
}

func writeCacheKeyStringSlice(h hash.Hash, label string, values []string) {
	writeCacheKeyString(h, label)
	writeCacheKeyInt(h, len(values))
	for _, value := range values {
		writeCacheKeyString(h, value)
	}
}

func writeCacheKeyFloatMap(h hash.Hash, label string, values map[string]float64) {
	writeCacheKeyString(h, label)
	writeCacheKeyInt(h, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeCacheKeyString(h, key)
		writeCacheKeyString(h, strconv.FormatFloat(values[key], 'g', -1, 64))
	}
}

func writeCacheKeyJSON(h hash.Hash, label string, v any) {
	writeCacheKeyString(h, label)
	b, err := json.Marshal(v)
	if err != nil {
		writeCacheKeyString(h, err.Error())
		return
	}
	writeCacheKeyString(h, string(b))
}

func writeCacheKeyString(h hash.Hash, s string) {
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(s)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write([]byte(s))
}
