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
	"hash"
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
// exact-match hashing of the request (model + messages + logprobs + toplogprobs(optional)).
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
func (c *ResponseCache) Init(config map[string]interface{}) error {
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
			pctx.Response = resp
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

	c.Set(key, pctx.Response)
	return nil
}

func cacheKey(req *providers.Request) string {
	h := sha256.New()
	writeCacheKeyString(h, req.Model)
	for _, m := range req.Messages {
		writeCacheKeyString(h, m.Role)
		writeCacheKeyString(h, m.Name)
		writeCacheKeyString(h, m.Content)
	}
	if req.LogProbs {
		writeCacheKeyString(h, "true")
		if req.TopLogProbs != nil {
			writeCacheKeyString(h, strconv.Itoa(*req.TopLogProbs))
		}
	} else {
		writeCacheKeyString(h, "false")
	}

	return hex.EncodeToString(h.Sum(nil))
}

func writeCacheKeyString(h hash.Hash, s string) {
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(s)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write([]byte(s))
}
