package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/authctx"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// recordAudit records one administrative action to the structured log and,
// when an audit store is configured, to the durable trail.
//
// Until the trail existed, mutations were logged only when they failed: a
// successful deletion of the last admin key produced no record anywhere, which
// made "who deleted the admin key" unanswerable. Every audited action now leaves
// both a log line and a queryable row.
//
// The store write is best-effort and never fails the action being audited. The
// default backend is in-memory and the SQL one is a database that can be down;
// failing key management because its audit row could not be written trades a
// missing row for a broken admin API, which is the worse outcome. A failed
// append is logged and the request proceeds — and the log line above it is the
// fallback record, so the action is never entirely unrecorded. This is why the
// method takes the request rather than a context: source_ip comes from the
// resolved r.RemoteAddr (see RealIPMiddleware), which the trail exists to
// capture on a denied sign-in.
//
// attrs are alternating key/value pairs, as passed to a structured logger; they
// become the JSON detail on the stored row. The store redacts detail as a
// backstop, but callers must still pass only identifiers and names here, never a
// bearer secret.
func (h *Handlers) recordAudit(r *http.Request, action, targetID string, outcome model.AuditOutcome, attrs ...any) {
	ctx := r.Context()
	h.writeAudit(r, model.ActorFromContext(ctx), actorID(ctx), action, targetID, outcome, attrs)
}

// recordAuditAs records an action whose actor is known explicitly rather than
// through the request context.
//
// The session-exchange endpoint is the one caller: it authenticates the
// presented bearer itself, ahead of the auth middleware, so on a successful
// sign-in the accepted credential is not yet in context and ActorFromContext
// would record the sign-in as unattributed. The credential is passed straight
// in instead.
func (h *Handlers) recordAuditAs(r *http.Request, actor *model.APIKey, action, targetID string, outcome model.AuditOutcome, attrs ...any) {
	h.writeAudit(r, model.FormatActor(actor.Name, actor.ID), actor.ID, action, targetID, outcome, attrs)
}

// writeAudit is the shared body of recordAudit and recordAuditAs: it writes the
// log line and, when a store is configured, appends the durable row.
func (h *Handlers) writeAudit(r *http.Request, actor, actorID, action, targetID string, outcome model.AuditOutcome, attrs []any) {
	fields := append([]any{
		"action", action,
		"actor", actor,
		"target_id", targetID,
		"outcome", string(outcome),
	}, attrs...)
	logger.Default().Info("admin audit", fields...)

	if h.Audit == nil {
		return
	}

	entry := model.AuditEntry{
		Action:   action,
		Actor:    actor,
		ActorID:  actorID,
		TargetID: targetID,
		Outcome:  outcome,
		Detail:   auditDetail(attrs),
		SourceIP: r.RemoteAddr,
		TraceID:  logger.TraceIDFromContext(r.Context()),
	}
	if err := h.Audit.Append(r.Context(), entry); err != nil {
		// Deliberately not fatal to the request — see recordAudit's doc. Logged
		// at error level so a persistently failing trail is visible rather than
		// silently empty.
		logger.Default().Error("audit append failed", "action", action, "target_id", targetID, "error", err)
	}
}

// actorID returns the acting credential's id alone, for filtering one operator's
// history without a substring match on the display name.
//
// It prefers the session's source credential over the session id itself, for the
// reason ActorFromContext gives: the credential is the actor, the session is only
// how they were holding it. Empty when there is no authenticated caller — a
// denied sign-in has none, which is exactly when actor is unrecorded and only the
// source IP and trace id carry information.
func actorID(ctx context.Context) string {
	if id, ok := authctx.KeyID(ctx); ok && id != "" {
		return id
	}
	if key, ok := model.APIKeyFromContext(ctx); ok {
		return key.ID
	}
	return ""
}

// auditDetail renders alternating key/value log attrs as a JSON object for the
// stored row, or "" when there are none.
//
// Odd or non-string keys are dropped rather than guessed at: detail is a
// convenience field, and a malformed pair is not worth failing an audit write
// over. Anything genuinely load-bearing belongs in its own column.
func auditDetail(attrs []any) string {
	if len(attrs) < 2 {
		return ""
	}
	fields := make(map[string]any, len(attrs)/2)
	for i := 0; i+1 < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if !ok {
			continue
		}
		fields[key] = attrs[i+1]
	}
	if len(fields) == 0 {
		return ""
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	return string(encoded)
}
