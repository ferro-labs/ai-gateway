package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/authctx"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/go-chi/chi/v5"
)

// writeKeyStoreError maps an API key store error to an HTTP response. A genuine
// not-found (errors.Is(err, ErrKeyNotFound)) becomes 404 with the key id echoed;
// any other error is an internal or transient store failure, reported as 500
// with a generic message so wrapped database/internal text is never leaked to
// callers, and logged server-side for operators.
func writeKeyStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, model.ErrKeyNotFound) {
		writeError(w, http.StatusNotFound, err.Error(), "not_found_error", "resource_not_found")
		return
	}
	logger.Default().Error("admin key store operation failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
}

// Machine-readable codes for the two lockout refusals. Both are 409 Conflict:
// the request is well-formed and the caller is authorized, but the store's
// current state makes the change unsafe.
const (
	codeSelfMutation = "self_mutation_forbidden"
	codeLastAdminKey = "last_admin_key"
)

// codeInvalidScope marks a request naming a scope the gateway does not issue.
// It is 400, not 409: nothing about the store's state makes the request
// unsafe, the request itself is malformed. The name matches OAuth 2.0's
// invalid_scope so a client already handling that code needs no new branch.
const codeInvalidScope = "invalid_scope"

// guardKeyMutation refuses a key change that would lock an operator out of the
// Admin API. It writes a 409 and reports false when it refuses; a true return
// means the caller may proceed.
//
// action is the audit action of the mutation being guarded, because a refusal is
// recorded exactly as the config control plane records one: "someone tried to
// delete the last admin key" is a question the trail is read to answer, and a
// store holding only accepted changes answers it with a silence
// indistinguishable from nobody having tried.
//
// Two refusals are enforced:
//
//   - Self-mutation: a credential cannot delete or revoke itself, nor drop its
//     own admin scope. Rotation is deliberately not guarded — the rotate
//     response carries the new secret, so the caller keeps a working credential
//     and is never locked out.
//   - Last admin: the final key that can authenticate an admin request cannot be
//     deleted, revoked, or stripped of that scope.
//
// removesAdmin says whether the mutation takes away the target's ability to
// authenticate as an admin. Delete and revoke always do; an update only does
// when the replacement scope set drops "admin".
//
// The last-admin count looks at stored keys only and ignores whether MASTER_KEY
// is set. The master key may be unset, rotated away, or simply not visible from
// a request, so a guard whose behaviour depended on that invisible env state
// would answer differently on two identical-looking deployments. Counting
// stored keys alone is slightly over-restrictive — an operator who genuinely
// wants no stored admin keys can create a second one, delete the first, then
// delete the second — and it always answers the same way.
//
// Callers hold h.keysMu, so the count and the mutation it authorizes cannot be
// separated by another request in this process.
//
// The comparison uses authctx.KeyID rather than APIKeyFromContext(ctx).ID.
// For a direct API key the two are the same value (storeKeyInContext buckets
// under key.ID when no session is involved), but for a dashboard session
// APIKeyFromContext(ctx).ID is the synthetic "session:"-prefixed session ID,
// which can never equal a real key ID — comparing that would silently disable
// this guard for every session. authctx.KeyID carries the session's *source*
// credential ID instead, which is the identity this guard actually needs to
// ask "which credential is the caller" about.
func (h *Handlers) guardKeyMutation(w http.ResponseWriter, r *http.Request, action, id string, removesAdmin bool) bool {
	if callerID, ok := authctx.KeyID(r.Context()); ok && callerID == id {
		h.recordAudit(r, action, id, model.AuditDenied, "reason", codeSelfMutation)
		writeError(w, http.StatusConflict,
			"cannot delete, revoke, or de-scope the credential this request authenticated with",
			"invalid_request_error", codeSelfMutation)
		return false
	}

	if !removesAdmin {
		return true
	}

	// A key that cannot authenticate as an admin today — missing the scope, or
	// already revoked — is not holding anyone's access open, so removing it is
	// always safe. A missing key falls through to the store's own 404.
	target, ok := h.Keys.Get(r.Context(), id)
	if !ok || !model.IsUsableAdmin(target) {
		return true
	}

	count, err := h.Keys.CountAdminKeys(r.Context())
	if err != nil {
		h.recordAudit(r, action, id, model.AuditError, "error", err.Error())
		writeKeyStoreError(w, err)
		return false
	}
	if count <= 1 {
		h.recordAudit(r, action, id, model.AuditDenied, "reason", codeLastAdminKey)
		writeError(w, http.StatusConflict,
			"cannot remove the last admin key: create another admin key first",
			"invalid_request_error", codeLastAdminKey)
		return false
	}
	return true
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
	if err := model.ValidateScopes(body.Scopes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", codeInvalidScope)
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
		// No key exists to name, so the target is the whole store: the trail
		// still records that a credential was asked for and the store refused.
		h.recordAudit(r, "key.create", "*", model.AuditError, "name", body.Name, "error", err.Error())
		logger.Default().Error("admin create key failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
		return
	}

	h.recordAudit(r, "key.create", key.ID, model.AuditOK, "name", key.Name, "scopes", key.Scopes)

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

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(key)
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

	filteredKeys := make([]*model.APIKey, 0)
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

	keys := make([]*model.APIKey, 0)
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
	h.keysMu.Lock()
	defer h.keysMu.Unlock()

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
	// Ahead of the lockout guards below on purpose. A misspelled scope does not
	// grant admin, so it reads as a de-scope and would otherwise be answered
	// with a 409 about self-mutation or the last admin key — telling the
	// operator about a rule they did not break while the typo stays invisible.
	if err := model.ValidateScopes(body.Scopes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", codeInvalidScope)
		return
	}

	var expiresAt *time.Time
	if !body.ClearExpiration && body.ExpiresAt != "" {
		parsed, parseErr := time.Parse(time.RFC3339, body.ExpiresAt)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid expires_at: must be RFC3339 format", "invalid_request_error", "invalid_request")
			return
		}
		expiresAt = &parsed
	}

	// An empty scopes list leaves scopes untouched, and a replacement that still
	// grants admin takes nothing away. Only an actual de-scope is guarded, so
	// renaming or scheduling a future expiry on one's own key stays allowed.
	deScopes := len(body.Scopes) > 0 && !model.HasAdminScope(body.Scopes)

	// An expiry at or before now is a revocation wearing a different name: the
	// key stops validating the moment this returns. Guard it exactly as revoke
	// is guarded, or the whole guard is bypassable by sending a past timestamp.
	//
	// A *future* expiry is deliberately not guarded. It is a clock event no
	// check here can prevent — any key counted as an admin today can expire a
	// moment after the count — so refusing it would buy nothing while breaking
	// the legitimate "retire this key tomorrow" workflow.
	expiresImmediately := expiresAt != nil && !expiresAt.After(time.Now())

	if deScopes || expiresImmediately {
		if !h.guardKeyMutation(w, r, "key.update", id, true) {
			return
		}
	}

	key, err := h.Keys.Update(r.Context(), id, body.Name, body.Scopes)
	if err != nil {
		h.recordAudit(r, "key.update", id, model.AuditError, "error", err.Error())
		writeKeyStoreError(w, err)
		return
	}

	if body.ClearExpiration {
		if err := h.Keys.SetExpiration(r.Context(), id, nil); err != nil {
			h.recordAudit(r, "key.update", id, model.AuditError, "error", err.Error())
			writeKeyStoreError(w, err)
			return
		}
		key.ExpiresAt = nil
	} else if expiresAt != nil {
		if err := h.Keys.SetExpiration(r.Context(), id, expiresAt); err != nil {
			h.recordAudit(r, "key.update", id, model.AuditError, "error", err.Error())
			writeKeyStoreError(w, err)
			return
		}
		t := *expiresAt
		key.ExpiresAt = &t
	}

	// Scopes are recorded because an update is how a key silently gains or
	// loses admin, which a bare "key.update" would not distinguish.
	h.recordAudit(r, "key.update", id, model.AuditOK, "name", key.Name, "scopes", key.Scopes)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(key)
}

