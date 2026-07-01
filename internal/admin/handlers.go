// Package admin provides HTTP handlers for the gateway administration API.
// Routes expose API key management and provider model listing.
// All admin routes are protected by bearer-token authentication via AuthMiddleware.
package admin

import (
	"context"
	"sync"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/go-chi/chi/v5"
)

// ConfigManager exposes the minimal gateway config operations needed by admin API.
type ConfigManager interface {
	GetConfig() aigateway.Config
	ReloadConfig(ctx context.Context, cfg aigateway.Config) error
}

// Handlers holds dependencies for admin HTTP handlers.
type Handlers struct {
	Keys      Store
	Providers providers.ProviderSource
	Configs   ConfigManager
	Logs      requestlog.Reader
	LogAdmin  requestlog.Maintainer

	historyMu     sync.Mutex
	configHistory []ConfigHistoryEntry
}

const unknownLabel = "unknown"
const logsStatsMaxScannedEntries = 5000

// Routes returns a chi.Router with all admin endpoints mounted.
func (h *Handlers) Routes() chi.Router {
	r := chi.NewRouter()

	// Read-only endpoints (accessible with read-only or admin scope).
	r.Group(func(r chi.Router) {
		r.Use(RequireScope(ScopeReadOnly, ScopeAdmin))
		r.Get("/dashboard", h.dashboard)
		r.Get("/keys", h.listKeys)
		r.Get("/keys/usage", h.keyUsage)
		r.Get("/keys/{id}", h.getKey)
		r.Get("/logs", h.listLogs)
		r.Get("/logs/stats", h.logsStats)
		r.Get("/providers", h.listProviders)
		r.Get("/health", h.healthCheck)
		r.Get("/plugins", h.listPlugins)
		r.Get("/config", h.getConfig)
		r.Get("/config/history", h.getConfigHistory)
	})

	// Write endpoints (admin scope only).
	r.Group(func(r chi.Router) {
		r.Use(RequireScope(ScopeAdmin))
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
	})

	return r
}
