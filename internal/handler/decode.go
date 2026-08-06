package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
)

// errTrailingJSON reports a body carrying more than one JSON document.
var errTrailingJSON = errors.New("unexpected data after top-level JSON value")

// decodeSingleJSON decodes exactly one JSON document from r into dst and fails
// if anything but whitespace follows it.
//
// json.Decoder reads a *stream*, so on its own it accepts `{...}{...}` and
// silently discards everything after the first document. OpenAI 400s such a
// body, and a caller whose real request was the second document has no way to
// tell it was thrown away.
//
// Read errors are returned unwrapped so callers keep classifying them: an
// *http.MaxBytesError must still be a 413 whether the limit is reached inside
// the document or in the bytes after it.
func decodeSingleJSON(r io.Reader, dst any) error {
	dec := json.NewDecoder(r)
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// Token rather than a second Decode: it needs no destination value, so the
	// common case — nothing follows — stays allocation-free on the request path.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errTrailingJSON
	}
	return nil
}

// decodeJSONBody decodes the JSON request body into dst. On failure it writes
// an OpenAI-format error response and returns false; callers should stop
// processing when it returns false. A body that exceeds the configured size
// limit yields 413; any other decode failure yields 400.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := decodeSingleJSON(r.Body, dst); err != nil {
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
