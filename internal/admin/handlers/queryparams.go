package handlers

import (
	"net/http"
	"strconv"
	"time"
)

// Limit defaults and clamp ceilings for the admin list endpoints. Ceilings
// differ per endpoint, so callers pass the applicable bound explicitly.
const (
	defaultKeyUsageLimit = 20
	maxKeyUsageLimit     = 100
	defaultLogsLimit     = 50
	maxLogsLimit         = 200
	maxLogsStatsLimit    = 100
	// maxLogsStatsBuckets caps the resolution of the stats time series. A
	// dashboard cannot draw more points than a chart has pixels, and every
	// bucket is a payload entry.
	maxLogsStatsBuckets = 120
	// logsStatsTopErrors is how many distinct failure messages the stats
	// endpoint ranks. Fixed rather than caller-controlled: it is a display
	// concern with no reason to vary, and error text is unbounded in size.
	logsStatsTopErrors = 8
	// stageAll opts GET /admin/logs out of its one-row-per-request default and
	// back to every logged row. A reserved value rather than a second parameter
	// because it occupies the same slot as a stage name and cannot collide with
	// one: the stages are before_request, after_request and on_error.
	stageAll = "all"
	// apiKeyNone asks GET /admin/logs for the rows that name no credential. A
	// reserved value rather than a second parameter, and safe in the same slot
	// as a credential id because it cannot be one: stored ids are UUIDs and the
	// master credential carries a prefixed synthetic id.
	apiKeyNone = "none"
	// Matched to the AuditStore's own clamp so a caller's page size means the
	// same thing at the handler and at the store, rather than the handler
	// forwarding a limit the store silently narrows.
	defaultAuditLimit = 50
	maxAuditLimit     = 200
)

// parseLimit reads the optional "limit" query parameter, returning def when it
// is absent. A non-integer or non-positive value writes a 400 response and
// reports false so the caller returns; a valid value is clamped to maxLimit.
func parseLimit(w http.ResponseWriter, r *http.Request, def, maxLimit int) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def, true
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		writeError(w, http.StatusBadRequest, "invalid limit: must be a positive integer", "invalid_request_error", "invalid_request")
		return 0, false
	}
	if parsed > maxLimit {
		parsed = maxLimit
	}
	return parsed, true
}

// parseOffset reads the optional "offset" query parameter, defaulting to 0. A
// non-integer or negative value writes a 400 response and reports false so the
// caller returns.
func parseOffset(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("offset")
	if raw == "" {
		return 0, true
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		writeError(w, http.StatusBadRequest, "invalid offset: must be a non-negative integer", "invalid_request_error", "invalid_request")
		return 0, false
	}
	return parsed, true
}

// parseBoundedQueryInt reads an optional non-negative integer query parameter,
// returning def when it is absent and clamping to maxValue. A non-integer or
// negative value writes a 400 response and reports false so the caller returns.
func parseBoundedQueryInt(w http.ResponseWriter, r *http.Request, name string, def, maxValue int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, true
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		writeError(w, http.StatusBadRequest, "invalid "+name+": must be a non-negative integer", "invalid_request_error", "invalid_request")
		return 0, false
	}
	if parsed > maxValue {
		parsed = maxValue
	}
	return parsed, true
}

// parseAPIKeyID reads the optional "api_key_id" query parameter, returning nil
// when it is absent so no filter is applied.
//
// The reserved value apiKeyNone resolves to a pointer to "", which selects the
// rows naming no credential (see requestlog.Query.APIKeyID). Any other value is
// matched exactly and is never validated against the key store: a credential
// that has since been deleted still has rows, and "who spent this" is asked
// about a deleted key more often than about a current one.
//
// An empty value is read as absent, like every other filter on this endpoint,
// so `?api_key_id=` is not a second — and silent — way to ask for the
// unattributed rows.
func parseAPIKeyID(r *http.Request) *string {
	raw := r.URL.Query().Get("api_key_id")
	switch raw {
	case "":
		return nil
	case apiKeyNone:
		none := ""
		return &none
	default:
		return &raw
	}
}

// parseSince reads the optional "since" query parameter as an RFC3339 timestamp,
// returning nil when it is absent. A malformed value writes a 400 response and
// reports false so the caller returns.
func parseSince(w http.ResponseWriter, r *http.Request) (*time.Time, bool) {
	raw := r.URL.Query().Get("since")
	if raw == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid since: must be RFC3339 format", "invalid_request_error", "invalid_request")
		return nil, false
	}
	return &parsed, true
}
