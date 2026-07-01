package nvidianim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

const testBearerAPIKey = "Bearer test-key"

const testEmbeddingModel = "nvidia/nv-embedqa-e5-v5"

func TestNewNVIDIANIM(t *testing.T) {
	p, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "nvidia-nim" {
		t.Errorf("Name() = %q, want nvidia-nim", p.Name())
	}
}

func TestNVIDIANIMProvider_SupportedModels(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "nvidia/Llama-3.1-Nemotron-70B-Instruct" {
			found = true
		}
	}
	if !found {
		t.Error("nvidia/Llama-3.1-Nemotron-70B-Instruct not found")
	}
}

func TestNVIDIANIMProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("nvidia/Llama-3.1-Nemotron-70B-Instruct") {
		t.Error("expected nvidia/Llama-3.1-Nemotron-70B-Instruct to be supported")
	}
	if !p.SupportsModel("custom-model") {
		t.Error("passthrough: expected all models to return true")
	}
}

func TestNVIDIANIMProvider_Models(t *testing.T) {
	p, _ := New("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "nvidia-nim" {
			t.Errorf("ModelInfo.OwnedBy = %q, want nvidia-nim", m.OwnedBy)
		}
	}
}

func TestNVIDIANIMProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.StreamProvider = p
}

func TestNVIDIANIMProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-key", "")
	headers := p.AuthHeaders()
	if headers["Authorization"] != testBearerAPIKey {
		t.Errorf("AuthHeaders Authorization = %q, want %s", headers["Authorization"], testBearerAPIKey)
	}
}

func TestNVIDIANIMProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"cmpl-1\",\"model\":\"nvidia/Llama-3.1-Nemotron-70B-Instruct\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"nvidia/Llama-3.1-Nemotron-70B-Instruct\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"nvidia/Llama-3.1-Nemotron-70B-Instruct\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"cmpl-1\",\"model\":\"nvidia/Llama-3.1-Nemotron-70B-Instruct\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "nvidia/Llama-3.1-Nemotron-70B-Instruct",
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

func TestNVIDIANIMProvider_Complete_MockHTTP(t *testing.T) {
	respBody := `{"id":"cmpl-1","model":"nvidia/Llama-3.1-Nemotron-70B-Instruct","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "nvidia/Llama-3.1-Nemotron-70B-Instruct",
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

func TestNVIDIANIMProvider_Embed_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.EmbeddingProvider = p
}

func TestNVIDIANIMProvider_Embed_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != testBearerAPIKey {
			t.Errorf("Authorization = %q, want %s", got, testBearerAPIKey)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model"] != testEmbeddingModel {
			t.Errorf("model = %v, want %s", body["model"], testEmbeddingModel)
		}
		if body["input_type"] != "query" {
			t.Errorf("input_type = %v, want query", body["input_type"])
		}
		if _, present := body["truncate"]; present {
			t.Errorf("truncate present = %v, want field removed from request", body["truncate"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.5,0.6],"index":0}],"model":"` + testEmbeddingModel + `","usage":{"prompt_tokens":2,"total_tokens":2}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model:     testEmbeddingModel,
		Input:     "hello world",
		InputType: "query",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if resp.Object != "list" || resp.Model != testEmbeddingModel {
		t.Errorf("resp = %+v, want list/%s", resp, testEmbeddingModel)
	}
	if len(resp.Data) != 1 || !reflect.DeepEqual(resp.Data[0].Embedding, []float64{0.5, 0.6}) {
		t.Errorf("Data = %+v, want one embedding", resp.Data)
	}
	if resp.Usage.PromptTokens != 2 || resp.Usage.TotalTokens != 2 {
		t.Errorf("Usage = %+v, want prompt=2 total=2", resp.Usage)
	}
}

func TestNVIDIANIMProvider_Embed_NoInputTypeDefaultInjection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if _, present := body["input_type"]; present {
			t.Errorf("input_type present = %v, want omitted when req.InputType is empty", body["input_type"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}],"model":"` + testEmbeddingModel + `","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	if _, err := p.Embed(context.Background(), core.EmbeddingRequest{Model: testEmbeddingModel, Input: "hi"}); err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
}
