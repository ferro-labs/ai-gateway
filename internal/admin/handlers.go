// Package admin provides HTTP handlers for the gateway administration API.
// Routes expose API key management and provider model listing.
// All admin routes are protected by bearer-token authentication via AuthMiddleware.
package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/go-chi/chi/v5"
)

// ConfigManager exposes the minimal gateway config operations needed by admin API.
type ConfigManager interface {
	GetConfig() aigateway.Config
	ReloadConfig(cfg aigateway.Config) error
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

// ConfigHistoryEntry captures a runtime config update snapshot.
type ConfigHistoryEntry struct {
	Version        int              `json:"version"`
	UpdatedAt      time.Time        `json:"updated_at"`
	Config         aigateway.Config `json:"config"`
	RolledBackFrom *int             `json:"rolled_back_from,omitempty"`
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

func (h *Handlers) dashboard(w http.ResponseWriter, r *http.Request) {
	providersCount := 0
	availableProviders := 0
	if h.Providers != nil {
		providers := h.Providers.List()
		providersCount = len(providers)
		for _, name := range providers {
			if _, ok := h.Providers.Get(name); ok {
				availableProviders++
			}
		}
	}

	keys := h.Keys.List()
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

	requestLogs := map[string]interface{}{
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
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"providers": map[string]interface{}{
			"total":     providersCount,
			"available": availableProviders,
		},
		"keys": map[string]interface{}{
			"total":       len(keys),
			"active":      activeKeys,
			"expired":     expiredKeys,
			"total_usage": totalUsage,
		},
		"request_logs": requestLogs,
	})
}

func (h *Handlers) createKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		ExpiresAt string   `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error", "invalid_request")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required", "invalid_request_error", "invalid_request")
		return
	}

	var expiresAt *time.Time
	if body.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, body.ExpiresAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid expires_at: must be RFC3339 format", "invalid_request_error", "invalid_request")
			return
		}
		expiresAt = &t
	}

	key, err := h.Keys.Create(body.Name, body.Scopes, expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error", "internal_error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(key)
}

func (h *Handlers) listKeys(w http.ResponseWriter, _ *http.Request) {
	keys := h.Keys.List()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(keys)
}

func (h *Handlers) getKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key, ok := h.Keys.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "key not found", "not_found_error", "resource_not_found")
		return
	}

	masked := *key
	if len(masked.Key) > 8 {
		masked.Key = masked.Key[:8] + "..."
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(masked)
}

//nolint:gocyclo // Query parsing + filtering/sorting logic is intentionally centralized for the endpoint.
func (h *Handlers) keyUsage(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit: must be a positive integer", "invalid_request_error", "invalid_request")
			return
		}
		if parsed > 100 {
			parsed = 100
		}
		limit = parsed
	}

	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset: must be a non-negative integer", "invalid_request_error", "invalid_request")
			return
		}
		offset = parsed
	}

	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "usage"
	}
	if sortBy != "usage" && sortBy != "last_used" {
		writeError(w, http.StatusBadRequest, "invalid sort: must be usage or last_used", "invalid_request_error", "invalid_request")
		return
	}

	activeFilter := ""
	if raw := r.URL.Query().Get("active"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid active: must be true or false", "invalid_request_error", "invalid_request")
			return
		}
		activeFilter = strconv.FormatBool(parsed)
	}

	var sinceFilter *time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since: must be RFC3339 format", "invalid_request_error", "invalid_request")
			return
		}
		sinceFilter = &parsed
	}

	filteredKeys := make([]*APIKey, 0)
	for _, key := range h.Keys.List() {
		if activeFilter != "" {
			requireActive := activeFilter == "true"
			if key.Active != requireActive {
				continue
			}
		}
		if sinceFilter != nil {
			if key.LastUsedAt == nil || key.LastUsedAt.Before(*sinceFilter) {
				continue
			}
		}
		filteredKeys = append(filteredKeys, key)
	}

	sort.Slice(filteredKeys, func(i, j int) bool {
		if sortBy == "last_used" {
			if filteredKeys[i].LastUsedAt == nil && filteredKeys[j].LastUsedAt != nil {
				return false
			}
			if filteredKeys[i].LastUsedAt != nil && filteredKeys[j].LastUsedAt == nil {
				return true
			}
			if filteredKeys[i].LastUsedAt != nil && filteredKeys[j].LastUsedAt != nil && !filteredKeys[i].LastUsedAt.Equal(*filteredKeys[j].LastUsedAt) {
				return filteredKeys[i].LastUsedAt.After(*filteredKeys[j].LastUsedAt)
			}
			if filteredKeys[i].UsageCount != filteredKeys[j].UsageCount {
				return filteredKeys[i].UsageCount > filteredKeys[j].UsageCount
			}
			return filteredKeys[i].CreatedAt.After(filteredKeys[j].CreatedAt)
		}

		if filteredKeys[i].UsageCount != filteredKeys[j].UsageCount {
			return filteredKeys[i].UsageCount > filteredKeys[j].UsageCount
		}

		if filteredKeys[i].LastUsedAt == nil && filteredKeys[j].LastUsedAt != nil {
			return false
		}
		if filteredKeys[i].LastUsedAt != nil && filteredKeys[j].LastUsedAt == nil {
			return true
		}
		if filteredKeys[i].LastUsedAt != nil && filteredKeys[j].LastUsedAt != nil && !filteredKeys[i].LastUsedAt.Equal(*filteredKeys[j].LastUsedAt) {
			return filteredKeys[i].LastUsedAt.After(*filteredKeys[j].LastUsedAt)
		}

		return filteredKeys[i].CreatedAt.After(filteredKeys[j].CreatedAt)
	})

	totalUsage := int64(0)
	activeKeys := 0
	for _, key := range filteredKeys {
		totalUsage += key.UsageCount
		if key.Active {
			activeKeys++
		}
	}

	keys := make([]*APIKey, 0)
	if offset < len(filteredKeys) {
		keys = filteredKeys[offset:]
		if limit < len(keys) {
			keys = keys[:limit]
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data": keys,
		"summary": map[string]interface{}{
			"total_keys":    len(filteredKeys),
			"active_keys":   activeKeys,
			"total_usage":   totalUsage,
			"returned_keys": len(keys),
		},
		"filters": map[string]interface{}{
			"limit":  limit,
			"offset": offset,
			"sort":   sortBy,
			"active": activeFilter,
			"since":  r.URL.Query().Get("since"),
		},
	})
}

func (h *Handlers) listLogs(w http.ResponseWriter, r *http.Request) {
	if h.Logs == nil {
		writeError(w, http.StatusNotImplemented, "request log storage is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit: must be a positive integer", "invalid_request_error", "invalid_request")
			return
		}
		if parsed > 200 {
			parsed = 200
		}
		limit = parsed
	}

	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset: must be a non-negative integer", "invalid_request_error", "invalid_request")
			return
		}
		offset = parsed
	}

	var since *time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since: must be RFC3339 format", "invalid_request_error", "invalid_request")
			return
		}
		since = &parsed
	}

	query := requestlog.Query{
		Limit:    limit,
		Offset:   offset,
		Stage:    r.URL.Query().Get("stage"),
		Model:    r.URL.Query().Get("model"),
		Provider: r.URL.Query().Get("provider"),
		Since:    since,
	}

	result, err := h.Logs.List(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list request logs", "server_error", "internal_error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data": result.Data,
		"summary": map[string]interface{}{
			"total_entries":    result.Total,
			"returned_entries": len(result.Data),
		},
		"filters": map[string]interface{}{
			"limit":    limit,
			"offset":   offset,
			"stage":    query.Stage,
			"model":    query.Model,
			"provider": query.Provider,
			"since":    r.URL.Query().Get("since"),
		},
	})
}

func (h *Handlers) deleteLogs(w http.ResponseWriter, r *http.Request) {
	if h.LogAdmin == nil {
		writeError(w, http.StatusNotImplemented, "request log storage is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	beforeRaw := r.URL.Query().Get("before")
	if beforeRaw == "" {
		writeError(w, http.StatusBadRequest, "before is required and must be RFC3339 format", "invalid_request_error", "invalid_request")
		return
	}

	before, err := time.Parse(time.RFC3339, beforeRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid before: must be RFC3339 format", "invalid_request_error", "invalid_request")
		return
	}

	deleted, err := h.LogAdmin.Delete(r.Context(), requestlog.MaintenanceQuery{
		Before:   &before,
		Stage:    r.URL.Query().Get("stage"),
		Model:    r.URL.Query().Get("model"),
		Provider: r.URL.Query().Get("provider"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete request logs", "server_error", "internal_error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted": deleted,
		"filters": map[string]interface{}{
			"before":   beforeRaw,
			"stage":    r.URL.Query().Get("stage"),
			"model":    r.URL.Query().Get("model"),
			"provider": r.URL.Query().Get("provider"),
		},
	})
}

func (h *Handlers) logsStats(w http.ResponseWriter, r *http.Request) {
	if h.Logs == nil {
		writeError(w, http.StatusNotImplemented, "request log storage is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit: must be a positive integer", "invalid_request_error", "invalid_request")
			return
		}
		if parsed > 100 {
			parsed = 100
		}
		limit = parsed
	}

	var since *time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since: must be RFC3339 format", "invalid_request_error", "invalid_request")
			return
		}
		since = &parsed
	}

	baseQuery := requestlog.Query{
		Limit:    200,
		Offset:   0,
		Stage:    r.URL.Query().Get("stage"),
		Model:    r.URL.Query().Get("model"),
		Provider: r.URL.Query().Get("provider"),
		Since:    since,
	}

	result, err := h.Logs.List(r.Context(), baseQuery)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute request log stats", "server_error", "internal_error")
		return
	}

	entries := make([]requestlog.Entry, 0, result.Total)
	entries = append(entries, result.Data...)
	for len(entries) < result.Total && len(entries) < logsStatsMaxScannedEntries {
		baseQuery.Offset = len(entries)
		next, listErr := h.Logs.List(r.Context(), baseQuery)
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to compute request log stats", "server_error", "internal_error")
			return
		}
		if len(next.Data) == 0 {
			break
		}
		remaining := logsStatsMaxScannedEntries - len(entries)
		if remaining <= 0 {
			break
		}
		if len(next.Data) > remaining {
			next.Data = next.Data[:remaining]
		}
		entries = append(entries, next.Data...)
	}
	truncated := len(entries) < result.Total

	byStage := map[string]int{}
	byProvider := map[string]int{}
	byModel := map[string]int{}
	errorCount := 0
	tokens := 0
	for _, entry := range entries {
		stage := entry.Stage
		if stage == "" {
			stage = unknownLabel
		}
		byStage[stage]++

		provider := entry.Provider
		if provider == "" {
			provider = unknownLabel
		}
		byProvider[provider]++

		model := entry.Model
		if model == "" {
			model = unknownLabel
		}
		byModel[model]++

		if entry.ErrorMessage != "" || stage == "on_error" {
			errorCount++
		}
		tokens += entry.TotalTokens
	}

	byProvider = limitCounts(byProvider, limit)
	byModel = limitCounts(byModel, limit)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"summary": map[string]interface{}{
			"total_entries":     len(entries),
			"error_entries":     errorCount,
			"total_tokens":      tokens,
			"truncated":         truncated,
			"available_entries": result.Total,
			"scan_limit":        logsStatsMaxScannedEntries,
		},
		"by_stage":    byStage,
		"by_provider": byProvider,
		"by_model":    byModel,
		"filters": map[string]interface{}{
			"limit":    limit,
			"stage":    baseQuery.Stage,
			"model":    baseQuery.Model,
			"provider": baseQuery.Provider,
			"since":    r.URL.Query().Get("since"),
		},
	})
}

func limitCounts(input map[string]int, limit int) map[string]int {
	if limit <= 0 || len(input) <= limit {
		return input
	}

	type item struct {
		name  string
		count int
	}
	items := make([]item, 0, len(input))
	for name, count := range input {
		items = append(items, item{name: name, count: count})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		return items[i].name < items[j].name
	})

	trimmed := make(map[string]int, limit)
	for i := 0; i < limit; i++ {
		trimmed[items[i].name] = items[i].count
	}

	return trimmed
}

func (h *Handlers) updateKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Name            string   `json:"name"`
		Scopes          []string `json:"scopes"`
		ExpiresAt       string   `json:"expires_at"`
		ClearExpiration bool     `json:"clear_expiration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error", "invalid_request")
		return
	}

	key, err := h.Keys.Update(id, body.Name, body.Scopes)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error(), "not_found_error", "resource_not_found")
		return
	}

	if body.ClearExpiration {
		if err := h.Keys.SetExpiration(id, nil); err != nil {
			writeError(w, http.StatusNotFound, err.Error(), "not_found_error", "resource_not_found")
			return
		}
		key.ExpiresAt = nil
	} else if body.ExpiresAt != "" {
		expiresAt, parseErr := time.Parse(time.RFC3339, body.ExpiresAt)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid expires_at: must be RFC3339 format", "invalid_request_error", "invalid_request")
			return
		}
		if err := h.Keys.SetExpiration(id, &expiresAt); err != nil {
			writeError(w, http.StatusNotFound, err.Error(), "not_found_error", "resource_not_found")
			return
		}
		t := expiresAt
		key.ExpiresAt = &t
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(key)
}

