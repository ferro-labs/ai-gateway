package middleware

import (
	"net/http"
)

// MaxRequestBody returns middleware that limits the size of the request body to
// the value limit reports. The body is wrapped with http.MaxBytesReader so that
// once the limit is reached, any further Read call returns *http.MaxBytesError.
//
// Handlers that read the body (via io.ReadAll or json.Decoder.Decode) must
// check for *http.MaxBytesError using errors.As and return HTTP 413.
//
// limit is a function, not a value, because the cap is operator-settable at
// runtime: capturing it once at construction meant a lowered cap was accepted
// and reported by the admin API while the process kept enforcing the boot
// value — the reported number and the enforced one have to be the same number.
// The call happens once per request, before any body byte is read.
//
// A nil limit is a programming error and panics here, at wiring time, rather
// than serving unbounded bodies until the first request discovers it.
func MaxRequestBody(limit func() int64) func(http.Handler) http.Handler {
	if limit == nil {
		panic("middleware: MaxRequestBody requires a non-nil limit function")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit())
			next.ServeHTTP(w, r)
		})
	}
}
