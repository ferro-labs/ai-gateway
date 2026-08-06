// Package middleware provides HTTP middleware for the gateway server.
package middleware

import "net/http"

// ContentSecurityPolicy is the policy served with every response, API and
// dashboard alike.
//
// Gateway responses do not rely on inline scripts, remote fonts, or
// third-party runtime assets, so browser execution stays restricted to
// same-origin resources.
//
// The one hash in style-src allowlists a single fixed stylesheet the component
// library injects to hide the native scrollbar inside an open dropdown. It is a
// constant string, so the digest is reproducible, and allowing it by digest
// keeps 'unsafe-inline' out — which would otherwise disable every digest in the
// same directive and permit any inline style on a page that manages
// credentials. If the library is upgraded and that string changes, the dropdown
// shows a scrollbar and the console reports a violation; web/src/lib/csp.test.ts
// recomputes the digest from the installed library and fails on a mismatch.
//
// connect-src needs no origin beyond 'self' because the dashboard is served by
// the gateway it calls, so its API requests are same-origin.
const ContentSecurityPolicy = "default-src 'self'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"object-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self' 'sha256-kLmvWqfziFavKtqHqRsb90f006UAK2Dmd0It5Iz2KFA='; " +
	"font-src 'self'; " +
	"img-src 'self' data:; " +
	"connect-src 'self'"

// PermissionsPolicy denies browser features the dashboard never uses.
const PermissionsPolicy = "camera=(), microphone=(), geolocation=()"

// SecurityHeaders returns middleware that sets baseline browser-hardening
// headers on every response — both the API's JSON and the dashboard HTML the
// gateway serves. These headers are unconditional except for
// Strict-Transport-Security, which is only emitted when the connection is TLS
// (i.e. r.TLS != nil) to avoid breaking plain-HTTP deployments. "preload" is
// deliberately omitted: it is an irreversible commitment for the whole domain
// and is the operator's decision, not the gateway's.
//
// Headers applied:
//   - Content-Security-Policy: see ContentSecurityPolicy
//   - Permissions-Policy: see PermissionsPolicy
//   - X-Content-Type-Options: nosniff
//   - X-Frame-Options: DENY
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Strict-Transport-Security: max-age=31536000; includeSubDomains (TLS only)
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("Permissions-Policy", PermissionsPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}
