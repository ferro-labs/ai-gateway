package groq

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestGroqProvider_Speech_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.SpeechProvider = p
}

func TestGroqProvider_Speech_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/audio/speech") {
			t.Errorf("path = %q, want suffix /audio/speech", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("GROQAUDIO"))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Speech(context.Background(), core.SpeechRequest{
		Model: "playai-tts", Input: "hello", Voice: "Fritz-PlayAI", ResponseFormat: "wav",
	})
	if err != nil {
		t.Fatalf("Speech() error: %v", err)
	}
	if string(resp.Audio) != "GROQAUDIO" {
		t.Errorf("Audio = %q, want GROQAUDIO", resp.Audio)
	}
	// ContentType is derived from the requested format, not the upstream header.
	if resp.ContentType != "audio/wav" {
		t.Errorf("ContentType = %q, want audio/wav", resp.ContentType)
	}
}
