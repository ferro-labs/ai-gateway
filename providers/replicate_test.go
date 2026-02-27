package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewReplicate(t *testing.T) {
	p, err := NewReplicate("test-token", "", nil, nil)
	if err != nil {
		t.Fatalf("NewReplicate() error: %v", err)
	}
	if p.Name() != "replicate" {
		t.Errorf("Name() = %q, want replicate", p.Name())
	}
}

func TestReplicateProvider_SupportedModels_Defaults(t *testing.T) {
	p, _ := NewReplicate("test-token", "", nil, nil)
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if strings.Contains(m, "llama") {
			found = true
		}
	}
	if !found {
		t.Error("no llama model found in default supported models")
	}
}

func TestReplicateProvider_SupportedModels_Custom(t *testing.T) {
	textModels := []string{"owner/text-model"}
	imageModels := []string{"owner/image-model"}
	p, _ := NewReplicate("test-token", "", textModels, imageModels)
	models := p.SupportedModels()
	if len(models) != 2 {
		t.Fatalf("SupportedModels() returned %d, want 2", len(models))
	}
}

func TestReplicateProvider_SupportsModel(t *testing.T) {
	p, _ := NewReplicate("test-token", "", []string{"meta/meta-llama-3.1-8b-instruct"}, nil)
	if !p.SupportsModel("meta/meta-llama-3.1-8b-instruct") {
		t.Error("expected meta-llama model to be supported")
	}
	if p.SupportsModel("unknown/model") {
		t.Error("unknown model should not be supported")
	}
}

func TestReplicateProvider_SupportsModel_WithVersion(t *testing.T) {
	p, _ := NewReplicate("test-token", "", []string{"meta/model:abc123"}, nil)
	if !p.SupportsModel("meta/model") {
		t.Error("expected meta/model (without version) to match meta/model:abc123")
	}
}

func TestReplicateProvider_Models(t *testing.T) {
	p, _ := NewReplicate("test-token", "", nil, nil)
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "replicate" {
			t.Errorf("ModelInfo.OwnedBy = %q, want replicate", m.OwnedBy)
		}
	}
}

func TestReplicateProvider_AuthHeaders(t *testing.T) {
	p, _ := NewReplicate("test-token", "", nil, nil)
	headers := p.AuthHeaders()
	if headers["Authorization"] != "Token test-token" {
		t.Errorf("AuthHeaders Authorization = %q, want Token test-token", headers["Authorization"])
	}
}

func TestReplicateProvider_Complete_MockHTTP(t *testing.T) {
	// Mock Replicate: first POST creates the prediction, returns succeeded immediately.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		pred := replicatePrediction{
			ID:     "pred-123",
			Status: "succeeded",
			Output: []interface{}{"Hello", " world"},
		}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, _ := NewReplicate("test-token", srv.URL, []string{"meta/meta-llama-3.1-8b-instruct"}, nil)
	resp, err := p.Complete(context.Background(), Request{
		Model:    "meta/meta-llama-3.1-8b-instruct",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "pred-123" {
		t.Errorf("Response.ID = %q, want pred-123", resp.ID)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if resp.Choices[0].Message.Content != "Hello world" {
		t.Errorf("content = %q, want 'Hello world'", resp.Choices[0].Message.Content)
	}
}

func TestReplicateProvider_GenerateImage_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pred := replicatePrediction{
			ID:     "img-pred-1",
			Status: "succeeded",
			Output: []interface{}{"https://example.com/image.png"},
		}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, _ := NewReplicate("test-token", srv.URL, nil, []string{"black-forest-labs/flux-schnell"})
	resp, err := p.GenerateImage(context.Background(), ImageRequest{
		Model:  "black-forest-labs/flux-schnell",
		Prompt: "A robot",
		Size:   "1024x1024",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if len(resp.Data) == 0 {
		t.Fatal("expected at least one image")
	}
	if resp.Data[0].URL != "https://example.com/image.png" {
		t.Errorf("image URL = %q, want https://example.com/image.png", resp.Data[0].URL)
	}
}

func TestReplicateProvider_Complete_PollingBehavior(t *testing.T) {
	// First call: prediction is "processing", second call (poll): "succeeded"
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		var pred replicatePrediction
		if callCount == 1 {
			// Initial prediction
			pred = replicatePrediction{ID: "pred-poll", Status: "processing"}
		} else {
			// Poll request
			pred = replicatePrediction{ID: "pred-poll", Status: "succeeded", Output: "text result"}
		}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, _ := NewReplicate("test-token", srv.URL, []string{"test/model"}, nil)
	resp, err := p.Complete(context.Background(), Request{
		Model:    "test/model",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() polling error: %v", err)
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 calls (submit + poll), got %d", callCount)
	}
	if resp.Choices[0].Message.Content != "text result" {
		t.Errorf("polled content = %q, want 'text result'", resp.Choices[0].Message.Content)
	}
}