func (h *Handlers) deleteKey(w http.ResponseWriter, r *http.Request) {
	h.keysMu.Lock()
	defer h.keysMu.Unlock()

	id := chi.URLParam(r, "id")
	if !h.guardKeyMutation(w, r, "key.delete", id, true) {
		return
	}
	if err := h.Keys.Delete(r.Context(), id); err != nil {
		h.recordAudit(r, "key.delete", id, model.AuditError, "error", err.Error())
		writeKeyStoreError(w, err)
		return
	}
	h.recordAudit(r, "key.delete", id, model.AuditOK)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) revokeKey(w http.ResponseWriter, r *http.Request) {
	h.keysMu.Lock()
	defer h.keysMu.Unlock()

	id := chi.URLParam(r, "id")
	if !h.guardKeyMutation(w, r, "key.revoke", id, true) {
		return
	}
	if err := h.Keys.Revoke(r.Context(), id); err != nil {
		h.recordAudit(r, "key.revoke", id, model.AuditError, "error", err.Error())
		writeKeyStoreError(w, err)
		return
	}
	h.recordAudit(r, "key.revoke", id, model.AuditOK)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
}

func (h *Handlers) rotateKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key, err := h.Keys.RotateKey(r.Context(), id)
	if err != nil {
		h.recordAudit(r, "key.rotate", id, model.AuditError, "error", err.Error())
		writeKeyStoreError(w, err)
		return
	}
	h.recordAudit(r, "key.rotate", id, model.AuditOK)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(key)
}
