package admin

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
