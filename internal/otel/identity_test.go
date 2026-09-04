package otel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/observability"
)

func identityThroughMiddleware(t *testing.T, set func(h http.Header)) observability.RequestIdentity {
	t.Helper()
	var seen observability.RequestIdentity
	h := Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = observability.RequestIdentityFromContext(r.Context())
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions", nil)
	set(req.Header)
	h.ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

func TestMiddleware_IdentityFromHeaders(t *testing.T) {
	got := identityThroughMiddleware(t, func(h http.Header) {
		h.Set("X-User-ID", " user-42 ")
		h.Set("X-Session-ID", "sess-7")
	})
	if got.User != "user-42" || got.SessionID != "sess-7" {
		t.Fatalf("identity = %+v, want user-42 / sess-7 (trimmed)", got)
	}
	if got.Metadata != nil {
		t.Fatalf("metadata = %v, want nil: the HTTP layer reads no metadata", got.Metadata)
	}
}

func TestMiddleware_IdentityFromBaggageWithoutPropagatorInstalled(t *testing.T) {
	// No installPropagator() here on purpose: baggage must be read whether or
	// not tracing is on, since the identity feeds the request log too.
	got := identityThroughMiddleware(t, func(h http.Header) {
		h.Set("baggage", "user.id=user%2042,session.id=sess-7,other=x")
	})
	if got.User != "user 42" || got.SessionID != "sess-7" {
		t.Fatalf("identity = %+v, want percent-decoded user / sess-7 from baggage", got)
	}
}

func TestMiddleware_HeaderOutranksBaggage(t *testing.T) {
	got := identityThroughMiddleware(t, func(h http.Header) {
		h.Set("X-User-ID", "from-header")
		h.Set("baggage", "user.id=from-baggage,session.id=sess-baggage")
	})
	if got.User != "from-header" {
		t.Errorf("user = %q, want the header value", got.User)
	}
	if got.SessionID != "sess-baggage" {
		t.Errorf("session = %q, want the baggage value when the header is absent", got.SessionID)
	}
}

func TestMiddleware_IdentityRejectsUnusableValues(t *testing.T) {
	cases := map[string]string{
		"too long":          strings.Repeat("a", maxIdentityValueLen+1),
		"control character": "user\x00id",
		"blank":             "   ",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			got := identityThroughMiddleware(t, func(h http.Header) { h.Set("X-User-ID", value) })
			if !got.IsZero() {
				t.Fatalf("identity = %+v, want zero for an unusable header value", got)
			}
		})
	}
}

func TestMiddleware_NoIdentityInputsLeavesContextZero(t *testing.T) {
	got := identityThroughMiddleware(t, func(http.Header) {})
	if !got.IsZero() {
		t.Fatalf("identity = %+v, want zero", got)
	}
}
