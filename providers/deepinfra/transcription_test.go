package deepinfra

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestDeepInfraProvider_Transcribe_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.TranscriptionProvider = p
}

func TestDeepInfraProvider_Transcribe_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if !strings.HasSuffix(r.URL.Path, "/audio/transcriptions") {
			t.Errorf("path = %q, want suffix /audio/transcriptions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		// DeepInfra whisper ids are namespaced (openai/whisper-large-v3-turbo).
		if r.FormValue("model") != "openai/whisper-large-v3-turbo" {
			t.Errorf("model = %q, want openai/whisper-large-v3-turbo", r.FormValue("model"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello deepinfra"}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Transcribe(context.Background(), core.TranscriptionRequest{
		Model: "openai/whisper-large-v3-turbo", File: []byte("AUDIODATA"), Filename: "clip.mp3",
	})
	if err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if resp.Text != "hello deepinfra" {
		t.Errorf("Text = %q, want 'hello deepinfra'", resp.Text)
	}
}

func TestDeepInfraProvider_Transcribe_TranslatePath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"translated"}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	if _, err := p.Transcribe(context.Background(), core.TranscriptionRequest{
		Model: "openai/whisper-large-v3", File: []byte("x"), Filename: "a.mp3", Translate: true,
	}); err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/audio/translations") {
		t.Errorf("path = %q, want suffix /audio/translations (translate=true)", gotPath)
	}
}
