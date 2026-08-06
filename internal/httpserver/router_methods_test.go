package httpserver_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
)

// TestNativeRoutes_WrongMethod_Returns405 pins the boundary between the routes
// the gateway serves itself and the /v1/* pass-through.
//
// The natively-handled routes are registered per-method. A request using any
// other method does not match them, and without an explicit refusal it falls
// through to the /v1/* catch-all and is forwarded upstream with the operator's
// credential — past every guardrail, budget, circuit breaker, concurrency limit
// and the targets allowlist. A wrong method on a route the gateway owns is a
// client mistake, not a pass-through candidate.
func TestNativeRoutes_WrongMethod_Returns405(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "stub"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	r := buildTestRouter(t, gw)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"delete on chat completions", http.MethodDelete, "/v1/chat/completions"},
		// HEAD is served wherever GET is, and nowhere else: a POST route has no
		// body to omit, so it refuses HEAD like any other wrong method.
		{"head on chat completions", http.MethodHead, "/v1/chat/completions"},
		{"put on chat completions", http.MethodPut, "/v1/chat/completions"},
		{"patch on chat completions", http.MethodPatch, "/v1/chat/completions"},
		{"get on chat completions", http.MethodGet, "/v1/chat/completions"},
		{"delete on completions", http.MethodDelete, "/v1/completions"},
		{"get on embeddings", http.MethodGet, "/v1/embeddings"},
		{"delete on embeddings", http.MethodDelete, "/v1/embeddings"},
		{"get on image generations", http.MethodGet, "/v1/images/generations"},
		{"post on models", http.MethodPost, "/v1/models"},
		{"delete on models", http.MethodDelete, "/v1/models"},
		{"post on capabilities", http.MethodPost, "/v1/capabilities"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: got %d, want 405 — the request was not refused and may have been proxied upstream (body: %s)",
					tt.method, tt.path, w.Code, w.Body.String())
			}
			if allow := w.Header().Get("Allow"); allow == "" {
				t.Errorf("%s %s: 405 must carry an Allow header naming the accepted method", tt.method, tt.path)
			}
		})
	}
}

// TestNativeRoutes_CorrectMethod_NotRefused guards the fix from over-reaching:
// the method each route actually serves must still reach its handler.
func TestNativeRoutes_CorrectMethod_NotRefused(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "stub"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	r := buildTestRouter(t, gw)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"post chat completions", http.MethodPost, "/v1/chat/completions", `{"model":"stub-model","messages":[{"role":"user","content":"hi"}]}`},
		{"post embeddings", http.MethodPost, "/v1/embeddings", `{"model":"stub-model","input":"hi"}`},
		{"get models", http.MethodGet, "/v1/models", ""},
		{"get capabilities", http.MethodGet, "/v1/capabilities", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			if w.Code == http.StatusMethodNotAllowed {
				t.Errorf("%s %s: the route's own method must not be refused (body: %s)", tt.method, tt.path, w.Body.String())
			}
		})
	}
}

// TestGetRoutes_ServeHead is the RFC 9110 §9.3.2 contract: HEAD is GET with the
// response body omitted, so every route that answers GET answers HEAD with the
// same status. HEAD is what load-balancer probes and cache revalidation send,
// and answering 405 takes an instance out of rotation.
//
// It runs against a real server rather than httptest.NewRecorder because the
// body suppression is net/http's, not the handler's: a recorder captures what
// the handler wrote, a server discards it. Only the server can show that a HEAD
// response carries no body.
func TestGetRoutes_ServeHead(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "stub"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	srv := httptest.NewServer(buildTestRouter(t, gw))
	defer srv.Close()

	// The probes are registered with chi's per-method Get; the /v1 routes are
	// registered for every method and narrowed by onlyMethod. Both shapes have
	// to serve HEAD, and they reach it by different code paths.
	paths := []string{"/livez", "/readyz", "/health", "/v1/models", "/v1/capabilities"}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			getReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
			if err != nil {
				t.Fatalf("build GET %s: %v", path, err)
			}
			get, err := srv.Client().Do(getReq)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer get.Body.Close() //nolint:errcheck // test client response
			if _, err := io.Copy(io.Discard, get.Body); err != nil {
				t.Fatalf("drain GET %s: %v", path, err)
			}

			headReq, err := http.NewRequestWithContext(t.Context(), http.MethodHead, srv.URL+path, nil)
			if err != nil {
				t.Fatalf("build HEAD %s: %v", path, err)
			}
			head, err := srv.Client().Do(headReq)
			if err != nil {
				t.Fatalf("HEAD %s: %v", path, err)
			}
			defer head.Body.Close() //nolint:errcheck // test client response
			body, err := io.ReadAll(head.Body)
			if err != nil {
				t.Fatalf("read HEAD %s: %v", path, err)
			}

			if head.StatusCode != get.StatusCode {
				t.Errorf("HEAD %s: status %d, want %d — the status GET answers with",
					path, head.StatusCode, get.StatusCode)
			}
			if len(body) != 0 {
				t.Errorf("HEAD %s: response carried %d bytes of body, want none", path, len(body))
			}
		})
	}
}

// TestUnknownMethod_IsNotImplemented pins the answer for a method no route
// could match. chi refuses it before any route lookup and answered 405 with no
// Allow header, which RFC 9110 §15.5.6 forbids — a 405 must name the target
// resource's supported methods, and nothing here matched a resource. RFC 9110
// §15.6.2 makes 501 the status for a method the server does not recognize and
// cannot support for any resource.
func TestUnknownMethod_IsNotImplemented(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "stub"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	r := buildTestRouter(t, gw)

	for _, path := range []string{"/v1/models", "/v1/chat/completions", "/livez", "/nope"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), "FOOBAR", path, nil)
			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			if w.Code != http.StatusNotImplemented {
				t.Errorf("FOOBAR %s: status %d, want 501 (body: %s)", path, w.Code, w.Body.String())
			}
			if w.Code == http.StatusMethodNotAllowed && w.Header().Get("Allow") == "" {
				t.Errorf("FOOBAR %s: a 405 without an Allow header violates RFC 9110 §15.5.6", path)
			}
		})
	}
}

// TestProxyPassThrough_StillReachesUnhandledPaths guards the other direction:
// refusing wrong methods on native routes must not disturb the pass-through for
// paths the gateway does not serve itself.
func TestProxyPassThrough_StillReachesUnhandledPaths(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "stub"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	r := buildTestRouter(t, gw)

	// /v1/fine_tuning/jobs is not natively handled, so it belongs to the
	// pass-through and must not be answered with 405 by the method guard. (/v1/files
	// is no longer a valid example — it is now the batch surface.)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/v1/fine_tuning/jobs/ft-123", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code == http.StatusMethodNotAllowed {
		t.Errorf("an unhandled /v1/* path must still reach the pass-through, got 405 (body: %s)", w.Body.String())
	}
}