func (h *Handlers) deleteKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Keys.Delete(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error(), "not_found_error", "resource_not_found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) revokeKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Keys.Revoke(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error(), "not_found_error", "resource_not_found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
}

func (h *Handlers) rotateKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key, err := h.Keys.RotateKey(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error(), "not_found_error", "resource_not_found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(key)
}

func (h *Handlers) listProviders(w http.ResponseWriter, _ *http.Request) {
	type providerInfo struct {
		Name   string                `json:"name"`
		Models []providers.ModelInfo `json:"models"`
	}

	var result []providerInfo
	if h.Providers != nil {
		for _, name := range h.Providers.List() {
			p, ok := h.Providers.Get(name)
			if !ok {
				continue
			}
			result = append(result, providerInfo{
				Name:   name,
				Models: p.Models(),
			})
		}
	}
	if result == nil {
		result = []providerInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *Handlers) healthCheck(w http.ResponseWriter, _ *http.Request) {
	type providerHealth struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Models  int    `json:"models"`
		Message string `json:"message,omitempty"`
	}

	var providerStatuses []providerHealth
	overallStatus := "healthy"

	if h.Providers != nil {
		for _, name := range h.Providers.List() {
			p, ok := h.Providers.Get(name)
			if !ok {
				providerStatuses = append(providerStatuses, providerHealth{
					Name:    name,
					Status:  "unavailable",
					Message: "provider not found in registry",
				})
				overallStatus = "degraded"
				continue
			}
			providerStatuses = append(providerStatuses, providerHealth{
				Name:   name,
				Status: "available",
				Models: len(p.Models()),
			})
		}
	}

	if providerStatuses == nil {
		providerStatuses = []providerHealth{}
		overallStatus = "no_providers"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    overallStatus,
		"providers": providerStatuses,
	})
}

