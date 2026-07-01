package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/go-chi/chi/v5"
)

// writeKeyStoreError maps an API key store error to an HTTP response. A genuine
// not-found (errors.Is(err, ErrKeyNotFound)) becomes 404 with the key id echoed;
// any other error is an internal or transient store failure, reported as 500
// with a generic message so wrapped database/internal text is never leaked to
// callers, and logged server-side for operators.
func writeKeyStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrKeyNotFound) {
		writeError(w, http.StatusNotFound, err.Error(), "not_found_error", "resource_not_found")
		return
	}
	logging.Logger.Error("admin key store operation failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
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

	key, err := h.Keys.Create(r.Context(), body.Name, body.Scopes, expiresAt)
	if err != nil {
		logging.Logger.Error("admin create key failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(key)
}

func (h *Handlers) listKeys(w http.ResponseWriter, r *http.Request) {
	keys := h.Keys.List(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(keys)
}

func (h *Handlers) getKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key, ok := h.Keys.Get(r.Context(), id)
	if !ok {
		writeError(w, http.StatusNotFound, "key not found", "not_found_error", "resource_not_found")
		return
	}

	masked := *key
	masked.Key = maskKey(masked.Key)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(masked)
}

//nolint:gocyclo // Query parsing + filtering/sorting logic is intentionally centralized for the endpoint.
func (h *Handlers) keyUsage(w http.ResponseWriter, r *http.Request) {
	limit, ok := parseLimit(w, r, defaultKeyUsageLimit, maxKeyUsageLimit)
	if !ok {
		return
	}

	offset, ok := parseOffset(w, r)
	if !ok {
		return
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

	sinceFilter, ok := parseSince(w, r)
	if !ok {
		return
	}

	filteredKeys := make([]*APIKey, 0)
	for _, key := range h.Keys.List(r.Context()) {
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
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": keys,
		"summary": map[string]any{
			"total_keys":    len(filteredKeys),
			"active_keys":   activeKeys,
			"total_usage":   totalUsage,
			"returned_keys": len(keys),
		},
		"filters": map[string]any{
			"limit":  limit,
			"offset": offset,
			"sort":   sortBy,
			"active": activeFilter,
			"since":  r.URL.Query().Get("since"),
		},
	})
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

	key, err := h.Keys.Update(r.Context(), id, body.Name, body.Scopes)
	if err != nil {
		writeKeyStoreError(w, err)
		return
	}

	if body.ClearExpiration {
		if err := h.Keys.SetExpiration(r.Context(), id, nil); err != nil {
			writeKeyStoreError(w, err)
			return
		}
		key.ExpiresAt = nil
	} else if body.ExpiresAt != "" {
		expiresAt, parseErr := time.Parse(time.RFC3339, body.ExpiresAt)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid expires_at: must be RFC3339 format", "invalid_request_error", "invalid_request")
			return
		}
		if err := h.Keys.SetExpiration(r.Context(), id, &expiresAt); err != nil {
			writeKeyStoreError(w, err)
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
	if err := h.Keys.Delete(r.Context(), id); err != nil {
		writeKeyStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) revokeKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Keys.Revoke(r.Context(), id); err != nil {
		writeKeyStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
}

func (h *Handlers) rotateKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key, err := h.Keys.RotateKey(r.Context(), id)
	if err != nil {
		writeKeyStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(key)
}
