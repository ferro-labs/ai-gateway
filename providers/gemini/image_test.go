package gemini

import (
	"context"
	"encoding/json"
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

// TestGeminiProvider_GenerateImage_UltraClampsSampleCount verifies an Imagen
// ultra model, which generates a single image per request, gets sampleCount
// clamped to 1 rather than an n the API rejects.
func TestGeminiProvider_GenerateImage_UltraClampsSampleCount(t *testing.T) {
	var gotSampleCount *int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Parameters struct {
				SampleCount *int `json:"sampleCount"`
			} `json:"parameters"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotSampleCount = req.Parameters.SampleCount
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"predictions":[{"bytesBase64Encoded":"aGk=","mimeType":"image/png"}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)

	n := 4
	if _, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "imagen-4.0-ultra-generate-001",
		Prompt: "x",
		N:      &n,
	}); err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if gotSampleCount == nil || *gotSampleCount != 1 {
		t.Errorf("ultra sampleCount = %v, want 1", gotSampleCount)
	}
}

// TestGeminiProvider_GenerateImage_RejectsURLFormat proves the refusal happens
// here rather than only in core: an entry in the representation map that no
// provider consults is inert, and the caller still gets a 200 carrying base64
// they did not ask for. The stub fails the test if it is reached, so this also
// pins that the refusal costs no upstream call.
func TestGeminiProvider_GenerateImage_RejectsURLFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("upstream must not be called for a format the provider cannot produce")
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model: "imagen-4.0-generate-001", Prompt: "a cat", ResponseFormat: "url",
	})
	if err == nil {
		t.Fatal("GenerateImage = nil error, want a refusal (Imagen returns base64 only)")
	}
	if got := core.ParseStatusCode(err); got != 400 {
		t.Errorf("status = %d, want 400", got)
	}
}

// TestGeminiProvider_GenerateImage_FlashImageGenerateContent proves a
// gemini-*-image model routes to :generateContent (not Imagen :predict), asks
// for image output, and decodes the base64 image from the candidate's inlineData
// part. Payload shape is the documented generateContent image response.
func TestGeminiProvider_GenerateImage_FlashImageGenerateContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":generateContent") {
			t.Errorf("request path = %q, want suffix :generateContent", r.URL.Path)
		}
		if !strings.Contains(r.URL.Path, "gemini-2.5-flash-image") {
			t.Errorf("request path = %q, want it to contain the model id", r.URL.Path)
		}
		var body geminiRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.GenerationConfig == nil || len(body.GenerationConfig.ResponseModalities) == 0 {
			t.Error("request should set responseModalities so the model returns an image")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[` +
			`{"inlineData":{"mimeType":"image/png","data":"aGk="}}]},"finishReason":"STOP"}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "gemini-2.5-flash-image",
		Prompt: "a red panda",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "aGk=" {
		t.Fatalf("Data = %+v, want one image with B64JSON aGk=", resp.Data)
	}
	if resp.Created == 0 {
		t.Error("Created should be set")
	}
}

// TestGeminiProvider_GenerateImage_GenerateContentNoImage errors when the model
// returns a text-only candidate (e.g. a refusal) rather than surfacing an empty
// image list as success.
func TestGeminiProvider_GenerateImage_GenerateContentNoImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"I cannot create that"}]}}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model: "gemini-2.5-flash-image", Prompt: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "no image data") {
		t.Fatalf("error = %v, want a no-image-data error", err)
	}
}
