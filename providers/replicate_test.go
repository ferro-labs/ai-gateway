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

func TestModelVersion(t *testing.T) {
	tests := []struct {
		path    string
		wantVer string
	}{
		{"owner/name", ""},
		{"owner/name:abc123", "abc123"},
		{"owner/name:sha256deadbeef", "sha256deadbeef"},
	}
	for _, tc := range tests {
		if got := modelVersion(tc.path); got != tc.wantVer {
			t.Errorf("modelVersion(%q) = %q, want %q", tc.path, got, tc.wantVer)
		}
	}
}

func TestReplicateProvider_Complete_PinnedVersion(t *testing.T) {
	// Verify that when the registered model has a version suffix, Complete()
	// sends the request to /predictions with a "version" field in the body,
	// instead of /models/{owner}/{name}/predictions.
	var gotPath string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		pred := replicatePrediction{ID: "pred-ver", Status: "succeeded", Output: "ok"}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	const versionedModel = "meta/llama:abc123"
	p, _ := NewReplicate("test-token", srv.URL, []string{versionedModel}, nil)
	_, err := p.Complete(context.Background(), Request{
		Model:    "meta/llama",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if gotPath != "/predictions" {
		t.Errorf("request path = %q, want /predictions", gotPath)
	}
	if gotBody["version"] != "abc123" {
		t.Errorf("body[\"version\"] = %v, want abc123", gotBody["version"])
	}
	if _, ok := gotBody["input"]; !ok {
		t.Error("body missing \"input\" field")
	}
}

func TestReplicateProvider_GenerateImage_PinnedVersion(t *testing.T) {
	// Same check as above but for GenerateImage().
	var gotPath string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		pred := replicatePrediction{ID: "img-ver", Status: "succeeded", Output: []interface{}{"https://example.com/img.png"}}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	const versionedModel = "black-forest-labs/flux-schnell:deadbeef"
	p, _ := NewReplicate("test-token", srv.URL, nil, []string{versionedModel})
	_, err := p.GenerateImage(context.Background(), ImageRequest{
		Model:  "black-forest-labs/flux-schnell",
		Prompt: "A cat",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if gotPath != "/predictions" {
		t.Errorf("request path = %q, want /predictions", gotPath)
	}
	if gotBody["version"] != "deadbeef" {
		t.Errorf("body[\"version\"] = %v, want deadbeef", gotBody["version"])
	}
}

func TestReplicateProvider_Complete_NoVersion_UsesModelPath(t *testing.T) {
	// When no version is present the URL must be /models/{owner}/{name}/predictions.
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		pred := replicatePrediction{ID: "pred-nover", Status: "succeeded", Output: "ok"}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, _ := NewReplicate("test-token", srv.URL, []string{"meta/llama"}, nil)
	_, err := p.Complete(context.Background(), Request{
		Model:    "meta/llama",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if gotPath != "/models/meta/llama/predictions" {
		t.Errorf("request path = %q, want /models/meta/llama/predictions", gotPath)
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
		w.Header().Set("Content-Type", "application/json")
		var pred replicatePrediction
		if callCount == 1 {
			// Initial submission: 201 Created with processing status.
			pred = replicatePrediction{ID: "pred-poll", Status: "processing"}
			w.WriteHeader(http.StatusCreated)
		} else {
			// Poll request: 200 OK with succeeded status.
			pred = replicatePrediction{ID: "pred-poll", Status: "succeeded", Output: "text result"}
			w.WriteHeader(http.StatusOK)
		}
		data, _ := json.Marshal(pred)
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

// ── GenerateImage size validation ─────────────────────────────────────────────

func TestReplicateProvider_GenerateImage_InvalidSize(t *testing.T) {
	p, _ := NewReplicate("test-token", "http://unused", nil, []string{"owner/model"})

	badSizes := []string{
		"1024",        // only one dimension
		"axb",         // non-integer
		"0x1024",      // zero width
		"1024x0",      // zero height
		"-512x512",    // negative width
		"512x-512",    // negative height
		"1024 x 1024", // spaces instead of 'x'
		"",            // empty is fine (skipped) — not tested here, see valid test
	}
	for _, size := range badSizes {
		if size == "" {
			continue
		}
		_, err := p.GenerateImage(context.Background(), ImageRequest{
			Model:  "owner/model",
			Prompt: "A robot",
			Size:   size,
		})
		if err == nil {
			t.Errorf("GenerateImage() with Size=%q: expected error, got nil", size)
		}
		if !strings.Contains(err.Error(), size) {
			t.Errorf("error for size %q should mention the bad value; got: %v", size, err)
		}
	}
}

func TestReplicateProvider_GenerateImage_ValidSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pred := replicatePrediction{ID: "img-sz", Status: "succeeded", Output: []interface{}{"https://example.com/img.png"}}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, _ := NewReplicate("test-token", srv.URL, nil, []string{"owner/model"})
	_, err := p.GenerateImage(context.Background(), ImageRequest{
		Model:  "owner/model",
		Prompt: "A robot",
		Size:   "1024x1024",
	})
	if err != nil {
		t.Fatalf("GenerateImage() with valid size: unexpected error: %v", err)
	}
}

// ── Poll loop error handling ───────────────────────────────────────────────────

func TestReplicateProvider_Poll_NonOKStatus(t *testing.T) {
	// First call: submit — returns processing. Second call: poll — returns 429.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		if callCount == 1 {
			pred := replicatePrediction{ID: "pred-poll-err", Status: "processing"}
			data, _ := json.Marshal(pred)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(data)
			return
		}
		// Poll response: non-200 error.
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"detail":"rate limited"}`))
	}))
	defer srv.Close()

	p, _ := NewReplicate("test-token", srv.URL, []string{"test/model"}, nil)
	_, err := p.Complete(context.Background(), Request{
		Model:    "test/model",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error from non-200 poll response, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention HTTP status 429; got: %v", err)
	}
}
