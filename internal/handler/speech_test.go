package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
)

// TestSpeech_InputCap covers the request-validation half of the handler, which
// runs before any gateway routing: an over-length input must be refused at the
// boundary, because TTS is unpriced and nothing downstream bounds it.
func TestSpeech_InputCap(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantSubstr string
	}{
		{
			name: "input over 4096 characters is refused",
			body: `{"model":"tts-1","voice":"alloy","input":"` +
				strings.Repeat("a", maxSpeechInputChars+1) + `"}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "4096 characters",
		},
		{
			name:       "missing input is refused before the cap is consulted",
			body:       `{"model":"tts-1","voice":"alloy"}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "input is required",
		},
		{
			name: "multibyte input is counted in characters, not bytes",
			body: `{"model":"tts-1","voice":"alloy","input":"` +
				strings.Repeat("é", maxSpeechInputChars) + `"}`,
			// At the cap in runes (over it in bytes), so validation passes and the
			// request reaches routing, which 404s on a model no target serves —
			// anything but the 400 the cap produces proves the cap read runes.
			wantStatus: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := newTestGateway(t, config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeSingle},
				Targets:  []config.Target{{VirtualKey: "unused"}},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/audio/speech", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")

			Speech(gw)(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantSubstr == "" {
				return
			}
			var apiErr struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &apiErr); err != nil {
				t.Fatalf("response is not the OpenAI error envelope: %v (%s)", err, rec.Body.String())
			}
			if !strings.Contains(apiErr.Error.Message, tt.wantSubstr) {
				t.Errorf("error message %q does not mention %q", apiErr.Error.Message, tt.wantSubstr)
			}
		})
	}
}
