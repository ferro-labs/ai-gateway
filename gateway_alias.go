package aigateway

import (
	"github.com/ferro-labs/ai-gateway/providers"
)

// resolveModelAlias returns the alias target for model, or model unchanged.
func (g *Gateway) resolveModelAlias(model string) string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.resolveModelAliasLocked(model)
}

// resolveModelAliasLocked is resolveModelAlias for callers that already hold
// g.mu (a read lock is sufficient). sync.RWMutex forbids recursive read
// locking, so a path holding the lock must not go through resolveModelAlias.
func (g *Gateway) resolveModelAliasLocked(model string) string {
	if target, ok := g.config.Aliases[model]; ok {
		return target
	}
	return model
}

// resolveAlias replaces req.Model with its configured alias target (if any).
func (g *Gateway) resolveAlias(req providers.Request) providers.Request {
	req.Model = g.resolveModelAlias(req.Model)
	return req
}
