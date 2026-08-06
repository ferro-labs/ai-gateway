package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/admin/repository"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/go-chi/chi/v5"
)

// createSession exchanges a long-lived credential for a short-lived session.
//
// This route is mounted outside AuthMiddleware because it is how a caller
// obtains the credential that middleware checks. It authenticates the presented
// bearer itself, using the same chain, and mints nothing unless that succeeds.
func (h *Handlers) createSession(w http.ResponseWriter, r *http.Request) {
	if h.Sessions == nil || h.Credentials == nil {
		writeError(w, http.StatusNotImplemented, "session authentication is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	auth := r.Header.Get("Authorization")
	if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "missing or invalid authorization header", "authentication_error", "missing_api_key")
		return
	}

	identity, ok := h.Credentials(r.Context(), strings.TrimPrefix(auth, "Bearer "))
	if !ok {
		// The highest-value audit row: a real credential presented and rejected.
		// A burst of these from one source is a brute-force attempt, and until it
		// was recorded the only trace was a rate-limiter metric with no actor and
		// no address. The presented bearer is deliberately not in the record —
		// echoing it would put the very secret being guessed into the trail — so
		// the row carries the source IP and nothing that identifies the caller,
		// which is all that is truthfully known.
		h.recordAudit(r, "session.create", "*", model.AuditDenied, "reason", "invalid_api_key")
		writeError(w, http.StatusUnauthorized, "invalid or revoked API key", "authentication_error", "invalid_api_key")
		return
	}

	sess, token, err := h.Sessions.CreateSession(r.Context(), identity.Name, identity.ID, identity.Scopes, repository.DefaultSessionTTL)
	if err != nil {
		logger.Default().Error("session creation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
		return
	}

	// The accepted credential is the actor, but it is not in this request's
	// context — this endpoint authenticates the bearer itself, ahead of the auth
	// middleware — so the identity is stamped explicitly rather than read from
	// ActorFromContext, which would see no authenticated caller and record the
	// sign-in as unattributed.
	h.recordAuditAs(r, identity, "session.create", identity.ID, model.AuditOK, "subject", identity.Name)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      token,
		"subject":    sess.Subject,
		"scopes":     sess.Scopes,
		"expires_at": sess.ExpiresAt,
	})
}

// deleteSession signs out the session that authenticated this request. It
// removes the row rather than marking it, so the token stops validating
// immediately.
func (h *Handlers) deleteSession(w http.ResponseWriter, r *http.Request) {
	if h.Sessions == nil {
		writeError(w, http.StatusNotImplemented, "session authentication is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	caller, ok := model.APIKeyFromContext(r.Context())
	if !ok || !strings.HasPrefix(caller.ID, repository.SessionIdentityPrefix) {
		writeError(w, http.StatusBadRequest, "this request was not authenticated with a session", "invalid_request_error", "not_a_session")
		return
	}

	id := strings.TrimPrefix(caller.ID, repository.SessionIdentityPrefix)
	if err := h.Sessions.DeleteSession(r.Context(), id); err != nil && !errors.Is(err, repository.ErrSessionNotFound) {
		h.recordAudit(r, "session.logout", id, model.AuditError, "error", err.Error())
		logger.Default().Error("session deletion failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
		return
	}
	// A sign-in is audited, so the sign-out that ends it is too: without this row
	// the trail shows a session opening and never closing, and "was that operator
	// still signed in when the config changed" has no answer. The target is the
	// session id, which is opaque — the token never reaches this handler.
	h.recordAudit(r, "session.logout", id, model.AuditOK)
	w.WriteHeader(http.StatusNoContent)
}

// listSessions returns every currently live dashboard session.
func (h *Handlers) listSessions(w http.ResponseWriter, r *http.Request) {
	if h.Sessions == nil {
		writeError(w, http.StatusNotImplemented, "session authentication is not enabled", "not_implemented_error", "not_implemented")
		return
	}
	sessions, err := h.Sessions.ListSessions(r.Context())
	if err != nil {
		logger.Default().Error("session list failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": sessions})
}

// revokeSession signs one operator out, leaving every other session live.
//
// Without it the only revocation control was deleteAllSessions, which signs the
// acting operator out too — so removing one departing person's access meant
// interrupting everyone, including whoever was mid-incident.
//
// An unknown id is a success, exactly as deleteSession treats
// ErrSessionNotFound: the caller asked for that session to stop working, and a
// session that does not exist already satisfies that. Reporting 404 would also
// turn the endpoint into an oracle for which session ids are live.
func (h *Handlers) revokeSession(w http.ResponseWriter, r *http.Request) {
	if h.Sessions == nil {
		writeError(w, http.StatusNotImplemented, "session authentication is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.Sessions.DeleteSession(r.Context(), id); err != nil && !errors.Is(err, repository.ErrSessionNotFound) {
		logger.Default().Error("session revoke failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
		return
	}
	// Cutting off another operator's access is a credential change, so it is
	// attributed like one. The session id is the target; it is an opaque
	// identifier, not the token, which never reaches this handler.
	h.recordAudit(r, "session.revoke", id, model.AuditOK)
	w.WriteHeader(http.StatusNoContent)
}

// deleteAllSessions signs every operator out. It is the "rotate the session
// secret" control without a secret to rotate.
func (h *Handlers) deleteAllSessions(w http.ResponseWriter, r *http.Request) {
	if h.Sessions == nil {
		writeError(w, http.StatusNotImplemented, "session authentication is not enabled", "not_implemented_error", "not_implemented")
		return
	}
	n, err := h.Sessions.DeleteAllSessions(r.Context())
	if err != nil {
		logger.Default().Error("session revoke-all failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error", "server_error", "internal_error")
		return
	}
	// Signs out every operator, so it is the most disruptive control the admin
	// API exposes and the one most worth attributing. The target is the whole
	// session store rather than one row.
	h.recordAudit(r, "session.revoke_all", "*", model.AuditOK, "count", n)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"revoked": n})
}

// CreateSessionHandler exposes the session-exchange handler for mounting
// outside the authenticated admin router.
func (h *Handlers) CreateSessionHandler() http.HandlerFunc { return h.createSession }
