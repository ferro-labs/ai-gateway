package httpserver_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/internal/middleware"
	"github.com/ferro-labs/ai-gateway/internal/ratelimit"
)

// applyRealIP wraps a capture handler with the RealIPMiddleware using the
// given CIDR list (empty string → defaults), executes req against it, and
// returns the resolved r.RemoteAddr that the downstream handler observed.
func applyRealIP(t *testing.T, trustedCIDRs string, req *http.Request) string {
	t.Helper()
	nets, err := httpserver.ParseTrustedProxyCIDRs(trustedCIDRs)
	if err != nil {
		t.Fatalf("ParseTrustedProxyCIDRs(%q): %v", trustedCIDRs, err)
	}
	var got string
	capture := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.RemoteAddr
	})
	httpserver.RealIPMiddleware(nets)(capture).ServeHTTP(httptest.NewRecorder(), req)
	return got
}

// ---------------------------------------------------------------------------
// (a) Forged XFF from a NON-trusted direct peer must be ignored.
// ---------------------------------------------------------------------------

// TestRealIP_UntrustedPeer_IgnoresXFF verifies that when the direct TCP peer
// is not within any trusted CIDR, X-Forwarded-For is silently discarded and
// the raw RemoteAddr host is used as the resolved client IP.
func TestRealIP_UntrustedPeer_IgnoresXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.1:12345" // public IP, not in loopback CIDR
	req.Header.Set("X-Forwarded-For", "10.0.0.1")

	resolved := applyRealIP(t, "", req) // "" → default loopback only

	if resolved != "203.0.113.1" {
		t.Errorf("untrusted peer: expected RemoteAddr host 203.0.113.1, got %q", resolved)
	}
}

func TestRealIP_UntrustedPeer_IgnoresXRealIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.5:9999"
	req.Header.Set("X-Real-IP", "10.0.0.99")

	resolved := applyRealIP(t, "", req)

	if resolved != "198.51.100.5" {
		t.Errorf("untrusted peer: expected 198.51.100.5, got %q", resolved)
	}
}

// ---------------------------------------------------------------------------
// (b) XFF from a trusted-proxy peer is honored.
// ---------------------------------------------------------------------------

// TestRealIP_TrustedPeer_HonorsXFF verifies that when the direct TCP peer is
// within a trusted CIDR, the leftmost X-Forwarded-For entry is used as the
// resolved client IP.
func TestRealIP_TrustedPeer_HonorsXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:56789"                           // trusted loopback peer
	req.Header.Set("X-Forwarded-For", "203.0.113.42, 127.0.0.1") // leftmost = real client

	resolved := applyRealIP(t, "", req)

	if resolved != "203.0.113.42" {
		t.Errorf("trusted peer: expected leftmost XFF 203.0.113.42, got %q", resolved)
	}
}

func TestRealIP_TrustedPeer_HonorsXRealIP_WhenNoXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Real-IP", "198.51.100.7")

	resolved := applyRealIP(t, "", req)

	if resolved != "198.51.100.7" {
		t.Errorf("trusted peer: expected X-Real-IP 198.51.100.7, got %q", resolved)
	}
}

func TestRealIP_TrustedPeer_IPv6Loopback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "[::1]:44444"
	req.Header.Set("X-Forwarded-For", "2001:db8::1")

	resolved := applyRealIP(t, "", req) // default includes ::1/128

	if resolved != "2001:db8::1" {
		t.Errorf("trusted IPv6 peer: expected 2001:db8::1, got %q", resolved)
	}
}

func TestRealIP_CustomTrustedCIDR_HonorsXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.2.3:4567" // inside custom 10.0.0.0/8 range
	req.Header.Set("X-Forwarded-For", "203.0.113.99")

	resolved := applyRealIP(t, "127.0.0.0/8,::1/128,10.0.0.0/8", req)

	if resolved != "203.0.113.99" {
		t.Errorf("custom CIDR: expected 203.0.113.99, got %q", resolved)
	}
}

func TestRealIP_TrustedPeer_MalformedXFF_FallsBackToPeer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	req.Header.Set("X-Forwarded-For", "not-an-ip")

	resolved := applyRealIP(t, "", req)

	// Malformed XFF should not be used; fall back to the trusted peer itself.
	if resolved != "127.0.0.1" {
		t.Errorf("malformed XFF: expected fallback 127.0.0.1, got %q", resolved)
	}
}

// ---------------------------------------------------------------------------
// (c) Rate-limit bucket key uses the resolved host without the port.
// ---------------------------------------------------------------------------

// TestRateLimit_BucketKey_UsesHostOnly verifies that two requests arriving
// from the same IP address but different source ports share a single
// rate-limit bucket. This confirms the rate limiter keys on the host only
// (no port), so varying the ephemeral source port cannot evade per-IP limits.
func TestRateLimit_BucketKey_UsesHostOnly(t *testing.T) {
	// burst=1: first request exhausts the single token.
	store := ratelimit.NewStore(1, 1)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := middleware.RateLimit(store)(ok)

	r1 := httptest.NewRequest(http.MethodGet, "/", nil)
	r1.RemoteAddr = "1.2.3.4:5678"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", w1.Code)
	}

	// Different ephemeral port, same host → must hit the same bucket.
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "1.2.3.4:9999"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request (same host, different port): expected 429, got %d", w2.Code)
	}
}

// ---------------------------------------------------------------------------
// ParseTrustedProxyCIDRs unit tests
// ---------------------------------------------------------------------------

func TestParseTrustedProxyCIDRs_Empty_UsesDefaults(t *testing.T) {
	nets, err := httpserver.ParseTrustedProxyCIDRs("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nets) == 0 {
		t.Fatal("expected networks from defaults, got none")
	}
	// Verify that the loopback addresses are included.
	loopback4 := net.ParseIP("127.0.0.1")
	loopback6 := net.ParseIP("::1")
	var found4, found6 bool
	for _, n := range nets {
		if n.Contains(loopback4) {
			found4 = true
		}
		if n.Contains(loopback6) {
			found6 = true
		}
	}
	if !found4 {
		t.Error("default CIDRs should include 127.0.0.0/8 (IPv4 loopback)")
	}
	if !found6 {
		t.Error("default CIDRs should include ::1/128 (IPv6 loopback)")
	}
}

func TestParseTrustedProxyCIDRs_InvalidCIDR_ReturnsError(t *testing.T) {
	_, err := httpserver.ParseTrustedProxyCIDRs("not-a-cidr")
	if err == nil {
		t.Fatal("expected error for invalid CIDR, got nil")
	}
}

func TestParseTrustedProxyCIDRs_ValidList(t *testing.T) {
	nets, err := httpserver.ParseTrustedProxyCIDRs("10.0.0.0/8,192.168.0.0/16")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nets) != 2 {
		t.Fatalf("expected 2 networks, got %d", len(nets))
	}
}
