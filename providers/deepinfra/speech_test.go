package deepinfra

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestDeepInfraProvider_Speech_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.SpeechProvider = p
}

// DeepInfra's OpenAI /audio/speech route returns raw audio bytes (unlike its
// native /v1/inference route, which wraps them in base64-in-JSON).
func TestDeepInfraProvider_Speech_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/audio/speech") {
			t.Errorf("path = %q, want suffix /audio/speech", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("DEEPINFRAAUDIO"))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Speech(context.Background(), core.SpeechRequest{
		Model: "hexgrad/Kokoro-82M", Input: "hello", Voice: "af_bella", ResponseFormat: "mp3",
	})
	if err != nil {
		t.Fatalf("Speech() error: %v", err)
	}
	if string(resp.Audio) != "DEEPINFRAAUDIO" {
		t.Errorf("Audio = %q, want DEEPINFRAAUDIO", resp.Audio)
	}
	if resp.ContentType != "audio/mpeg" {
		t.Errorf("ContentType = %q, want audio/mpeg", resp.ContentType)
	}
}
