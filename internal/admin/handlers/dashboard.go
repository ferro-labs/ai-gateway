package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

const (
	healthStatusHealthy     = "healthy"
	healthStatusDegraded    = "degraded"
	healthStatusDisabled    = "disabled"
	healthStatusUnavailable = "unavailable"
)

// providerNames holds a resolved provider along with its registered name.
type providerNames struct {
	name     string
	provider providers.Provider
}

// listProviderStatus returns every registered provider name paired with the
// resolved provider (nil when the name is not resolvable) plus the count of
// names that resolved successfully. Centralizes the List()/Get() walk shared by
// the dashboard, provider list, and health-check handlers.
func (h *Handlers) listProviderStatus() (entries []providerNames, available int) {
	if h.Providers == nil {
		return nil, 0
	}
	names := h.Providers.List()
	entries = make([]providerNames, 0, len(names))
	for _, name := range names {
		p, ok := h.Providers.Get(name)
		if ok {
			available++
		}
		entries = append(entries, providerNames{name: name, provider: p})
	}
	return entries, available
}

func (h *Handlers) dashboard(w http.ResponseWriter, r *http.Request) {
	providerEntries, availableProviders := h.listProviderStatus()
	providersCount := len(providerEntries)

	keys := h.Keys.List(r.Context())
	activeKeys := 0
	expiredKeys := 0
	totalUsage := int64(0)
	now := time.Now().UTC()
	for _, key := range keys {
		if key.Active {
			activeKeys++
		}
		if key.ExpiresAt != nil && key.ExpiresAt.Before(now) {
			expiredKeys++
		}
		totalUsage += key.UsageCount
	}

	requestLogs := map[string]any{
		"enabled": false,
		"total":   0,
	}
	if h.Logs != nil {
		logsResult, err := h.Logs.List(r.Context(), requestlog.Query{Limit: 1, Offset: 0})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load dashboard summary", "server_error", "internal_error")
			return
		}
		requestLogs["enabled"] = true
		requestLogs["total"] = logsResult.Total
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"providers": map[string]any{
			"total":     providersCount,
			"available": availableProviders,
		},
		"keys": map[string]any{
			"total":       len(keys),
			"active":      activeKeys,
			"expired":     expiredKeys,
			"total_usage": totalUsage,
		},
		"request_logs": requestLogs,
	})
}

