package deepinfra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestDeepInfraProvider_GenerateImage_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.ImageProvider = p
}

func TestDeepInfraProvider_GenerateImage_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			t.Errorf("path = %q, want /images/generations", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		// size is forwarded for DeepInfra (FLUX/SD accept it).
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["size"] != "1024x1024" {
			t.Errorf("size = %v, want 1024x1024", body["size"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":1700000000,"data":[{"b64_json":"aGVsbG8="}]}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "black-forest-labs/FLUX-1-schnell",
		Prompt: "a red apple",
		Size:   "1024x1024",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if resp.Created != 1700000000 {
		t.Errorf("Created = %d, want 1700000000", resp.Created)
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "aGVsbG8=" {
		t.Errorf("Data = %+v, want one b64 image", resp.Data)
	}
}

func TestDeepInfraProvider_GenerateImage_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.GenerateImage(context.Background(), core.ImageRequest{Model: "black-forest-labs/FLUX-1-schnell", Prompt: "x"})
	if err == nil {
		t.Fatal("GenerateImage() error = nil, want error for 500 response")
	}
	if core.ParseStatusCode(err) != http.StatusInternalServerError {
		t.Errorf("ParseStatusCode = %d, want 500", core.ParseStatusCode(err))
	}
}
