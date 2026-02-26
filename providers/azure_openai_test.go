package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewAzureOpenAI(t *testing.T) {
	p, err := NewAzureOpenAI("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	if err != nil {
		t.Fatalf("NewAzureOpenAI() error: %v", err)
	}
	if p.Name() != "azure-openai" {
		t.Errorf("Name() = %q, want azure-openai", p.Name())
	}
}

func TestAzureOpenAIProvider_DefaultApiVersion(t *testing.T) {
	p, _ := NewAzureOpenAI("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	if p.apiVersion != "2024-10-21" {
		t.Errorf("apiVersion = %q, want 2024-10-21", p.apiVersion)
	}
}

func TestAzureOpenAIProvider_CustomApiVersion(t *testing.T) {
	p, _ := NewAzureOpenAI("test-key", "https://myresource.openai.azure.com", "gpt-4o", "2024-06-01")
	if p.apiVersion != "2024-06-01" {
		t.Errorf("apiVersion = %q, want 2024-06-01", p.apiVersion)
	}
}

func TestAzureOpenAIProvider_SupportedModels(t *testing.T) {
	p, _ := NewAzureOpenAI("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	models := p.SupportedModels()
	if len(models) != 1 {
		t.Fatalf("SupportedModels() returned %d models, want 1", len(models))
	}
	if models[0] != "gpt-4o" {
		t.Errorf("SupportedModels()[0] = %q, want gpt-4o", models[0])
	}
}

func TestAzureOpenAIProvider_SupportsModel(t *testing.T) {
	p, _ := NewAzureOpenAI("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	if !p.SupportsModel("gpt-4o") {
		t.Error("expected gpt-4o to be supported")
	}
	if !p.SupportsModel("gpt-3.5-turbo") {
		t.Error("passthrough: expected any model to return true")
	}
}

func TestAzureOpenAIProvider_Models(t *testing.T) {
	p, _ := NewAzureOpenAI("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	models := p.Models()
	if len(models) != 1 {
		t.Fatalf("Models() returned %d, want 1", len(models))
	}
	if models[0].OwnedBy != "azure-openai" {
		t.Errorf("ModelInfo.OwnedBy = %q, want azure-openai", models[0].OwnedBy)
	}
}

func TestAzureOpenAIProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := NewAzureOpenAI("test-key", "https://myresource.openai.azure.com", "gpt-4o", "")
	var _ StreamProvider = p
}

func TestAzureOpenAIProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := NewAzureOpenAI("test-key", srv.URL, "gpt-4o", "2024-10-21")
	ch, err := p.CompleteStream(context.Background(), Request{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}

	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}
	if chunks[1].Choices[0].Delta.Content != "Hello" {
		t.Errorf("delta content = %q, want Hello", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].Delta.Content != " world" {
		t.Errorf("delta content = %q, want ' world'", chunks[2].Choices[0].Delta.Content)
	}
}
