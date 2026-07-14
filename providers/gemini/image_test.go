package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestGeminiProvider_GenerateImage_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.ImageProvider = p
}

func TestGeminiProvider_SupportsModel_Imagen(t *testing.T) {
	p, _ := New("test-key", "")
	if !p.SupportsModel("imagen-4.0-generate-001") {
		t.Error("expected imagen-4.0-generate-001 to be supported")
	}
}

func TestGeminiProvider_GenerateImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":predict") {
			t.Errorf("request path = %q, want suffix :predict", r.URL.Path)
		}
		if !strings.Contains(r.URL.Path, "imagen-4.0-generate-001") {
			t.Errorf("request path = %q, want it to contain the model id", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-key" {
			t.Errorf("x-goog-api-key header = %q, want test-key", got)
		}
		if got := r.URL.Query().Get("key"); got != "" {
			t.Errorf("key must not appear in the query string, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"predictions":[{"bytesBase64Encoded":"aGk=","mimeType":"image/png"}]}`))
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	n := 1
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

func TestGeminiProvider_GenerateImage_AllFiltered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"predictions":[{"raiFilteredReason":"safety"}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
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

func TestGeminiProvider_GenerateImage_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad image request"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "imagen-4.0-generate-001",
		Prompt: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "bad image request") {
		t.Fatalf("GenerateImage() error = %v, want upstream error", err)
	}
}
