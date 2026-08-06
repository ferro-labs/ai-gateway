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
// XFF selection strategy: when the direct peer is trusted, the chain is
// walked right-to-left and the first entry that parses as an IP outside every
// trusted-proxy CIDR is selected. Proxies append, so the rightmost entry is
// the one written by the hop closest to us and each step leftwards is one
// step further from anything we can vouch for — the leftmost entry is
// whatever the original caller chose to send. Walking from the right
// therefore stops at the address a trusted proxy actually observed; walking
// from the left would return a value the caller supplied in its own
// X-Forwarded-For header.
//
// X-Real-IP holds a single address with no chain to corroborate it, so it is
// consulted only after the XFF walk yields no untrusted hop, and — like XFF —
// only when the direct peer is trusted.
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

	// Direct peer is trusted; honor X-Forwarded-For, reading the chain from
	// the right so the caller cannot pick the answer by prepending entries.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		hops := strings.Split(xff, ",")
		for i := len(hops) - 1; i >= 0; i-- {
			hop := strings.TrimSpace(hops[i])
			// A hop that is unparseable, or that is one of our own proxies,
			// tells us nothing about the caller; keep walking outward.
			if net.ParseIP(hop) == nil || isTrustedProxy(hop, trusted) {
				continue
			}
			return hop
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
