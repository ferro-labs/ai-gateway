// Package admin provides HTTP handlers for the gateway administration API.
// Routes expose API key management and provider model listing.
// All admin routes are protected by bearer-token authentication via AuthMiddleware.
package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/go-chi/chi/v5"
)

// Handlers holds dependencies for admin HTTP handlers.
type Handlers struct {
	Keys      Store
	Providers providers.ProviderSource
}

// Routes returns a chi.Router with all admin endpoints mounted.
func (h *Handlers) Routes() chi.Router {
	r := chi.NewRouter()

	// Read-only endpoints (accessible with read-only or admin scope).
	r.Group(func(r chi.Router) {
		r.Use(RequireScope(ScopeReadOnly, ScopeAdmin))
		r.Get("/keys", h.listKeys)
		r.Get("/providers", h.listProviders)
		r.Get("/health", h.healthCheck)
	})

	// Write endpoints (admin scope only).
	r.Group(func(r chi.Router) {
		r.Use(RequireScope(ScopeAdmin))
		r.Post("/keys", h.createKey)
		r.Put("/keys/{id}", h.updateKey)
		r.Delete("/keys/{id}", h.deleteKey)
		r.Post("/keys/{id}/revoke", h.revokeKey)
		r.Post("/keys/{id}/rotate", h.rotateKey)
	})

	return r
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

func (h *Handlers) updateKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
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
