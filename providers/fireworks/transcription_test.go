package fireworks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestFireworksProvider_Transcribe_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.TranscriptionProvider = p
}

func TestFireworksProvider_Transcribe_MockHTTP(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if !strings.HasSuffix(r.URL.Path, "/v1/audio/transcriptions") {
			t.Errorf("path = %q, want suffix /v1/audio/transcriptions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		gotModel = r.FormValue("model")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello fireworks"}`))
	}))
	defer srv.Close()

	// The two whisper models are served from fixed vendor hosts; point both at
	// the test server so the multipart POST is exercised end to end.
	orig := fireworksAudioHosts
	fireworksAudioHosts = map[string]string{"whisper-v3": srv.URL, "whisper-v3-turbo": srv.URL}
	defer func() { fireworksAudioHosts = orig }()

	p, _ := New("test-key", "")
	resp, err := p.Transcribe(context.Background(), core.TranscriptionRequest{
		Model: "whisper-v3", File: []byte("AUDIODATA"), Filename: "clip.mp3",
	})
	if err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if resp.Text != "hello fireworks" {
		t.Errorf("Text = %q, want 'hello fireworks'", resp.Text)
	}
	if gotModel != "whisper-v3" {
		t.Errorf("model = %q, want whisper-v3", gotModel)
	}
}

// An unknown model has no audio host and must be refused locally before any HTTP
// call, since sending it to the wrong host is the adapter's responsibility.
func TestFireworksProvider_Transcribe_UnknownModelRefused(t *testing.T) {
	p, _ := New("test-key", "")
	_, err := p.Transcribe(context.Background(), core.TranscriptionRequest{
		Model: "whisper-1", File: []byte("x"), Filename: "a.mp3",
	})
	if err == nil {
		t.Fatal("Transcribe(unknown model) error = nil, want refusal")
	}
	if code := core.ParseStatusCode(err); code != http.StatusBadRequest {
		t.Errorf("ParseStatusCode = %d, want 400", code)
	}
}
