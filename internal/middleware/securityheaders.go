// Package middleware provides HTTP middleware for the gateway server.
package middleware

import "net/http"

// SecurityHeaders returns middleware that sets baseline browser-hardening
// headers on every response. These headers are unconditional except for
// Strict-Transport-Security, which is only emitted when the connection is TLS
// (i.e. r.TLS != nil) to avoid breaking plain-HTTP deployments.
//
// Headers applied:
//   - X-Content-Type-Options: nosniff
//   - X-Frame-Options: DENY
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Strict-Transport-Security: max-age=31536000; includeSubDomains (TLS only)
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}
