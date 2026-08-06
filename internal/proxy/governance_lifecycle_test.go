package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/requestlog"
)

// recordingLogStore captures the rows the request-logger plugin writes, so a
// test can assert a pass-through request produced one.
type recordingLogStore struct {
	mu      sync.Mutex
	entries []requestlog.Entry
}

func (s *recordingLogStore) Write(_ context.Context, e requestlog.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return nil
}

func (s *recordingLogStore) all() []requestlog.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]requestlog.Entry(nil), s.entries...)
}

// requestLoggerConfig registers the request logger at the stages it branches on.
// One entry per stage, carrying identical config so both resolve to the same
// instance — see validateMultiStagePlugins.
func requestLoggerConfig() []config.PluginConfig {
	cfg := map[string]any{"persist": true}
	stages := []string{"before_request", "after_request", "on_error"}
	entries := make([]config.PluginConfig, 0, len(stages))
	for _, stage := range stages {
		entries = append(entries, config.PluginConfig{
			Name:    "request-logger",
			Type:    "logging",
			Stage:   stage,
			Enabled: true,
			Config:  cfg,
		})
	}
	return entries
}

// CORE-002: a pass-through request appears in the request log. Before this the
// rows were simply absent — every /v1/* endpoint but the six handled natively
// consumed provider quota under the gateway's credential and left no record.
func TestPassthrough_IsRecordedInTheRequestLog(t *testing.T) {
	up := newCountingUpstream(t)
	store := &recordingLogStore{}

	t.Setenv("FERRO_MODEL_CATALOG_TIMEOUT", "0")
	cfg := config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "stub", Models: []string{"stub-model"}}},
		Plugins:  requestLoggerConfig(),
	}
	gw, err := aigateway.New(cfg)
	if err != nil {
		t.Fatalf("aigateway.New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close() })
	gw.RegisterProvider(&proxiableStub{name: "stub", baseURL: up.URL, models: []string{"stub-model"}})
	gw.SetRequestLogWriter(store)
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}

	w := passthroughPOST(t, gw, "/v1/responses", `{"model":"stub-model","input":"hello"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	entries := store.all()
	if len(entries) == 0 {
		t.Fatal("request log has no rows for a pass-through request")
	}
	var sawProvider bool
	for _, e := range entries {
		if e.Provider == "stub" {
			sawProvider = true
			// An opaque response cannot be priced, and unpriced is a different
			// claim from free: nil means "unknown", 0 would mean "cost nothing".
			if e.CostUSD != nil {
				t.Errorf("CostUSD = %v, want nil (a pass-through response is unpriced, not free)", *e.CostUSD)
			}
		}
	}
	if !sawProvider {
		t.Errorf("no row names the target that served the request; rows: %+v", entries)
	}
}

// A body no content guardrail can read is REFUSED when a guardrail is
// configured. A guardrail with nothing to match on approves, and that approval
// is indistinguishable from one it actually made.
func TestPassthrough_UninspectableBodyIsRefusedWhenGuardrailConfigured(t *testing.T) {
	up := newCountingUpstream(t)
	gw := newGovernedGateway(t, up.URL, wordFilterConfig("forbidden"))

	// A multipart upload, the shape /v1/files carries. Not JSON, so there is no
	// projection a guardrail could screen.
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files",
		strings.NewReader("--b\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nforbidden\r\n--b--\r\n"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	r.Header.Set("X-Provider", "stub")
	w := httptest.NewRecorder()
	Handler(gw)(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if hits := up.hits.Load(); hits != 0 {
		t.Errorf("upstream received %d requests, want 0 — an uninspectable body was forwarded past a configured guardrail", hits)
	}
}

// The same body is SERVED when no content guardrail is configured. Fail-closed
// is a consequence of the operator's policy, not a new restriction on everyone:
// a deployment that screens nothing keeps forwarding uploads exactly as before.
func TestPassthrough_UninspectableBodyIsServedWithoutGuardrail(t *testing.T) {
	up := newCountingUpstream(t)
	gw := newGovernedGateway(t, up.URL)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files",
		strings.NewReader("--b\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nanything\r\n--b--\r\n"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	r.Header.Set("X-Provider", "stub")
	w := httptest.NewRecorder()
	Handler(gw)(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if hits := up.hits.Load(); hits != 1 {
		t.Errorf("upstream received %d requests, want 1", hits)
	}
}

// A non-guardrail plugin does not turn the refusal on. Only a plugin that
// reports TypeGuardrail screens content, and a logger that cannot read a body
// has not failed to do its job.
func TestPassthrough_UninspectableBodyIsServedUnderNonGuardrailPlugins(t *testing.T) {
	up := newCountingUpstream(t)
	gw := newGovernedGateway(t, up.URL, requestLoggerConfig()...)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files", strings.NewReader("raw bytes, not json"))
	r.Header.Set("Content-Type", "application/octet-stream")
	r.Header.Set("X-Provider", "stub")
	w := httptest.NewRecorder()
	Handler(gw)(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if hits := up.hits.Load(); hits != 1 {
		t.Errorf("upstream received %d requests, want 1", hits)
	}
}

// CORE-007: a guardrail that never reads request content does not turn the
// refusal on. `max-token` counts tokens and messages; an unreadable body denied
// it nothing, so refusing was pure over-refusal on every deployment that runs
// it without a content guardrail alongside.
//
// Paired with TestPassthrough_UninspectableBodyIsRefusedWhenGuardrailConfigured
// above, which holds the other half: word-filter DOES read content, and the
// same body is still refused for it.
func TestPassthrough_UninspectableBodyIsServedUnderContentAgnosticGuardrail(t *testing.T) {
	up := newCountingUpstream(t)
	gw := newGovernedGateway(t, up.URL, maxTokenConfig(0))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files",
		strings.NewReader("--b\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nanything\r\n--b--\r\n"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	r.Header.Set("X-Provider", "stub")
	w := httptest.NewRecorder()
	Handler(gw)(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if hits := up.hits.Load(); hits != 1 {
		t.Errorf("upstream received %d requests, want 1 — max-token inspects no content, so it had nothing to be denied", hits)
	}
}

// The same guardrail DOES read content once max_input_length is configured: it
// measures the projection, and an unreadable body satisfies a length cap at
// zero. Narrowing the refusal must not open that.
func TestPassthrough_UninspectableBodyIsRefusedUnderMaxInputLength(t *testing.T) {
	up := newCountingUpstream(t)
	gw := newGovernedGateway(t, up.URL, maxTokenConfig(16))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/files",
		strings.NewReader("--b\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\na body far longer than the configured cap\r\n--b--\r\n"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	r.Header.Set("X-Provider", "stub")
	w := httptest.NewRecorder()
	Handler(gw)(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if hits := up.hits.Load(); hits != 0 {
		t.Errorf("upstream received %d requests, want 0 — an unreadable body would satisfy max_input_length at length zero", hits)
	}
}

// A body carrying no content at all — a GET or a DELETE — is not
// "uninspectable": there is nothing a guardrail was prevented from seeing.
func TestPassthrough_EmptyBodyIsNotUninspectable(t *testing.T) {
	up := newCountingUpstream(t)
	gw := newGovernedGateway(t, up.URL, wordFilterConfig("forbidden"))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/files", nil)
	r.Header.Set("X-Provider", "stub")
	w := httptest.NewRecorder()
	Handler(gw)(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if hits := up.hits.Load(); hits != 1 {
		t.Errorf("upstream received %d requests, want 1", hits)
	}
}

// The guardrail screens content at any depth, which is what a projection of
// string VALUES buys: /v1/responses nests its prompt inside an array of typed
// content parts, and a top-level-only scan would miss every one of them.
func TestPassthrough_GuardrailScreensNestedContent(t *testing.T) {
	up := newCountingUpstream(t)
	gw := newGovernedGateway(t, up.URL, wordFilterConfig("forbidden"))

	body := `{"model":"stub-model","input":[{"role":"user","content":[{"type":"input_text","text":"the forbidden thing"}]}]}`
	w := passthroughPOST(t, gw, "/v1/responses", body, nil)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if hits := up.hits.Load(); hits != 0 {
		t.Errorf("upstream received %d requests, want 0", hits)
	}
}

// Governance must not consume the body it screens: whatever the guardrail
// approved has to reach the upstream byte for byte.
func TestPassthrough_ForwardedBodySurvivesProjection(t *testing.T) {
	up := newCountingUpstream(t)
	gw := newGovernedGateway(t, up.URL, wordFilterConfig("forbidden"))

	body := `{"model":"stub-model","input":"an ordinary prompt"}`
	if w := passthroughPOST(t, gw, "/v1/responses", body, nil); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	got, _ := up.lastBody.Load().(string)
	if got != body {
		t.Errorf("upstream received %q, want %q", got, body)
	}
}
