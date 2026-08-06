package azureopenai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestAzureOpenAIProvider_Transcribe_Interface(_ *testing.T) {
	p, _ := New("test-key", "https://res.openai.azure.com", "whisper-dep", "")
	var _ core.TranscriptionProvider = p
}

func TestAzureOpenAIProvider_Transcribe_MockHTTP(t *testing.T) {
	var gotModelField, gotAPIVersion string
	sawModelField := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		// Azure routes by deployment in the path, not a model body field.
		if !strings.Contains(r.URL.Path, "/openai/deployments/whisper-dep/audio/transcriptions") {
			t.Errorf("path = %q, want .../deployments/whisper-dep/audio/transcriptions", r.URL.Path)
		}
		if got := r.Header.Get("api-key"); got != "test-key" {
			t.Errorf("api-key = %q, want test-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty (Azure uses api-key)", got)
		}
		gotAPIVersion = r.URL.Query().Get("api-version")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if vals, ok := r.MultipartForm.Value["model"]; ok {
			sawModelField = true
			if len(vals) > 0 {
				gotModelField = vals[0]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello azure"}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL, "whisper-dep", "2024-10-21")
	resp, err := p.Transcribe(context.Background(), core.TranscriptionRequest{
		File: []byte("AUDIODATA"), Filename: "clip.mp3",
	})
	if err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if resp.Text != "hello azure" {
		t.Errorf("Text = %q, want 'hello azure'", resp.Text)
	}
	if gotAPIVersion != "2024-10-21" {
		t.Errorf("api-version = %q, want 2024-10-21", gotAPIVersion)
	}
	// The body must carry no meaningful model — the deployment is in the URL.
	if sawModelField && gotModelField != "" {
		t.Errorf("model field = %q, want empty (deployment is in the URL path)", gotModelField)
	}
}

// A request may target a different transcription deployment via req.Model.
func TestAzureOpenAIProvider_Transcribe_DeploymentOverride(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL, "default-dep", "2024-10-21")
	if _, err := p.Transcribe(context.Background(), core.TranscriptionRequest{
		Model: "other-whisper", File: []byte("x"), Filename: "a.mp3",
	}); err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if !strings.Contains(gotPath, "/openai/deployments/other-whisper/audio/transcriptions") {
		t.Errorf("path = %q, want deployment other-whisper", gotPath)
	}
}

func TestAzureOpenAIProvider_Transcribe_TranslatePath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"translated"}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL, "whisper-dep", "2024-10-21")
	if _, err := p.Transcribe(context.Background(), core.TranscriptionRequest{
		File: []byte("x"), Filename: "a.mp3", Translate: true,
	}); err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if !strings.HasSuffix(strings.Split(gotPath, "?")[0], "/audio/translations") {
		t.Errorf("path = %q, want suffix /audio/translations (translate=true)", gotPath)
	}
}
