package deepinfra

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// Transcribe transcribes (or, when req.Translate is set, translates to English)
// audio via DeepInfra's OpenAI-compatible /audio/transcriptions and
// /audio/translations endpoints. Model ids are namespaced
// (e.g. openai/whisper-large-v3).
func (p *Provider) Transcribe(ctx context.Context, req core.TranscriptionRequest) (*core.TranscriptionResponse, error) {
	path := "/audio/transcriptions"
	if req.Translate {
		path = "/audio/translations"
	}
	return openaicompat.PostTranscription(ctx, openaicompat.TranscriptionParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + path,
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey},
		Label:      Name,
	}, req)
}
