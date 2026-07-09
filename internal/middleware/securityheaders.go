// Package middleware provides HTTP middleware for the gateway server.
package middleware

import "net/http"

// ContentSecurityPolicy is the policy served with every response.
//
// script-src is strict: the admin token lives in localStorage, so blocking
// injected and inline script is the one directive that matters here. The
// dashboard therefore carries no inline <script> and no on*= attributes —
// handlers are registered against data-action in web/static/dashboard.js, and
// TestTemplatesCarryNoInlineScript keeps it that way.
//
// style-src keeps 'unsafe-inline': the templates still use style="…" attributes
// and login.html an inline <style> block. Inline style is a far weaker vector
// than inline script, and tightening it would buy little for a large diff.
// cdn.jsdelivr.net serves the Geist font stylesheet imported by style.css.
const ContentSecurityPolicy = "default-src 'self'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"object-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; " +
	"font-src 'self' https://cdn.jsdelivr.net; " +
	"img-src 'self' data:; " +
	"connect-src 'self'"

// PermissionsPolicy denies browser features the dashboard never uses.
const PermissionsPolicy = "camera=(), microphone=(), geolocation=()"

// SecurityHeaders returns middleware that sets baseline browser-hardening
// headers on every response. These headers are unconditional except for
// Strict-Transport-Security, which is only emitted when the connection is TLS
// (i.e. r.TLS != nil) to avoid breaking plain-HTTP deployments.
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
