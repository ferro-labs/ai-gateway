package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestOpenAIProvider_Transcribe_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.TranscriptionProvider = p
}

func TestOpenAIProvider_Transcribe_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %q, want /v1/audio/transcriptions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if r.FormValue("model") != "whisper-1" {
			t.Errorf("model = %q, want whisper-1", r.FormValue("model"))
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		b, _ := io.ReadAll(file)
		if string(b) != "AUDIODATA" {
			t.Errorf("file bytes = %q, want AUDIODATA", b)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"hello world"}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Transcribe(context.Background(), core.TranscriptionRequest{
		Model:    "whisper-1",
		File:     []byte("AUDIODATA"),
		Filename: "clip.mp3",
	})
	if err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if resp.Text != "hello world" {
		t.Errorf("Text = %q, want 'hello world'", resp.Text)
	}
}

func TestOpenAIProvider_Transcribe_TranslatePath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"translated"}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	if _, err := p.Transcribe(context.Background(), core.TranscriptionRequest{
		Model: "whisper-1", File: []byte("x"), Filename: "a.mp3", Translate: true,
	}); err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if gotPath != "/v1/audio/translations" {
		t.Errorf("path = %q, want /v1/audio/translations (translate=true)", gotPath)
	}
}

func TestOpenAIProvider_Transcribe_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Transcribe(context.Background(), core.TranscriptionRequest{Model: "whisper-1", File: []byte("x"), Filename: "a.mp3"})
	if err == nil {
		t.Fatal("Transcribe() error = nil, want 401")
	}
	if core.ParseStatusCode(err) != http.StatusUnauthorized {
		t.Errorf("ParseStatusCode = %d, want 401", core.ParseStatusCode(err))
	}
}
