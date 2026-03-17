package databricks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const testBearerAPIKey = "Bearer test-key"

func TestNewDatabricks(t *testing.T) {
	p, err := New("test-key", "https://dbc.example.com")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "databricks" {
		t.Errorf("Name() = %q, want databricks", p.Name())
	}
	if got := p.BaseURL(); got != "https://dbc.example.com/serving-endpoints" {
		t.Errorf("BaseURL() = %q, want normalized serving-endpoints path", got)
	}
}

func TestDatabricksProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "https://dbc.example.com")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "databricks-claude-sonnet-4-5" {
			found = true
		}
	}
	if !found {
		t.Error("databricks-claude-sonnet-4-5 not found")
	}
}

func TestDatabricksProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "https://dbc.example.com")
	if !p.SupportsModel("databricks-claude-sonnet-4-5") {
		t.Error("expected databricks-claude-sonnet-4-5 to be supported")
	}
	if !p.SupportsModel("custom-endpoint-name") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestDatabricksProvider_Models(t *testing.T) {
	p, _ := New("test-key", "https://dbc.example.com")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "databricks" {
			t.Errorf("ModelInfo.OwnedBy = %q, want databricks", m.OwnedBy)
		}
	}
}

func TestDatabricksProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "https://dbc.example.com")
	var _ core.StreamProvider = p
}

func TestDatabricksProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-key", "https://dbc.example.com")
	headers := p.AuthHeaders()
	if headers["Authorization"] != testBearerAPIKey {
		t.Errorf("AuthHeaders Authorization = %q, want %s", headers["Authorization"], testBearerAPIKey)
	}
}

func TestDatabricksProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"cmpl-1\",\"model\":\"databricks-claude-sonnet-4-5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"databricks-claude-sonnet-4-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"databricks-claude-sonnet-4-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"databricks-claude-sonnet-4-5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Fatalf("path = %q, want suffix /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "databricks-claude-sonnet-4-5",
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
	if chunks[1].Choices[0].Delta.Content != "Hello" {
		t.Errorf("delta content = %q, want Hello", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].Delta.Content != " there" {
		t.Errorf("delta content = %q, want ' there'", chunks[2].Choices[0].Delta.Content)
	}
}

func TestDatabricksProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"databricks-claude-sonnet-4-5","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Fatalf("path = %q, want suffix /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "databricks-claude-sonnet-4-5",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "cmpl-1" {
		t.Errorf("Response.ID = %q, want cmpl-1", resp.ID)
	}
	if len(resp.Choices) == 0 {
		t.Error("expected at least one choice")
	}
}
