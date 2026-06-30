package middleware

import (
	"net"
	"net/http"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
)

// RateLimit returns middleware that enforces per-IP token-bucket rate limiting.
//
// The rate-limit bucket is keyed on the host portion of r.RemoteAddr only (no
// port). r.RemoteAddr is expected to have already been resolved to the real
// client IP by RealIPMiddleware, which honors X-Forwarded-For and X-Real-IP
// only when the direct TCP peer is within a trusted-proxy CIDR. That
// middleware must be installed before this one in the chain.
func RateLimit(store *ratelimit.Store) func(http.Handler) http.Handler {
	if store == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract the host without the port so ephemeral port variation
			// does not produce separate buckets for the same client.
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				// r.RemoteAddr is already a bare IP (set by RealIPMiddleware).
				host = r.RemoteAddr
			}
			if !store.Allow(host) {
				metrics.RateLimitRejections.WithLabelValues("ip").Inc()
				apierror.WriteOpenAI(w, http.StatusTooManyRequests,
					"rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
