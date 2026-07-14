package httpserver

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// DefaultTrustedProxyCIDRs is the comma-separated CIDR list used when the
// operator does not set TRUSTED_PROXIES. It covers loopback addresses for
// both IPv4 and IPv6, which is the correct default for a gateway running
// behind a local reverse-proxy or sidecar on the same host.
const DefaultTrustedProxyCIDRs = "127.0.0.0/8,::1/128"

// ParseTrustedProxyCIDRs parses a comma-separated list of CIDR blocks into
// []*net.IPNet. An empty raw string causes the DefaultTrustedProxyCIDRs to
// be used. Returns an error if any CIDR block is malformed.
func ParseTrustedProxyCIDRs(raw string) ([]*net.IPNet, error) {
	if strings.TrimSpace(raw) == "" {
		raw = DefaultTrustedProxyCIDRs
	}
	parts := strings.Split(raw, ",")
	nets := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", part, err)
		}
		nets = append(nets, ipNet)
	}
	return nets, nil
}

// isTrustedProxy reports whether host (a bare IP address without port) falls
// within any of the provided CIDR ranges.
func isTrustedProxy(host string, trusted []*net.IPNet) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, network := range trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// resolveClientIP returns the best available client IP address (host only,
// no port) for the request.
//
// Security property: X-Forwarded-For and X-Real-IP are honored only when the
// direct TCP peer (r.RemoteAddr host) is within a trusted-proxy CIDR. When
// the peer is not trusted, RemoteAddr is used unconditionally — a caller
// cannot forge their source IP by supplying these headers.
//
// XFF selection strategy: when the direct peer is trusted, the leftmost
// entry in the X-Forwarded-For chain is selected. A well-behaved reverse
// proxy prepends the true client IP before appending its own address, so the
// leftmost entry represents the original caller. X-Real-IP is used as a
// fallback when no XFF header is present but the peer is trusted.
func resolveClientIP(r *http.Request, trusted []*net.IPNet) string {
	// Extract host from "host:port" remote address; tolerate a plain IP.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr may already be a bare IP (e.g. set by a previous middleware).
		host = strings.TrimSpace(r.RemoteAddr)
	}

	if !isTrustedProxy(host, trusted) {
		// Direct peer is not in the trusted list; ignore all forwarded headers.
		return host
	}

	// Direct peer is trusted; honor X-Forwarded-For (leftmost entry).
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// XFF may be a comma-separated list; take only the first token.
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			xff = xff[:idx]
		}
		client := strings.TrimSpace(xff)
		if net.ParseIP(client) != nil {
			return client
		}
	}

	// Fall back to X-Real-IP.
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if ip := net.ParseIP(strings.TrimSpace(xri)); ip != nil {
			return ip.String()
		}
	}

	// No valid forwarded header; return the direct peer.
	return host
}

// RealIPMiddleware returns HTTP middleware that rewrites r.RemoteAddr to the
// resolved client IP host (no port). The IP is derived from X-Forwarded-For
// or X-Real-IP only when the direct TCP peer falls within a trusted-proxy
// CIDR; otherwise r.RemoteAddr (host portion) is used unchanged.
//
// This middleware replaces the deprecated chi middleware.RealIP. It must be
// placed early in the stack, before any middleware that keys on r.RemoteAddr
// (e.g. per-IP rate limiting, request logging).
func RealIPMiddleware(trusted []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.RemoteAddr = resolveClientIP(r, trusted)
			next.ServeHTTP(w, r)
		})
	}
}
