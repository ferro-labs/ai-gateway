package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
)

type nonProxyProvider struct {
	name   string
	models []string
	resp   *providers.Response
	calls  int
}

func (m *nonProxyProvider) Name() string                  { return m.name }
func (m *nonProxyProvider) SupportedModels() []string     { return m.models }
func (m *nonProxyProvider) Models() []providers.ModelInfo { return nil }
func (m *nonProxyProvider) SupportsModel(model string) bool {
	for _, mm := range m.models {
		if mm == model {
			return true
		}
	}
	return false
}
func (m *nonProxyProvider) Complete(_ context.Context, _ providers.Request) (*providers.Response, error) {
	m.calls++
	if m.resp != nil {
		return m.resp, nil
	}
	return &providers.Response{
		ID:    "np-1",
		Model: "non-proxy-model",
		Choices: []providers.Choice{{
			Index:        0,
			Message:      providers.Message{Role: providers.RoleAssistant, Content: "ok"},
			FinishReason: "stop",
		}},
	}, nil
}

func TestCompletionsEndpointURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
		wantErr bool
	}{
		{name: "root base URL", baseURL: "https://api.openai.com", want: "https://api.openai.com/v1/completions"},
		{name: "already has v1", baseURL: "https://api.example.com/v1", want: "https://api.example.com/v1/completions"},
		{name: "already has v1 trailing slash", baseURL: "https://api.example.com/v1/", want: "https://api.example.com/v1/completions"},
		{name: "invalid", baseURL: "not a url", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompletionsEndpointURL(tt.baseURL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for base URL %q", tt.baseURL)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCompletionsHandler_ProxyPath_DoesNotDuplicateV1(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"text_completion","model":"gpt-4o","choices":[{"text":"ok","index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	reg := providers.NewRegistry()
	op, err := openaipkg.New("sk-test", upstream.URL+"/v1")
	if err != nil {
		t.Fatalf("failed to build openai provider: %v", err)
	}
	reg.Register(op)

	h := Completions(reg)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"gpt-4o","prompt":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotPath != "/v1/completions" {
		t.Fatalf("expected upstream path /v1/completions, got %q", gotPath)
	}
}

func TestCompletionsHandler_ShimsStreamRequest_ReturnsExplicitError(t *testing.T) {
	np := &nonProxyProvider{name: "non-proxy", models: []string{"non-proxy-model"}}

	reg := providers.NewRegistry()
	reg.Register(np)

	h := Completions(reg)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"non-proxy-model","prompt":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if payload.Error.Code != "streaming_not_supported" {
		t.Fatalf("expected error code streaming_not_supported, got %q", payload.Error.Code)
	}
	if np.calls != 0 {
		t.Fatalf("provider should not be called for unsupported stream shim, got %d calls", np.calls)
	}
}

func TestCompletionsHandler_ProxyStreamPathUsesWriteDeadlines(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chunk-1\"}\n\n"))
	}))
	defer upstream.Close()

	reg := providers.NewRegistry()
	op, err := openaipkg.New("sk-test", upstream.URL)
	if err != nil {
		t.Fatalf("failed to build openai provider: %v", err)
	}
	reg.Register(op)

	h := Completions(reg)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"gpt-4o","prompt":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := newCompletionDeadlineRecorder()

	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "data:") {
		t.Fatalf("expected streamed body, got %q", w.Body.String())
	}
	if len(w.deadlines) < 2 {
		t.Fatalf("expected write deadline set and clear, got %d entries", len(w.deadlines))
	}
	if w.deadlines[0].IsZero() {
		t.Fatal("first deadline should set a timeout")
	}
	if !w.deadlines[len(w.deadlines)-1].IsZero() {
		t.Fatalf("last deadline should clear timeout, got %v", w.deadlines[len(w.deadlines)-1])
	}
	if w.flushes == 0 {
		t.Fatal("expected streaming response flush")
	}
}

type completionDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	flushes   int
}

func newCompletionDeadlineRecorder() *completionDeadlineRecorder {
	return &completionDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *completionDeadlineRecorder) Flush() {
	r.flushes++
	r.ResponseRecorder.Flush()
}

func (r *completionDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}
