package azureopenai

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

func TestAzureOpenAIProvider_Speech_Interface(_ *testing.T) {
	p, _ := New("test-key", "https://res.openai.azure.com", "tts-dep", "")
	var _ core.SpeechProvider = p
}

func TestAzureOpenAIProvider_Speech_MockHTTP(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/openai/deployments/tts-dep/audio/speech") {
			t.Errorf("path = %q, want .../deployments/tts-dep/audio/speech", r.URL.Path)
		}
		if got := r.Header.Get("api-key"); got != "test-key" {
			t.Errorf("api-key = %q, want test-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty (Azure uses api-key)", got)
		}
		if v := r.URL.Query().Get("api-version"); v != "2025-04-01-preview" {
			t.Errorf("api-version = %q, want 2025-04-01-preview", v)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("AZUREAUDIO"))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL, "tts-dep", "2025-04-01-preview")
	resp, err := p.Speech(context.Background(), core.SpeechRequest{
		Input: "hello", Voice: "alloy", ResponseFormat: "mp3",
	})
	if err != nil {
		t.Fatalf("Speech() error: %v", err)
	}
	if string(resp.Audio) != "AZUREAUDIO" {
		t.Errorf("Audio = %q, want AZUREAUDIO", resp.Audio)
	}
	// ContentType comes from the requested format, not Azure's octet-stream.
	if resp.ContentType != "audio/mpeg" {
		t.Errorf("ContentType = %q, want audio/mpeg", resp.ContentType)
	}
	// The deployment is in the URL; the body must carry no meaningful model.
	if m, ok := body["model"]; ok && m != "" {
		t.Errorf("model field = %v, want empty (deployment is in the URL path)", m)
	}
}
