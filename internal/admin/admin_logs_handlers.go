package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/requestlog"
)

func (h *Handlers) listLogs(w http.ResponseWriter, r *http.Request) {
	if h.Logs == nil {
		writeError(w, http.StatusNotImplemented, "request log storage is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	limit, ok := parseLimit(w, r, defaultLogsLimit, maxLogsLimit)
	if !ok {
		return
	}

	offset, ok := parseOffset(w, r)
	if !ok {
		return
	}

	since, ok := parseSince(w, r)
	if !ok {
		return
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
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": result.Data,
		"summary": map[string]any{
			"total_entries":    result.Total,
			"returned_entries": len(result.Data),
		},
		"filters": map[string]any{
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
	_ = json.NewEncoder(w).Encode(map[string]any{
		"deleted": deleted,
		"filters": map[string]any{
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

	limit, ok := parseLimit(w, r, 0, maxLogsStatsLimit)
	if !ok {
		return
	}

	since, ok := parseSince(w, r)
	if !ok {
		return
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

	entries := make([]requestlog.Entry, 0, min(result.Total, logsStatsMaxScannedEntries))
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
	_ = json.NewEncoder(w).Encode(map[string]any{
		"summary": map[string]any{
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
		"filters": map[string]any{
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