func (h *Handlers) listProviders(w http.ResponseWriter, _ *http.Request) {
	type providerInfo struct {
		Name   string                `json:"name"`
		Models []providers.ModelInfo `json:"models"`
	}

	var result []providerInfo
	entries, _ := h.listProviderStatus()
	for _, e := range entries {
		if e.provider == nil {
			continue
		}
		result = append(result, providerInfo{
			Name:   e.name,
			Models: h.Providers.ModelsFor(e.name),
		})
	}
	if result == nil {
		result = []providerInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// listProviderCatalog reports every built-in provider — registered or not —
// alongside a per-provider non-deprecated catalog model count. It is the roster
// the dashboard's Providers gallery renders: unlike listProviders, which reports
// only providers with a registered credential, this answers the full set from
// providers.AllProviders, so a provider no credential has configured still
// appears with registered=false and a count drawn from the model catalog alone.
func (h *Handlers) listProviderCatalog(w http.ResponseWriter, _ *http.Request) {
	type providerCatalogEntry struct {
		ID            string `json:"id"`
		Registered    bool   `json:"registered"`
		CatalogModels int    `json:"catalog_models"`
	}

	var catalog models.Catalog
	if h.Catalog != nil {
		catalog = h.Catalog()
	}

	entries := providers.AllProviders()
	result := make([]providerCatalogEntry, 0, len(entries))
	for _, e := range entries {
		registered := false
		if h.Providers != nil {
			_, registered = h.Providers.Get(e.ID)
		}
		result = append(result, providerCatalogEntry{
			ID:         e.ID,
			Registered: registered,
			// A nil catalog (h.Catalog unset in tests) is an empty map here, so
			// the count is 0 rather than a panic.
			CatalogModels: catalog.ActiveModelCountForProvider(e.ID),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"providers": result})
}

func (h *Handlers) listPlugins(w http.ResponseWriter, _ *http.Request) {
	type pluginInfo struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Enabled bool   `json:"enabled"`
	}

	var result []pluginInfo
	if h.Configs != nil {
		for _, p := range h.Configs.GetConfig().Plugins {
			result = append(result, pluginInfo{
				Name:    p.Name,
				Type:    p.Type,
				Enabled: p.Enabled,
			})
		}
	}
	if result == nil {
		result = []pluginInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *Handlers) healthCheck(w http.ResponseWriter, r *http.Request) {
	type providerHealth struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Models  int    `json:"models"`
		Message string `json:"message,omitempty"`
	}
	type componentHealth struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}

	var providerStatuses []providerHealth
	overallStatus := healthStatusHealthy

	entries, _ := h.listProviderStatus()
	for _, e := range entries {
		if e.provider == nil {
			providerStatuses = append(providerStatuses, providerHealth{
				Name:    e.name,
				Status:  healthStatusUnavailable,
				Message: "provider not found in registry",
			})
			overallStatus = healthStatusDegraded
			continue
		}
		providerStatuses = append(providerStatuses, providerHealth{
			Name:   e.name,
			Status: "available",
			Models: len(h.Providers.ModelsFor(e.name)),
		})
	}

	if providerStatuses == nil {
		providerStatuses = []providerHealth{}
		overallStatus = "no_providers"
	}

	components := make([]componentHealth, 0, 4)
	components = append(components, componentHealth{Name: "API", Status: healthStatusHealthy})
	keyStoreStatus := healthStatusDisabled
	if h.Keys != nil {
		keyStoreStatus = healthStatusHealthy
		if err := h.Keys.Ping(r.Context()); err != nil {
			keyStoreStatus = healthStatusUnavailable
			if overallStatus == healthStatusHealthy {
				overallStatus = healthStatusDegraded
			}
		}
	}
	components = append(components, componentHealth{Name: "Key store", Status: keyStoreStatus})

	configStoreStatus := healthStatusDisabled
	if h.Configs != nil {
		configStoreStatus = healthStatusHealthy
		if err := h.Configs.Ping(r.Context()); err != nil {
			configStoreStatus = healthStatusUnavailable
			if overallStatus == healthStatusHealthy {
				overallStatus = healthStatusDegraded
			}
		}
	}
	components = append(components, componentHealth{Name: "Config store", Status: configStoreStatus})

	// requestlog.Reader has no Ping, so the smallest read it does offer stands in
	// for one. Reporting healthy without asking the store anything would name the
	// one component in this list whose backing database is never checked.
	requestLogsStatus := healthStatusDisabled
	if h.Logs != nil {
		requestLogsStatus = healthStatusHealthy
		if _, err := h.Logs.List(r.Context(), requestlog.Query{Limit: 1}); err != nil {
			requestLogsStatus = healthStatusUnavailable
			if overallStatus == healthStatusHealthy {
				overallStatus = healthStatusDegraded
			}
		}
	}
	components = append(components, componentHealth{Name: "Request logs", Status: requestLogsStatus})

	// The audit store is reported but never downgrades overallStatus: it fails
	// open by design, so an unreachable trail must not take the instance out of
	// rotation. An operator watching this list sees the degradation; a readiness
	// probe gated on it would stop serving traffic over a missing log row.
	auditStatus := healthStatusDisabled
	if h.Audit != nil {
		auditStatus = healthStatusHealthy
		if err := h.Audit.Ping(r.Context()); err != nil {
			auditStatus = healthStatusUnavailable
		}
	}
	components = append(components, componentHealth{Name: "Audit log", Status: auditStatus})

	// MCP servers are reported here with the failure reason, which is served by
	// no other endpoint: /readyz is unauthenticated and has to withhold it.
	//
	// Only a *required* server downgrades the overall status, which is exactly
	// where /readyz gates. An optional server that is down costs its own tools
	// and nothing else, so reporting the instance degraded over one would call a
	// gateway serving every request it receives unhealthy.
	var mcpServers []MCPServerHealth
	if h.MCPStatus != nil {
		mcpServers = h.MCPStatus()
	}
	for _, server := range mcpServers {
		if server.Required && !server.Ready && overallStatus == healthStatusHealthy {
			overallStatus = healthStatusDegraded
		}
	}

	resp := map[string]any{
		"status":     overallStatus,
		"providers":  providerStatuses,
		"components": components,
	}
	if len(mcpServers) > 0 {
		resp["mcp_servers"] = mcpServers
	}

	// Include scopes of the authenticated key so the dashboard can set up
	// role-based UI without a separate round trip.
	if apiKey, ok := model.APIKeyFromContext(r.Context()); ok {
		resp["scopes"] = apiKey.Scopes
	}

	// Deliberately always 200: this endpoint is bearer-authenticated, so it is
	// never an LB or k8s probe target, and three clients read a non-2xx here as
	// "auth failed" or "gateway down" — the dashboard login probe, the dashboard
	// providers page, and `ferrogw admin health`. Degradation is reported in the
	// body. Probes should use the unauthenticated /health, which does return 503.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}
