package mistral

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestMistralProvider_Transcribe_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.TranscriptionProvider = p
}

func TestMistralProvider_Transcribe_MockHTTP(t *testing.T) {
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
		if r.FormValue("model") != "voxtral-mini-latest" {
			t.Errorf("model = %q, want voxtral-mini-latest", r.FormValue("model"))
		}
		w.Header().Set("Content-Type", "application/json")
		// Mistral's response is richer than {text}; the shared decoder reads text.
		_, _ = w.Write([]byte(`{"model":"voxtral-mini-latest","text":"hello mistral","language":"en","usage":{"total_tokens":3}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Transcribe(context.Background(), core.TranscriptionRequest{
		Model: "voxtral-mini-latest", File: []byte("AUDIODATA"), Filename: "clip.mp3",
	})
	if err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if resp.Text != "hello mistral" {
		t.Errorf("Text = %q, want 'hello mistral'", resp.Text)
	}
}

// Mistral has no /audio/translations endpoint; a translate request must be
// refused locally rather than routed to a path that does not exist.
func TestMistralProvider_Transcribe_TranslateRefused(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Transcribe(context.Background(), core.TranscriptionRequest{
		Model: "voxtral-mini-latest", File: []byte("x"), Filename: "a.mp3", Translate: true,
	})
	if err == nil {
		t.Fatal("Transcribe(translate=true) error = nil, want refusal")
	}
	if code := core.ParseStatusCode(err); code != http.StatusBadRequest {
		t.Errorf("ParseStatusCode = %d, want 400", code)
	}
	if hit {
		t.Error("translate request reached the server; it must be refused before any HTTP call")
	}
}
