package middleware

import (
	"net/http"
)

// MaxRequestBody returns middleware that limits the size of the request body to
// limit bytes. The body is wrapped with http.MaxBytesReader so that once the
// limit is reached, any further Read call returns *http.MaxBytesError.
//
// Handlers that read the body (via io.ReadAll or json.Decoder.Decode) must
// check for *http.MaxBytesError using errors.As and return HTTP 413.
func MaxRequestBody(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}
