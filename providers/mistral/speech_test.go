package mistral

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestMistralProvider_Speech_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.SpeechProvider = p
}

// Mistral returns audio base64-encoded inside a JSON envelope and selects the
// voice by voice_id, not OpenAI's preset name — the adapter must decode the
// base64 and forward the voice as voice_id.
func TestMistralProvider_Speech_MockHTTP(t *testing.T) {
	var body map[string]any
	audio := []byte("MISTRALAUDIO")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/audio/speech") {
			t.Errorf("path = %q, want suffix /audio/speech", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"audio_data":"` + base64.StdEncoding.EncodeToString(audio) + `"}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Speech(context.Background(), core.SpeechRequest{
		Model: "voxtral-mini-tts-2603", Input: "hello", Voice: "a3e8f2b1-voice-uuid", ResponseFormat: "mp3",
	})
	if err != nil {
		t.Fatalf("Speech() error: %v", err)
	}
	if string(resp.Audio) != "MISTRALAUDIO" {
		t.Errorf("Audio = %q, want MISTRALAUDIO (base64-decoded)", resp.Audio)
	}
	if resp.ContentType != "audio/mpeg" {
		t.Errorf("ContentType = %q, want audio/mpeg", resp.ContentType)
	}
	// The gateway 'voice' is forwarded as voice_id, and there is no speed field.
	if body["voice_id"] != "a3e8f2b1-voice-uuid" {
		t.Errorf("voice_id = %v, want a3e8f2b1-voice-uuid", body["voice_id"])
	}
	if _, present := body["voice"]; present {
		t.Errorf("body carried 'voice'=%v; Mistral uses voice_id", body["voice"])
	}
	if _, present := body["speed"]; present {
		t.Errorf("body carried speed; Mistral has no speed field")
	}
}

func TestMistralProvider_Speech_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Speech(context.Background(), core.SpeechRequest{Model: "voxtral-mini-tts-2603", Input: "hi", Voice: "v"})
	if err == nil {
		t.Fatal("Speech() error = nil, want 401")
	}
	if core.ParseStatusCode(err) != http.StatusUnauthorized {
		t.Errorf("ParseStatusCode = %d, want 401", core.ParseStatusCode(err))
	}
}
