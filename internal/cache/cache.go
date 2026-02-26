// Package cache provides the CacheEntry and Cache interface used by the
// response-cache plugin. The default in-process implementation is MemoryCache.
package cache

import "github.com/ferro-labs/ai-gateway/providers"

// Cache defines the interface for response caching.
type Cache interface {
	Get(key string) (*providers.Response, bool)
	Set(key string, resp *providers.Response)
	Delete(key string)
	Len() int
	Clear()
}
