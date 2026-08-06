package middleware

import (
	"net"
	"net/http"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/pkg/metrics"
	"github.com/ferro-labs/ai-gateway/pkg/ratelimit"
)

// retryAfterSeconds is the Retry-After hint sent with every 429 this middleware
// writes.
//
// The header is not decoration. RFC 9110 §10.2.3 defines it and RFC 6585
// recommends it on 429, and the SDKs on the other side of this gateway read it:
// the OpenAI Go client prefers Retry-After-Ms and falls back to Retry-After,
// and the Anthropic and OpenRouter clients behave the same way. A 429 without
// it leaves those clients on a fixed short delay, so the limiter's own
// rejections are what produce the retry storm that follows.
//
// One second is the floor that is always honest: the bucket refills at
// RATE_LIMIT_RPS tokens per second, so at any rate of 1 rps or more a token
// exists by the time a client that waited this long comes back. Below 1 rps the
// hint is optimistic and the client may be shed once more — the ceiling of
// stating a constant rather than reading the bucket's own refill schedule.
// Deriving it per response is the upgrade path if sub-1-rps limits become a
// real configuration.
const retryAfterSeconds = "1"

// RateLimit returns middleware that enforces per-IP token-bucket rate limiting.
//
// The rate-limit bucket is keyed on the host portion of r.RemoteAddr only (no
// port). r.RemoteAddr is expected to have already been resolved to the real
// client IP by RealIPMiddleware, which honors X-Forwarded-For and X-Real-IP
// only when the direct TCP peer is within a trusted-proxy CIDR. That
// middleware must be installed before this one in the chain.
func RateLimit(store *ratelimit.Store) func(http.Handler) http.Handler {
	return RateLimitKeyed(store, "ip")
}

// RateLimitKeyed is RateLimit with the metrics label named by the caller.
//
// A route with its own store needs its own label, or its rejections land in the
// same counter as general traffic and there is no way to tell a burst of failed
// sign-ins from a busy gateway — which is the one thing the counter is worth
// watching for.
func RateLimitKeyed(store *ratelimit.Store, rejectionLabel string) func(http.Handler) http.Handler {
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
				metrics.RateLimitRejections.WithLabelValues(rejectionLabel).Inc()
				// Set before WriteOpenAI: it writes the status line, after
				// which the header map is no longer sent.
				w.Header().Set("Retry-After", retryAfterSeconds)
				apierror.WriteOpenAI(w, http.StatusTooManyRequests,
					"rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
