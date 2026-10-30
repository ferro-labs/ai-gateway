// Package cache provides a response-cache plugin that stores LLM responses
// in memory and serves them on exact-match cache hits, reducing provider cost
// and latency for repeated requests. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/cache"
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	cachestore "github.com/ferro-labs/ai-gateway/pkg/cache"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

const metadataCacheBypass = "response_cache_bypass"

func init() {
	plugin.RegisterFactory("response-cache", func() plugin.Plugin {
		return &ResponseCache{}
	})
}

// ResponseCache is a transform plugin that caches LLM responses using
// exact-match hashing of the request fields that can affect provider output.
// It embeds Memory from the public cache package.
type ResponseCache struct {
	*cachestore.Memory
}

var _ plugin.ModelPreserving = (*ResponseCache)(nil)

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
	c.Memory = cachestore.NewMemory(capacity, ttl)
	return nil
}

// Execute checks for a cache hit (before request) or stores the response (after
// request) and does maintenance as per LRU policy.
//
// A hit sets SkipProvider, which suppresses the provider call and nothing else:
// every guardrail, rate limit and budget registered behind this plugin still runs
// on the request, and the after_request stage still records it.
func (c *ResponseCache) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx.Request == nil {
		return nil
	}

	// Both stages stand down on a non-chat surface. The request there is a
	// PROJECTION of an embeddings input or an image prompt onto the chat request
	// shape (see plugin.MetadataSurface), so it hashes exactly as the chat
	// request asking the same question — while the response projected back
	// carries no Choices at all. Storing one would serve an empty completion to
	// the next chat request with that text, and never call the provider that
	// would have answered it.
	//
	// Nothing is lost by standing down: the gateway drops whatever this plugin
	// leaves on those surfaces, because a providers.Response cannot hold an
	// embedding or an image. Caching them needs a seam that can, not a key.
	if _, ok := pctx.Metadata[plugin.MetadataSurface]; ok {
		return nil
	}

	// The store is shared across callers, so content alone could serve one
	// credential's answer to another. Scope with the opaque APIKey.ID, never
	// the bearer secret. Metadata is populated before before_request, so both
	// stages use the same ID. Empty IDs share an unauthenticated namespace that
	// remains separate from every authenticated ID.
	//
	// The provider is not part of the key: Context.Target is unresolved during
	// before_request, when a hit must decide whether to skip the provider. This
	// assumes a model name has the same meaning across its routing targets.
	// Routing metadata can select a different target, so include it in the
	// namespace. JSON sorts map keys and keeps the ID and metadata unambiguous.
	keyID, _ := pctx.Metadata["api_key"].(string)
	if bypass, _ := pctx.Metadata[metadataCacheBypass].(bool); bypass {
		return nil
	}
	namespace, err := responseCacheNamespace(keyID, pctx.Request.RoutingMetadata)
	var key string
	if err == nil {
		key, err = cachestore.Fingerprint(pctx.Request, string(namespace))
	}
	if err != nil {
		// Keep both stages out of the cache, even if a later plugin changes
		// the request into a serializable form.
		if pctx.Metadata == nil {
			pctx.Metadata = make(map[string]any)
		}
		pctx.Metadata[metadataCacheBypass] = true
		return nil
	}

	if pctx.Stage == plugin.StageBeforeRequest {
		if resp, ok := c.Get(key); ok {
			pctx.Response = cloneResponse(resp)
			pctx.SkipProvider = true
			pctx.Metadata["cache_hit"] = true
		}
		return nil
	}

	// after_request: store. A response this plugin just served is already in the
	// cache; re-storing it would refresh a TTL the operator set from the original
	// fetch.
	if pctx.SkipProvider || pctx.Response == nil {
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

// responseCacheNamespace rejects strings that JSON would normalize, preserving
// the byte-for-byte distinctions used by conditional routing and credential IDs.
func responseCacheNamespace(keyID string, metadata map[string]string) ([]byte, error) {
	if !utf8.ValidString(keyID) {
		return nil, errors.New("cache namespace: invalid UTF-8 identifier")
	}
	for key, value := range metadata {
		if !utf8.ValidString(key) || !utf8.ValidString(value) {
			return nil, errors.New("cache namespace: invalid UTF-8 routing metadata")
		}
	}
	return json.Marshal(struct {
		KeyID           string            `json:"key_id"`
		RoutingMetadata map[string]string `json:"routing_metadata,omitempty"`
	}{KeyID: keyID, RoutingMetadata: metadata})
}

// PreservesModel declares that this plugin never changes the model a request
// routes to — it reads the request and answers from a store, and rewrites
// nothing. See plugin.ModelPreserving: it reports TypeTransform, and that alone
// used to cost the deployment the pre-plugin admission check, so enabling
// caching let an unroutable model spend a rate-limit token and a budget on its
// way to a 404.
func (c *ResponseCache) PreservesModel() {}

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
