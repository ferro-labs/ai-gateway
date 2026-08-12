package table

import "github.com/ferro-labs/ai-gateway/cli/internal/api"

// A plugin's fail policy is vocabulary the operator reads, so it is spelled
// once. "open" and "closed" answer what happens to a request when the plugin
// itself breaks: a logging or metrics plugin fails open and the request
// proceeds, while a guardrail, auth or rate limiter fails closed and the
// request does not.
const (
	FailsOpen   = "open"
	FailsClosed = "closed"
)

// PluginPolicy is what this build's catalog says about one configured plugin.
// Fails is "-" when the catalog does not name the plugin at all — an unknown
// policy, which is a different claim from "closed".
type PluginPolicy struct {
	Summary string
	Fails   string
}

// PluginCatalog answers what a build ships, indexed by plugin name.
type PluginCatalog map[string]PluginPolicy

// NewPluginCatalog indexes GET /admin/plugins/catalog.
//
// Only the catalog knows whether a plugin fails open, so both the scriptable
// table and the console must derive the FAILS cell from it. They each used to
// carry their own copy of this lookup and their own open/closed constants,
// which let one report a policy the other did not — for the same plugin, on
// the same gateway, in the same release.
func NewPluginCatalog(builtins []api.BuiltinPlugin) PluginCatalog {
	c := make(PluginCatalog, len(builtins))
	for _, b := range builtins {
		fails := FailsClosed
		if b.FailsOpen {
			fails = FailsOpen
		}
		c[b.Name] = PluginPolicy{Summary: b.Summary, Fails: fails}
	}
	return c
}

// For resolves a configured plugin against the catalog. A plugin the build
// does not ship — registered out of tree — reports an unknown policy rather
// than being assumed to fail closed.
func (c PluginCatalog) For(name string) PluginPolicy {
	if p, ok := c[name]; ok {
		return p
	}
	return PluginPolicy{Fails: "-"}
}
