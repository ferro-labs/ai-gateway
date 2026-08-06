package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// listAudit serves the audit trail as a filtered page.
//
// Nothing is added to the stored rows on the way out. AuditEntry is written
// redacted and carries no secret by construction (see model.AuditEntry), and
// this endpoint is read during an incident by more people than hold the
// credentials it describes — so a field synthesised here would be disclosed to
// all of them. The response is the stored rows plus the caller's own filters.
func (h *Handlers) listAudit(w http.ResponseWriter, r *http.Request) {
	if h.Audit == nil {
		writeError(w, http.StatusNotImplemented, "audit trail storage is not enabled", "not_implemented_error", "not_implemented")
		return
	}

	limit, ok := parseLimit(w, r, defaultAuditLimit, maxAuditLimit)
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

	outcome, ok := parseAuditOutcome(w, r)
	if !ok {
		return
	}

	query := model.AuditQuery{
		Limit:   limit,
		Offset:  offset,
		Action:  r.URL.Query().Get("action"),
		ActorID: r.URL.Query().Get("actor_id"),
		Outcome: outcome,
		Since:   since,
	}

	result, err := h.Audit.List(r.Context(), query)
	if err != nil {
		logger.Default().Error("audit list failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list audit entries", "server_error", "internal_error")
		return
	}

	filters := map[string]any{"limit": limit, "offset": offset}
	for name, value := range map[string]string{
		"action":   query.Action,
		"actor_id": query.ActorID,
		"outcome":  string(query.Outcome),
		"since":    r.URL.Query().Get("since"),
	} {
		if value != "" {
			filters[name] = value
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": result.Data,
		"summary": map[string]any{
			"total_entries":    result.Total,
			"returned_entries": len(result.Data),
		},
		"filters": filters,
	})
}

// parseAuditOutcome reads the optional "outcome" query parameter, returning ""
// when it is absent. An unrecognised value is refused rather than passed to the
// store: it would match no row, and an empty page is indistinguishable from
// "nothing was denied" — which is the one answer this trail is read to get.
func parseAuditOutcome(w http.ResponseWriter, r *http.Request) (model.AuditOutcome, bool) {
	raw := model.AuditOutcome(r.URL.Query().Get("outcome"))
	switch raw {
	case "", model.AuditOK, model.AuditDenied, model.AuditError:
		return raw, true
	default:
		writeError(w, http.StatusBadRequest, "invalid outcome: must be one of ok, denied, error", "invalid_request_error", "invalid_request")
		return "", false
	}
}
