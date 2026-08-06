package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/ferro-labs/ai-gateway/plugin"
)

// pluginCatalog serves the plugins this build ships, so a client does not have
// to keep its own copy of what the gateway already knows.
//
// A separate route from GET /admin/plugins rather than a wider version of it:
// that one answers "what is configured", read from the config document and
// changing whenever the config does; this answers "what could be configured",
// fixed for the lifetime of the binary. Folding them together would have made
// one response two resources and broken every existing caller of the first.
//
// Not sensitive — it is the same list the README prints — but it sits with the
// other read-only admin routes rather than being served unauthenticated,
// because it reports what this build contains and there is no reason for that
// to be the one /admin route an unauthenticated caller can fingerprint.
func (h *Handlers) pluginCatalog(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Immutable for the life of the process, and the dashboard asks for it on
	// two pages. Still revalidated so an upgraded gateway is not shadowed by a
	// browser cache holding the previous build's answer.
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": plugin.Builtins()})
}