func (h *Handlers) getConfig(w http.ResponseWriter, _ *http.Request) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.Configs.GetConfig())
}

func (h *Handlers) getConfigHistory(w http.ResponseWriter, _ *http.Request) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	h.historyMu.Lock()
	history := make([]ConfigHistoryEntry, len(h.configHistory))
	copy(history, h.configHistory)
	h.historyMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data": history,
		"summary": map[string]interface{}{
			"total_versions": len(history),
		},
	})
}

func (h *Handlers) updateConfig(w http.ResponseWriter, r *http.Request) {
	h.applyConfigUpdate(w, r, http.StatusOK, "updated")
}

func (h *Handlers) createConfig(w http.ResponseWriter, r *http.Request) {
	h.applyConfigUpdate(w, r, http.StatusCreated, "created")
}

func (h *Handlers) applyConfigUpdate(w http.ResponseWriter, r *http.Request, statusCode int, statusText string) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	var cfg aigateway.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request_error", "invalid_request")
		return
	}

	if err := h.Configs.ReloadConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_config")
		return
	}

	h.appendConfigHistory(cfg, nil)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": statusText})
}

func (h *Handlers) deleteConfig(w http.ResponseWriter, _ *http.Request) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	resetter, ok := h.Configs.(ConfigResetter)
	if !ok {
		writeError(w, http.StatusNotImplemented, "config reset is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	if err := resetter.ResetConfig(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error", "internal_error")
		return
	}

	h.appendConfigHistory(h.Configs.GetConfig(), nil)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

