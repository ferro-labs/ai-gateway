package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestPostTranscription_FormatFidelity asserts the response format decides how
// the body is read: text/srt/vtt are taken verbatim (so a transcript that is
// itself valid JSON survives), while json/verbose_json/default unmarshal the
// {text} envelope.
func TestPostTranscription_FormatFidelity(t *testing.T) {
	tests := []struct {
		name           string
		responseFormat string
		body           string
		wantText       string
	}{
		{
			name:           "text format with JSON-shaped transcript is kept verbatim",
			responseFormat: "text",
			body:           "{}",
			wantText:       "{}",
		},
		{
			name:           "text format with plain-text transcript",
			responseFormat: "text",
			body:           "hello there",
			wantText:       "hello there",
		},
		{
			name:           "json format decodes the text envelope",
			responseFormat: "json",
			body:           `{"text":"hello"}`,
			wantText:       "hello",
		},
		{
			name:           "default format decodes the text envelope",
			responseFormat: "",
			body:           `{"text":"hello"}`,
			wantText:       "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			p := TranscriptionParams{
				HTTPClient: srv.Client(),
				URL:        srv.URL,
				Headers:    map[string]string{"Authorization": "Bearer test-key"},
				Label:      "testprov",
			}
			req := core.TranscriptionRequest{
				Model:          "whisper-1",
				File:           []byte("audio"),
				ResponseFormat: tt.responseFormat,
			}

			resp, err := PostTranscription(context.Background(), p, req)
			if err != nil {
				t.Fatalf("PostTranscription: %v", err)
			}
			if resp.Text != tt.wantText {
				t.Fatalf("Text = %q, want %q", resp.Text, tt.wantText)
			}
		})
	}
}
