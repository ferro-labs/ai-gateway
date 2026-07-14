package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
)

// decodeJSONBody decodes the JSON request body into dst. On failure it writes
// an OpenAI-format error response and returns false; callers should stop
// processing when it returns false. A body that exceeds the configured size
// limit yields 413; any other decode failure yields 400.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			apierror.WriteOpenAI(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "request_too_large")
			return false
		}
		apierror.WriteOpenAI(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_request_error", "invalid_request")
		return false
	}
	return true
}