func (h *Handlers) rollbackConfig(w http.ResponseWriter, r *http.Request) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	requestedVersion, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || requestedVersion <= 0 {
		writeError(w, http.StatusBadRequest, "invalid version: must be a positive integer", "invalid_request_error", "invalid_request")
		return
	}

	h.historyMu.Lock()
	var target *ConfigHistoryEntry
	latestVersion := 0
	if len(h.configHistory) > 0 {
		latestVersion = h.configHistory[len(h.configHistory)-1].Version
	}
	for i := range h.configHistory {
		if h.configHistory[i].Version == requestedVersion {
			copyEntry := h.configHistory[i]
			target = &copyEntry
			break
		}
	}
	h.historyMu.Unlock()

	if target == nil {
		writeError(w, http.StatusNotFound, "config version not found", "not_found_error", "resource_not_found")
		return
	}

	if err := h.Configs.ReloadConfig(target.Config); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_config")
		return
	}

	rollbackFrom := latestVersion
	h.appendConfigHistory(target.Config, &rollbackFrom)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":               "rolled_back",
		"rolled_back_to":       requestedVersion,
		"current_history_size": len(h.getConfigHistorySnapshot()),
	})
}

func (h *Handlers) appendConfigHistory(cfg aigateway.Config, rolledBackFrom *int) {
	h.historyMu.Lock()
	defer h.historyMu.Unlock()
	h.configHistory = append(h.configHistory, ConfigHistoryEntry{
		Version:        len(h.configHistory) + 1,
		UpdatedAt:      time.Now().UTC(),
		Config:         cfg,
		RolledBackFrom: rolledBackFrom,
	})
}

func (h *Handlers) getConfigHistorySnapshot() []ConfigHistoryEntry {
	h.historyMu.Lock()
	defer h.historyMu.Unlock()
	history := make([]ConfigHistoryEntry, len(h.configHistory))
	copy(history, h.configHistory)
	return history
}
