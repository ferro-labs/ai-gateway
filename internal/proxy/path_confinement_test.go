package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
)

type pathConfinementProvider struct {
	baseURL string
}

func (pathConfinementProvider) Name() string              { return "path-confinement" }
func (pathConfinementProvider) SupportsModel(string) bool { return true }
func (pathConfinementProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}
func (p pathConfinementProvider) BaseURL() string { return p.baseURL }
func (pathConfinementProvider) AuthHeaders() map[string]string {
	return map[string]string{"X-Provider-Key": "1"}
}

func TestPassThroughPathConfinementPreservesSafeEscapingAndQuery(t *testing.T) {
	observed := make(chan *url.URL, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured := *r.URL
		observed <- &captured
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	reg := providers.NewRegistry()
	reg.Register(pathConfinementProvider{baseURL: upstream.URL + "/custom/api"})

	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/v1/models/file..name%3Av1;revision=1?after=a%2Fb&note=..%2Fsafe",
		nil,
	)
	req.Header.Set("X-Provider", "path-confinement")
	rec := httptest.NewRecorder()
	Handler(reg)(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	got := <-observed
	if got.EscapedPath() != "/custom/api/models/file..name%3Av1;revision=1" {
		t.Errorf("escaped path = %q, want /custom/api/models/file..name%%3Av1;revision=1", got.EscapedPath())
	}
	if got.RawQuery != "after=a%2Fb&note=..%2Fsafe" {
		t.Errorf("raw query = %q, want original encoding", got.RawQuery)
	}
}

func TestPassThroughPathConfinementAllowsLiteralPercent(t *testing.T) {
	observed := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	reg := providers.NewRegistry()
	reg.Register(pathConfinementProvider{baseURL: upstream.URL + "/api/v1"})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/files/100%25-ready", nil)
	req.Header.Set("X-Provider", "path-confinement")
	rec := httptest.NewRecorder()
	Handler(reg)(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := <-observed; got != "/api/v1/files/100%25-ready" {
		t.Errorf("escaped path = %q, want /api/v1/files/100%%25-ready", got)
	}
}

// TestPassThroughRejectsDotSegmentsBeforeCredentialInjection is the outside-in
// SEC-009 reproduction. Every case must stop before ReverseProxy.Rewrite can
// attach the provider credential or contact the configured upstream.
func TestPassThroughRejectsDotSegmentsBeforeCredentialInjection(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "decoded-parent", path: "/v1/../control-plane/secrets"},
		{name: "encoded-parent", path: "/v1/%2e%2e/control-plane/secrets"},
		{name: "encoded-separators", path: "/v1/safe%2f..%2fcontrol-plane/secrets"},
		{name: "backslash-separator", path: "/v1/..%5ccontrol-plane/secrets"},
		{name: "double-encoded-parent", path: "/v1/%252e%252e/control-plane/secrets"},
		{name: "matrix-param-parent", path: "/v1/..;revision=1/control-plane/secrets"},
		{name: "encoded-matrix-param-parent", path: "/v1/%2e%2e%3brevision=1/control-plane/secrets"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("X-Provider-Key") != "" {
					t.Error("provider credential reached upstream")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(upstream.Close)

			reg := providers.NewRegistry()
			reg.Register(pathConfinementProvider{baseURL: upstream.URL + "/api/v1"})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tt.path, nil)
			req.Header.Set("X-Provider", "path-confinement")
			rec := httptest.NewRecorder()
			Handler(reg)(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("upstream calls = %d, want 0", got)
			}

			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Error.Code != "invalid_proxy_path" {
				t.Errorf("error code = %q, want invalid_proxy_path", body.Error.Code)
			}
		})
	}
}
