// Package cache provides a response-cache plugin that stores LLM responses
// in memory and serves them on exact-match cache hits, reducing provider cost
// and latency for repeated requests. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func init() {
	plugin.RegisterFactory("response-cache", func() plugin.Plugin {
		return &ResponseCache{}
	})
}

type cacheEntry struct {
	response  *providers.Response
	expiresAt time.Time
}

// ResponseCache is a transform plugin that caches LLM responses using
// exact-match hashing of the request (model + messages).
type ResponseCache struct {
	mu         sync.RWMutex
	entries    map[string]cacheEntry
	maxAge     time.Duration
	maxEntries int
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
	c.maxAge = time.Duration(maxAge) * time.Second

	c.maxEntries = 1000
	switch v := config["max_entries"].(type) {
	case int:
		c.maxEntries = v
	case float64:
		c.maxEntries = int(v)
	}

	c.entries = make(map[string]cacheEntry)
	return nil
}

// Execute checks for a cache hit (before request) or stores the response (after request).
func (c *ResponseCache) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx.Request == nil {
		return nil
	}

	key := cacheKey(pctx.Request)

	if pctx.Response == nil {
		// before_request: lookup
		c.mu.RLock()
		entry, ok := c.entries[key]
		c.mu.RUnlock()

		if ok && time.Now().Before(entry.expiresAt) {
			pctx.Response = entry.response
			pctx.Skip = true
			pctx.Metadata["cache_hit"] = true
		}
		return nil
	}

	// after_request: store
	if pctx.Metadata["cache_hit"] == true {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.maxEntries {
		return nil
	}

	c.entries[key] = cacheEntry{
		response:  pctx.Response,
		expiresAt: time.Now().Add(c.maxAge),
	}
	return nil
}

func cacheKey(req *providers.Request) string {
	msgs := make([]string, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = fmt.Sprintf("%s:%s:%s", m.Role, m.Name, m.Content)
	}
	sort.Strings(msgs)

	raw := req.Model + "\n" + fmt.Sprintf("%v", msgs)
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
