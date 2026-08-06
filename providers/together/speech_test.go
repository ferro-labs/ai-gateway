package together

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

func TestTogetherProvider_Speech_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.SpeechProvider = p
}

// Together's schema has no speed field, so a caller-supplied speed must be
// dropped rather than forwarded to a field the upstream rejects.
func TestTogetherProvider_Speech_MockHTTP_DropsSpeed(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/audio/speech") {
			t.Errorf("path = %q, want suffix /audio/speech", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("TOGETHERAUDIO"))
	}))
	defer srv.Close()

	speed := 1.5
	p, _ := New("test-key", srv.URL)
	resp, err := p.Speech(context.Background(), core.SpeechRequest{
		Model: "cartesia/sonic", Input: "hello", Voice: "tara", ResponseFormat: "mp3", Speed: &speed,
	})
	if err != nil {
		t.Fatalf("Speech() error: %v", err)
	}
	if string(resp.Audio) != "TOGETHERAUDIO" {
		t.Errorf("Audio = %q, want TOGETHERAUDIO", resp.Audio)
	}
	if _, present := body["speed"]; present {
		t.Errorf("body carried speed=%v; Together has no speed field, it must be dropped", body["speed"])
	}
	if body["voice"] != "tara" {
		t.Errorf("voice = %v, want tara", body["voice"])
	}
}
