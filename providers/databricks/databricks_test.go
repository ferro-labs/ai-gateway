package databricks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
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
	for _, want := range []string{"databricks-bge-large-en", "databricks-gte-large-en"} {
		foundEmbedding := false
		for _, m := range models {
			if m == want {
				foundEmbedding = true
				break
			}
		}
		if !foundEmbedding {
			t.Errorf("%s not found", want)
		}
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
	if p.SupportsModel("") {
		t.Error("empty model should not be supported")
	}
	if p.SupportsModel("  ") {
		t.Error("blank model should not be supported")
	}
	if !p.SupportsModel("databricks-bge-large-en") {
		t.Error("expected databricks-bge-large-en to be supported")
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

func TestDatabricksProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("test-key", "https://dbc.example.com")
	var _ core.EmbeddingProvider = p
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

func TestDatabricksProvider_Embed_StringInput_MockHTTP(t *testing.T) {
	testDatabricksEmbedSuccess(t, "hello world")
}

func TestDatabricksProvider_Embed_StringSliceInput_MockHTTP(t *testing.T) {
	testDatabricksEmbedSuccess(t, []string{"hello", "world"})
}

func TestDatabricksProvider_Embed_InterfaceSliceInput_MockHTTP(t *testing.T) {
	testDatabricksEmbedSuccess(t, []interface{}{"hello", "world"})
}

func TestDatabricksProvider_Embed_InvalidInput(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	badInputs := []struct {
		name  string
		input interface{}
	}{
		{"nil", nil},
		{"integer", 42},
		{"empty-string", ""},
		{"blank-string", "  "},
		{"empty-string-slice", []string{}},
		{"empty-interface-slice", []interface{}{}},
		{"empty-string-slice-member", []string{"ok", ""}},
		{"blank-string-slice-member", []string{"ok", "  "}},
		{"empty-interface-slice-member", []interface{}{"ok", ""}},
		{"blank-interface-slice-member", []interface{}{"ok", "  "}},
		{"non-string-array-member", []interface{}{"ok", 42}},
	}
	for _, tc := range badInputs {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Embed(context.Background(), core.EmbeddingRequest{
				Model: "databricks-bge-large-en",
				Input: tc.input,
			})
			if err == nil {
				t.Fatalf("Embed() error = nil, want error")
			}
		})
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func TestDatabricksProvider_Embed_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/serving-endpoints/embeddings" {
			t.Errorf("path = %q, want /serving-endpoints/embeddings", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"serving endpoint unavailable","type":"bad_gateway"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "databricks-bge-large-en",
		Input: "hello",
	})
	if err == nil {
		t.Fatal("Embed() error = nil, want upstream error")
	}
	if !strings.Contains(err.Error(), "serving endpoint unavailable") {
		t.Fatalf("error = %v, want serving endpoint unavailable message", err)
	}
}

func testDatabricksEmbedSuccess(t *testing.T, input interface{}) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/serving-endpoints/embeddings" {
			t.Errorf("path = %q, want /serving-endpoints/embeddings", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if got := body["model"]; got != "databricks-bge-large-en" {
			t.Errorf("model = %v, want databricks-bge-large-en", got)
		}
		assertDatabricksEmbeddingInput(t, body["input"], input)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0},{"object":"embedding","embedding":[0.3,0.4],"index":1}],"model":"databricks-bge-large-en","usage":{"prompt_tokens":4,"total_tokens":4}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "databricks-bge-large-en",
		Input: input,
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("Object = %q, want list", resp.Object)
	}
	if resp.Model != "databricks-bge-large-en" {
		t.Errorf("Model = %q, want databricks-bge-large-en", resp.Model)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("Data length = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].Object != "embedding" || resp.Data[0].Index != 0 || !reflect.DeepEqual(resp.Data[0].Embedding, []float64{0.1, 0.2}) {
		t.Errorf("Data[0] = %+v, want mapped embedding at index 0", resp.Data[0])
	}
	if resp.Data[1].Object != "embedding" || resp.Data[1].Index != 1 || !reflect.DeepEqual(resp.Data[1].Embedding, []float64{0.3, 0.4}) {
		t.Errorf("Data[1] = %+v, want mapped embedding at index 1", resp.Data[1])
	}
	if resp.Usage.PromptTokens != 4 || resp.Usage.TotalTokens != 4 {
		t.Errorf("Usage = %+v, want prompt_tokens=4 total_tokens=4", resp.Usage)
	}
}

func assertDatabricksEmbeddingInput(t *testing.T, got interface{}, want interface{}) {
	t.Helper()

	switch w := want.(type) {
	case string:
		if got != w {
			t.Errorf("input = %#v, want %q", got, w)
		}
	case []string:
		gotSlice, ok := got.([]interface{})
		if !ok {
			t.Fatalf("input = %T, want JSON array", got)
		}
		if len(gotSlice) != len(w) {
			t.Fatalf("input length = %d, want %d", len(gotSlice), len(w))
		}
		for i := range w {
			if gotSlice[i] != w[i] {
				t.Errorf("input[%d] = %#v, want %q", i, gotSlice[i], w[i])
			}
		}
	case []interface{}:
		gotSlice, ok := got.([]interface{})
		if !ok {
			t.Fatalf("input = %T, want JSON array", got)
		}
		if !reflect.DeepEqual(gotSlice, w) {
			t.Errorf("input = %#v, want %#v", gotSlice, w)
		}
	default:
		t.Fatalf("unsupported expected input type %T", want)
	}
}
