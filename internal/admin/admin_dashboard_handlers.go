package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/providers"
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
			Models: e.provider.Models(),
		})
	}
	if result == nil {
		result = []providerInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
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

	var providerStatuses []providerHealth
	overallStatus := "healthy"

	entries, _ := h.listProviderStatus()
	for _, e := range entries {
		if e.provider == nil {
			providerStatuses = append(providerStatuses, providerHealth{
				Name:    e.name,
				Status:  "unavailable",
				Message: "provider not found in registry",
			})
			overallStatus = "degraded"
			continue
		}
		providerStatuses = append(providerStatuses, providerHealth{
			Name:   e.name,
			Status: "available",
			Models: len(e.provider.Models()),
		})
	}

	if providerStatuses == nil {
		providerStatuses = []providerHealth{}
		overallStatus = "no_providers"
	}

	resp := map[string]any{
		"status":    overallStatus,
		"providers": providerStatuses,
	}

	// Include scopes of the authenticated key so the dashboard can set up
	// role-based UI without a separate round trip.
	if apiKey, ok := APIKeyFromContext(r.Context()); ok {
		resp["scopes"] = apiKey.Scopes
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}
