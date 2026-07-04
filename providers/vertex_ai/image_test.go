package vertexai

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

func TestVertexAIProvider_GenerateImage_Interface(_ *testing.T) {
	p, _ := New(Options{ProjectID: "demo-project", Region: "us-central1", APIKey: testAPIKey})
	var _ core.ImageProvider = p
}

func TestVertexAIProvider_SupportsModel_Imagen(t *testing.T) {
	p, _ := New(Options{ProjectID: "demo-project", Region: "us-central1", APIKey: testAPIKey})
	if !p.SupportsModel("imagen-4.0-generate-001") {
		t.Error("expected imagen-4.0-generate-001 to be supported")
	}
	if !p.SupportsModel("imagen-3.0-generate-002") {
		t.Error("expected imagen-3.0-generate-002 to be supported")
	}
}

func TestVertexAIProvider_GenerateImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/publishers/google/models/imagen-4.0-generate-001:predict") {
			t.Errorf("request path = %q, want it to contain /publishers/google/models/imagen-4.0-generate-001:predict", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != testAPIKey {
			t.Errorf("x-goog-api-key = %q, want %s", r.Header.Get("x-goog-api-key"), testAPIKey)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"predictions":[{"bytesBase64Encoded":"aGk=","mimeType":"image/png"}]}`))
	}))
	defer srv.Close()

	p, _ := New(Options{ProjectID: "demo-project", Region: "us-central1", APIKey: testAPIKey})
	p.SetBaseURL(srv.URL)

	n := 2
	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "imagen-4.0-generate-001",
		Prompt: "a red panda",
		N:      &n,
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len(Data) = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].B64JSON != "aGk=" {
		t.Errorf("B64JSON = %q, want aGk=", resp.Data[0].B64JSON)
	}
	if resp.Created == 0 {
		t.Error("Created should be set")
	}
}

func TestVertexAIProvider_GenerateImage_UltraClampsSampleCount(t *testing.T) {
	var gotSampleCount *int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Parameters struct {
				SampleCount *int `json:"sampleCount"`
			} `json:"parameters"`
		}
		_ = json.Unmarshal(body, &req)
		gotSampleCount = req.Parameters.SampleCount
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"predictions":[{"bytesBase64Encoded":"aGk=","mimeType":"image/png"}]}`))
	}))
	defer srv.Close()

	p, _ := New(Options{ProjectID: "demo-project", Region: "us-central1", APIKey: testAPIKey})
	p.SetBaseURL(srv.URL)

	n := 4
	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "imagen-4.0-ultra-generate-001",
		Prompt: "x",
		N:      &n,
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if gotSampleCount == nil || *gotSampleCount != 1 {
		t.Errorf("ultra sampleCount = %v, want 1", gotSampleCount)
	}
}

func TestVertexAIProvider_GenerateImage_AllFiltered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"predictions":[{"raiFilteredReason":"safety"}]}`))
	}))
	defer srv.Close()

	p, _ := New(Options{ProjectID: "demo-project", Region: "us-central1", APIKey: testAPIKey})
	p.SetBaseURL(srv.URL)

	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "imagen-4.0-generate-001",
		Prompt: "blocked",
	})
	if err == nil {
		t.Fatal("expected error when all predictions are filtered")
	}
	if !strings.Contains(err.Error(), "safety") {
		t.Errorf("error should surface the safety-filter reason, got %q", err.Error())
	}
}

func TestVertexAIProvider_GenerateImage_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad image request"}}`))
	}))
	defer srv.Close()

	p, _ := New(Options{ProjectID: "demo-project", Region: "us-central1", APIKey: testAPIKey})
	p.SetBaseURL(srv.URL)

	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "imagen-4.0-generate-001",
		Prompt: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "bad image request") {
		t.Fatalf("GenerateImage() error = %v, want upstream error", err)
	}
}
