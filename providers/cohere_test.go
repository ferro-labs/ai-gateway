package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewCohere(t *testing.T) {
	p, err := NewCohere("test-key", "")
	if err != nil {
		t.Fatalf("NewCohere() error: %v", err)
	}
	if p.Name() != "cohere" {
		t.Errorf("Name() = %q, want cohere", p.Name())
	}
}

func TestCohereProvider_SupportedModels(t *testing.T) {
	p, _ := NewCohere("test-key", "")
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if m == "command-r-plus" {
			found = true
		}
	}
	if !found {
		t.Error("command-r-plus not found")
	}
}

func TestCohereProvider_SupportsModel(t *testing.T) {
	p, _ := NewCohere("test-key", "")
	if !p.SupportsModel("command-r-plus") {
		t.Error("expected command-r-plus to be supported")
	}
	if !p.SupportsModel("command") {
		t.Error("expected command to be supported")
	}
	if p.SupportsModel("gpt-4o") {
		t.Error("cohere should not support gpt-4o")
	}
}

func TestCohereProvider_Models(t *testing.T) {
	p, _ := NewCohere("test-key", "")
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "cohere" {
			t.Errorf("ModelInfo.OwnedBy = %q, want cohere", m.OwnedBy)
		}
	}
}

func TestCohereProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := NewCohere("test-key", "")
	var _ StreamProvider = p
}

func TestCohereProvider_CompleteStream_MockSSE(t *testing.T) {
	sseData := "data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\"Hello\"}}}}\n\n" +
		"data: {\"type\":\"content-delta\",\"delta\":{\"message\":{\"content\":{\"text\":\" there\"}}}}\n\n" +
		"data: {\"type\":\"message-end\",\"delta\":{\"finish_reason\":\"COMPLETE\",\"usage\":{\"billed_units\":{\"input_tokens\":5,\"output_tokens\":2},\"tokens\":{\"input_tokens\":5,\"output_tokens\":2}}}}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseData))
	}))
	defer srv.Close()

	p, _ := NewCohere("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), Request{
		Model:    "command-r-plus",
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
	if chunks[0].Choices[0].Delta.Content != "Hello" {
		t.Errorf("delta content = %q, want Hello", chunks[0].Choices[0].Delta.Content)
	}
	//nolint:goconst // " there" appears in multiple test strings; fine in tests
	if chunks[1].Choices[0].Delta.Content != " there" {
		t.Errorf("delta content = %q, want ' there'", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].FinishReason != "COMPLETE" {
		t.Errorf("finish_reason = %q, want COMPLETE", chunks[2].Choices[0].FinishReason)
	}
}
