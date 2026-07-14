package ai21

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const (
	ai21TestContentHello = "Hello"
	ai21TestContentWorld = " world"
	ai21TestChunkID      = "chatcmpl-1"
)

func TestNewAI21(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "ai21" {
		t.Errorf("Name() = %q, want ai21", p.Name())
	}
}

func TestAI21Provider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "jamba-1.5-large" {
			found = true
		}
	}
	if !found {
		t.Error("jamba-1.5-large not found in supported models")
	}
}

func TestAI21Provider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("jamba-1.5-large") {
		t.Error("expected jamba-1.5-large to be supported")
	}
	if !p.SupportsModel("any-model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestAI21Provider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "ai21" {
			t.Errorf("ModelInfo.OwnedBy = %q, want ai21", m.OwnedBy)
		}
	}
}

func TestAI21Provider_isJambaModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"jamba-1.5-large", true},
		{"jamba-1.5-mini", true},
		{"jamba-instruct", true},
		{"j2-ultra", false},
		{"j2-mid", false},
	}
	for _, tt := range tests {
		got := IsJambaModel(tt.model)
		if got != tt.want {
			t.Errorf("isJambaModel(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestAI21Provider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestAI21Provider_CompleteStream_JambaModel(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"jamba-1.5-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"jamba-1.5-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"jamba-1.5-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"jamba-1.5-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "jamba-1.5-mini",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}

	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}
	if chunks[1].Choices[0].Delta.Content != ai21TestContentHello {
		t.Errorf("delta content = %q, want Hello", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].Delta.Content != ai21TestContentWorld {
		t.Errorf("delta content = %q, want ' world'", chunks[2].Choices[0].Delta.Content)
	}
}

func TestAI21Provider_CompleteStream_NonJambaReturnsError(t *testing.T) {
	p, _ := New("test-key", "")
	_, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "j2-ultra",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for non-Jamba model, got nil")
	}
}

func TestAI21Provider_Complete_JambaModel(t *testing.T) {
	respBody := `{"id":"chatcmpl-1","model":"jamba-1.5-mini","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "jamba-1.5-mini",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != ai21TestChunkID {
		t.Errorf("Response.ID = %q, want chatcmpl-1", resp.ID)
	}
}

func TestAI21Provider_Complete_JurassicModel(t *testing.T) {
	respBody := `{"id":"j2-1","completions":[{"data":{"text":"Hello from Jurassic!","tokens":[]},"finishReason":{"reason":"length"}}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "j2-ultra",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if resp.Choices[0].Message.Content != "Hello from Jurassic!" {
		t.Errorf("choice content = %q, want 'Hello from Jurassic!'", resp.Choices[0].Message.Content)
	}
}

// TestAI21Provider_Complete_JurassicAPIError verifies a non-2xx response from the
// native Jurassic /complete endpoint is surfaced as a core.APIError whose status
// core.ParseStatusCode can recover.
func TestAI21Provider_Complete_JurassicAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/j2-ultra/complete" {
			t.Errorf("path = %q, want /j2-ultra/complete", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"detail":"service unavailable"}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "j2-ultra",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for non-2xx Jurassic response, got nil")
	}
	if got := core.ParseStatusCode(err); got != http.StatusServiceUnavailable {
		t.Errorf("ParseStatusCode = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

// TestAI21Provider_Complete_RoutesByModelFamily verifies Complete dispatches by
// model family: Jurassic (j2-*) models hit the native /<model>/complete endpoint
// while Jamba models hit the OpenAI-compatible /chat/completions endpoint.
func TestAI21Provider_Complete_RoutesByModelFamily(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		wantPath string
		respBody string
	}{
		{
			name:     "jurassic model hits /<model>/complete",
			model:    "j2-ultra",
			wantPath: "/j2-ultra/complete",
			respBody: `{"id":"j2-1","completions":[{"data":{"text":"hi","tokens":[]},"finishReason":{"reason":"stop"}}]}`,
		},
		{
			name:     "jamba model hits /chat/completions",
			model:    "jamba-large-1.7",
			wantPath: "/chat/completions",
			respBody: `{"id":"chatcmpl-1","model":"jamba-large-1.7","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.respBody)
			}))
			defer srv.Close()

			p, _ := New("test-key", srv.URL)
			if _, err := p.Complete(context.Background(), core.Request{
				Model:    tc.model,
				Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
			}); err != nil {
				t.Fatalf("Complete() error: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
		})
	}
}

// TestAI21Provider_Complete_JambaAPIError verifies a non-2xx Jamba response is
// surfaced as a core.APIError carrying the parseable status code and message.
func TestAI21Provider_Complete_JambaAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "jamba-mini-1.7",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for non-2xx response, got nil")
	}
	if got := core.ParseStatusCode(err); got != http.StatusTooManyRequests {
		t.Errorf("ParseStatusCode = %d, want %d", got, http.StatusTooManyRequests)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error %q does not contain upstream message", err.Error())
	}
}

// TestAI21Provider_CompleteStream_JambaAPIError verifies a non-2xx streaming
// Jamba response is surfaced as a core.APIError before any chunk is emitted.
func TestAI21Provider_CompleteStream_JambaAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"server boom"}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "jamba-mini-1.7",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for non-2xx stream response, got nil")
	}
	if got := core.ParseStatusCode(err); got != http.StatusInternalServerError {
		t.Errorf("ParseStatusCode = %d, want %d", got, http.StatusInternalServerError)
	}
	if !strings.Contains(err.Error(), "server boom") {
		t.Errorf("error %q does not contain upstream message", err.Error())
	}
}

// TestAI21Provider_Complete_JambaRequestShape verifies the Jamba path issues a
// POST to /chat/completions with Bearer auth and forwards the requested model.
func TestAI21Provider_Complete_JambaRequestShape(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotModel  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotModel = body.Model

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","model":"jamba-large-1.7","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	if _, err := p.Complete(context.Background(), core.Request{
		Model:    "jamba-large-1.7",
		Messages: []core.Message{{Role: core.RoleUser, Content: "Hi"}},
	}); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want 'Bearer test-key'", gotAuth)
	}
	if gotModel != "jamba-large-1.7" {
		t.Errorf("forwarded model = %q, want jamba-large-1.7", gotModel)
	}
}

// TestNewAI21_RejectsInvalidBaseURL locks in the base-URL validation.
func TestNewAI21_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}

// TestAI21Provider_Complete_DetailError verifies AI21's native {"detail":…} error
// envelope surfaces the extracted message (not a raw JSON blob) on the Jamba path.
func TestAI21Provider_Complete_DetailError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Invalid API Key"}`))
	}))
	defer srv.Close()

	p, err := New("bad-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Complete(context.Background(), core.Request{
		Model:    "jamba-large-1.7",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "Invalid API Key") {
		t.Errorf("error = %q, want the extracted detail message", err.Error())
	}
	if code := core.ParseStatusCode(err); code != http.StatusUnauthorized {
		t.Errorf("ParseStatusCode = %d, want 401", code)
	}
}
