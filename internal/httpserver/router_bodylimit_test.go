package httpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/providers"
)

// stubProvider is a minimal provider stub that satisfies providers.Provider.
type stubProvider struct{}

func (stubProvider) Name() string              { return "stub" }
func (stubProvider) SupportedModels() []string { return []string{"stub-model"} }
func (stubProvider) SupportsModel(m string) bool {
	return m == "stub-model"
}
func (stubProvider) Models() []providers.ModelInfo {
	return []providers.ModelInfo{{ID: "stub-model", Object: "model", OwnedBy: "stub"}}
}
func (stubProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	return &providers.Response{
		ID:    "stub-1",
		Model: "stub-model",
		Choices: []providers.Choice{{
			Message:      providers.Message{Role: "assistant", Content: "ok"},
			FinishReason: "stop",
		}},
	}, nil
}

// buildTestRouter creates a router wired with the given gateway and a stub registry.
// It always enables unauthenticated proxy access (test only).
func buildTestRouter(t *testing.T, gw *aigateway.Gateway) http.Handler {
	t.Helper()
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")

	reg := providers.NewRegistry()
	reg.Register(stubProvider{})

	ks := admin.NewKeyStore()
	return httpserver.NewRouter(reg, ks, nil, gw, nil, nil, nil, nil, "", nil)
}

// TestBodySizeLimit_TooLarge_Returns413 verifies that a POST body exceeding the configured
// MaxRequestBytes limit results in HTTP 413 Request Entity Too Large.
func TestBodySizeLimit_TooLarge_Returns413(t *testing.T) {
	const smallLimit = 64 // bytes — well below a real chat request

	gw, err := aigateway.New(aigateway.Config{
		MaxRequestBytes: smallLimit,
		Strategy:        aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:         []aigateway.Target{{VirtualKey: "stub"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}

	r := buildTestRouter(t, gw)

	// Build a body that is definitely larger than the 64-byte limit.
	body := `{"model":"stub-model","messages":[{"role":"user","content":"` + strings.Repeat("x", 200) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected HTTP 413, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// TestBodySizeLimit_UnderLimit_NotRejected verifies that a small valid body is not
// rejected by the size limit middleware (it may fail for other reasons such as
// no matching provider, but must not be a 413).
func TestBodySizeLimit_UnderLimit_NotRejected(t *testing.T) {
	const largeLimit = 10 * 1024 * 1024 // 10 MiB default

	gw, err := aigateway.New(aigateway.Config{
		MaxRequestBytes: largeLimit,
		Strategy:        aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:         []aigateway.Target{{VirtualKey: "stub"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}

	r := buildTestRouter(t, gw)

	// A minimal valid-looking chat body (under the 10 MiB limit).
	body := `{"model":"stub-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code == http.StatusRequestEntityTooLarge {
		t.Errorf("small body should not produce 413, got %d", w.Code)
	}
}
