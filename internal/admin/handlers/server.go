// Package handlers provides HTTP handlers for the gateway administration API.
// Routes expose API key management and provider model listing.
// All admin routes are protected by bearer-token authentication via AuthMiddleware.
package handlers

import (
	"context"
	"sync"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/go-chi/chi/v5"
)

// ConfigManager exposes the minimal gateway config operations needed by admin API.
type ConfigManager interface {
	GetConfig() config.Config
	ReloadConfig(ctx context.Context, cfg config.Config) error
	// Ping reports whether the config manager's backing store is reachable.
	// Readiness probes call it to gate traffic; it must be cheap.
	Ping(ctx context.Context) error
}

// MCPServerHealth reports one MCP server's availability on /admin/health.
//
// LastError is the reason the server is unready. The unauthenticated /readyz
// withholds it — it can quote a server URL, an authorization header, or a
// subprocess command line — which left the reason on no endpoint at all, so an
// operator had to read the server logs to learn why a server was down. This
// route is bearer-authenticated and scoped, so it carries it.
//
// The value must arrive already redacted: this package is the JSON encoder, and
// the caller that supplies MCPStatus is the HTTP sink that knows to apply
// redact.String (see mcpHealth in internal/httpserver).
type MCPServerHealth struct {
	Name      string `json:"name"`
	Ready     bool   `json:"ready"`
	Required  bool   `json:"required"`
	LastError string `json:"last_error,omitempty"`
}

// Handlers holds dependencies for admin HTTP handlers.
type Handlers struct {
	Keys      repository.Store
	Providers providers.ProviderSource
	Configs   ConfigManager
	Logs      requestlog.Reader
	LogAdmin  requestlog.Maintainer

	// Catalog reads the live model catalog, used by the provider-catalog
	// endpoint to report a per-provider model count for every built-in provider
	// — including ones with no registered credential, whose count comes from the
	// catalog alone. Read per request rather than snapshotted so a 24h catalog
	// refresh is reflected. Nil in tests that do not exercise the endpoint; the
	// handler treats a nil accessor as an empty catalog (every count 0).
	Catalog func() models.Catalog

	// MCPStatus reports the configured MCP servers, or is nil when the caller
	// wires none. /admin/health then omits the key rather than publishing an
	// empty list, which would read as "every MCP server is gone" on a gateway
	// that never had one.
	MCPStatus func() []MCPServerHealth

	// Sessions is nil when session authentication is not enabled; every
	// session-handler checks that before touching it.
	Sessions repository.SessionStore
	// Audit records administrative actions. It may be nil — recordAudit tolerates
	// that and falls back to the log line alone — but bootstrap always supplies
	// one, defaulting to the in-memory store, so nil is really only the case in
	// tests that do not exercise the trail.
	Audit repository.AuditStore
	// Credentials is the credential chain the session-exchange handler
	// authenticates the presented bearer with. It must be handed an
	// already-constructed CredentialValidator — calling NewCredentialValidator
	// a second time here would build a second, independently-drifting identity
	// set for the same key store and master key.
	Credentials CredentialValidator

	// configMu serializes whole config mutations: applying a config and
	// recording it in configHistory must happen as one step, or a concurrent
	// mutation lands in between and the newest history entry ends up naming a
	// config that is not the active one. Config mutation is a rare,
	// operator-driven action, so a single coarse write lock costs nothing.
	//
	// Lock order is configMu → historyMu, never the reverse. historyMu stays a
	// short slice guard so history reads never wait behind a config apply.
	configMu sync.Mutex

	// keysMu serializes the guarded key mutations (delete, revoke, update).
	// Each first counts the remaining admin keys and then acts on that count;
	// without the lock two concurrent deletes could each see the other's key
	// still present and between them remove the last admin credential. Key
	// management is a rare, operator-driven action, so one coarse lock costs
	// nothing. It guards no shared field and so has no ordering relationship
	// with configMu or historyMu.
	keysMu sync.Mutex

	historyMu     sync.Mutex
	configHistory []ConfigHistoryEntry
}

// Routes returns a chi.Router with all admin endpoints mounted.
func (h *Handlers) Routes() chi.Router {
	r := chi.NewRouter()

	// Read-only endpoints (accessible with read-only or admin scope).
	r.Group(func(r chi.Router) {
		r.Use(RequireScope(model.ScopeReadOnly, model.ScopeAdmin))
		r.Get("/dashboard", h.dashboard)
		r.Get("/keys", h.listKeys)
		r.Get("/keys/usage", h.keyUsage)
		r.Get("/keys/{id}", h.getKey)
		r.Get("/logs", h.listLogs)
		r.Get("/logs/stats", h.logsStats)
		r.Get("/providers", h.listProviders)
		r.Get("/providers/catalog", h.listProviderCatalog)
		r.Get("/health", h.healthCheck)
		r.Get("/plugins", h.listPlugins)
		r.Get("/plugins/catalog", h.pluginCatalog)
		r.Get("/config", h.getConfig)
		r.Get("/config/history", h.getConfigHistory)
		r.Get("/sessions", h.listSessions)
		r.Get("/audit", h.listAudit)
		// This group's RequireScope(ScopeReadOnly, ScopeAdmin) gates this route
		// like every other one above: a session may sign itself out only if its
		// credential carries one of those two scopes. Every key now holds a
		// scope from that set — createKey and updateKey refuse anything else
		// (model.ValidateScopes) — so no credential can reach the dashboard yet
		// be unable to reach this route to log itself out.
		r.Delete("/session", h.deleteSession)
	})

	// Write endpoints (admin scope only).
	r.Group(func(r chi.Router) {
		r.Use(RequireScope(model.ScopeAdmin))
		r.Post("/keys", h.createKey)
		r.Put("/keys/{id}", h.updateKey)
		r.Delete("/keys/{id}", h.deleteKey)
		r.Post("/keys/{id}/revoke", h.revokeKey)
		r.Post("/keys/{id}/rotate", h.rotateKey)
		r.Delete("/logs", h.deleteLogs)
		r.Post("/config", h.createConfig)
		r.Put("/config", h.updateConfig)
		r.Delete("/config", h.deleteConfig)
		r.Post("/config/rollback/{version}", h.rollbackConfig)
		r.Delete("/sessions", h.deleteAllSessions)
		// A distinct pattern from "/sessions" above, not a more specific form of
		// it: chi matches on segment count first, so signing one operator out
		// cannot be reached by the sign-everyone-out handler or the reverse.
		r.Delete("/sessions/{id}", h.revokeSession)
	})

	return r
}
