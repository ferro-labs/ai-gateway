package middleware

import (
	"net"
	"net/http"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/metrics"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
)

// RateLimitGlobal returns middleware that enforces a single process-wide token
// bucket, shared by every caller regardless of source address.
//
// This is the right shape for an unauthenticated endpoint whose cost is borne by
// a shared backing resource rather than by the caller. A per-IP bucket cannot
// bound such an endpoint: each distinct source address is handed its own full
// allowance, so a distributed caller multiplies the ceiling, and the per-key map
// grows with every new address it presents. One bucket has neither property — it
// caps the total rate and allocates no per-caller state.
//
// The trade-off is deliberate: one caller can consume the whole allowance and
// starve others. Only use it where a global ceiling matters more than fairness
// between callers.
func RateLimitGlobal(limiter *ratelimit.Limiter, rejectionLabel string) func(http.Handler) http.Handler {
	if limiter == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow() {
				metrics.RateLimitRejections.WithLabelValues(rejectionLabel).Inc()
				apierror.WriteOpenAI(w, http.StatusTooManyRequests,
					"rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

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
