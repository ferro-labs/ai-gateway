package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// batchStub is a provider that supports batch pass-through to a configured
// upstream base URL with a fixed credential.
type batchStub struct {
	name    string
	baseURL string
}

func (b *batchStub) Name() string                                                   { return b.name }
func (b *batchStub) SupportsModel(string) bool                                      { return true }
func (b *batchStub) Complete(context.Context, core.Request) (*core.Response, error) { return nil, nil }
func (b *batchStub) BatchBaseURL() string                                           { return b.baseURL }
func (b *batchStub) BatchAuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer upstream-secret"}
}

// plainStub supports no batch pass-through (not a BatchProvider).
type plainStub struct{ name string }

func (p *plainStub) Name() string              { return p.name }
func (p *plainStub) SupportsModel(string) bool { return true }
func (p *plainStub) Complete(context.Context, core.Request) (*core.Response, error) {
	return nil, nil
}

// batchSourceStub resolves a fixed batch target to a fixed provider.
type batchSourceStub struct {
	target   string
	provider providers.Provider
}

func (s *batchSourceStub) BatchTarget() string { return s.target }
func (s *batchSourceStub) Get(name string) (providers.Provider, bool) {
	if s.provider != nil && name == s.provider.Name() {
		return s.provider, true
	}
	return nil, false
}

func TestBatchHandler_ForwardsToTargetWithProviderAuth(t *testing.T) {
	var gotPath, gotMethod, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"file-abc","object":"file"}`))
	}))
	defer upstream.Close()

	src := &batchSourceStub{target: "openai", provider: &batchStub{name: "openai", baseURL: upstream.URL}}
	h := BatchHandler(src)

	// A client bearer token must be replaced by the provider credential.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/files" {
		t.Errorf("upstream path = %q, want /files (gateway /v1 stripped)", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("upstream method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer upstream-secret" {
		t.Errorf("upstream Authorization = %q, want the provider credential (client token must be swapped out)", gotAuth)
	}
	if rec.Header().Get("X-Gateway-Provider") != "openai" {
		t.Errorf("X-Gateway-Provider = %q, want openai", rec.Header().Get("X-Gateway-Provider"))
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != `{"id":"file-abc","object":"file"}` {
		t.Errorf("body = %q, want the upstream File object verbatim", body)
	}
}

// A bare id subroute (GET /v1/batches/{id}) forwards to the same backend with
// the full path preserved beneath the root — the whole point of single-target.
func TestBatchHandler_ForwardsBareIdSubroute(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"batch_1","object":"batch","status":"completed"}`))
	}))
	defer upstream.Close()

	src := &batchSourceStub{target: "openai", provider: &batchStub{name: "openai", baseURL: upstream.URL}}
	h := BatchHandler(src)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/batches/batch_1", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPath != "/batches/batch_1" {
		t.Errorf("upstream path = %q, want /batches/batch_1", gotPath)
	}
}

func TestBatchHandler_NotConfigured_Returns501(t *testing.T) {
	h := BatchHandler(&batchSourceStub{target: ""})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when batch_target is unset", rec.Code)
	}
}

func TestBatchHandler_ProviderNotBatchCapable_Returns501(t *testing.T) {
	src := &batchSourceStub{target: "plain", provider: &plainStub{name: "plain"}}
	h := BatchHandler(src)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/batches", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when the target provider is not batch-capable", rec.Code)
	}
}

// A traversal segment must be refused before any credential is installed.
func TestBatchHandler_RejectsTraversalPath(t *testing.T) {
	src := &batchSourceStub{target: "openai", provider: &batchStub{name: "openai", baseURL: "http://unused"}}
	h := BatchHandler(src)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/files/..%2f..%2fadmin", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a traversal path", rec.Code)
	}
}
