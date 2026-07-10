package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/go-chi/chi/v5"
)

// maxConfigHistoryEntries caps the in-memory config history so long-running
// gateways with frequent reloads (e.g. GitOps/CI-driven sync) don't grow the
// history slice — and the full aigateway.Config snapshots it holds — without
// bound.
const maxConfigHistoryEntries = 200

// ConfigHistoryEntry captures a runtime config update snapshot.
type ConfigHistoryEntry struct {
	Version        int              `json:"version"`
	UpdatedAt      time.Time        `json:"updated_at"`
	Config         aigateway.Config `json:"config"`
	RolledBackFrom *int             `json:"rolled_back_from,omitempty"`
}

func (h *Handlers) getConfig(w http.ResponseWriter, _ *http.Request) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(scrubConfigSecrets(h.Configs.GetConfig()))
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

	// Redact secret-bearing config values on each copied entry before encoding.
	// scrubConfigSecrets operates on a copy so the live history is never mutated.
	for i := range history {
		history[i].Config = scrubConfigSecrets(history[i].Config)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": history,
		"summary": map[string]any{
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

	if err := h.Configs.ReloadConfig(r.Context(), cfg); err != nil {
		writeConfigReloadError(w, err)
		return
	}

	h.appendConfigHistory(cfg, nil)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": statusText})
}

func (h *Handlers) deleteConfig(w http.ResponseWriter, r *http.Request) {
	if h.Configs == nil {
		writeError(w, http.StatusNotImplemented, "config management is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	resetter, ok := h.Configs.(ConfigResetter)
	if !ok {
		writeError(w, http.StatusNotImplemented, "config reset is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	if err := resetter.ResetConfig(r.Context()); err != nil {
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

	if err := h.Configs.ReloadConfig(r.Context(), target.Config); err != nil {
		writeConfigReloadError(w, err)
		return
	}

	rollbackFrom := latestVersion
	h.appendConfigHistory(target.Config, &rollbackFrom)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":               "rolled_back",
		"rolled_back_to":       requestedVersion,
		"current_history_size": len(h.getConfigHistorySnapshot()),
	})
}

func (h *Handlers) appendConfigHistory(cfg aigateway.Config, rolledBackFrom *int) {
	h.historyMu.Lock()
	defer h.historyMu.Unlock()

	// Derive the next version from the last entry's Version rather than the
	// slice length: once old entries are evicted below, length no longer
	// tracks the cumulative version count.
	nextVersion := 1
	if n := len(h.configHistory); n > 0 {
		nextVersion = h.configHistory[n-1].Version + 1
	}

	h.configHistory = append(h.configHistory, ConfigHistoryEntry{
		Version:        nextVersion,
		UpdatedAt:      time.Now().UTC(),
		Config:         cfg,
		RolledBackFrom: rolledBackFrom,
	})

	if len(h.configHistory) > maxConfigHistoryEntries {
		h.configHistory = h.configHistory[len(h.configHistory)-maxConfigHistoryEntries:]
	}
}

func writeConfigReloadError(w http.ResponseWriter, err error) {
	if errors.Is(err, errConfigPersistence) {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error", "internal_error")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_config")
}

func (h *Handlers) getConfigHistorySnapshot() []ConfigHistoryEntry {
	h.historyMu.Lock()
	defer h.historyMu.Unlock()
	history := make([]ConfigHistoryEntry, len(h.configHistory))
	copy(history, h.configHistory)
	return history
}
