package groq

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// Speech synthesizes speech from text via Groq's OpenAI-compatible
// /audio/speech endpoint (playai-tts, orpheus), returning the raw audio bytes.
func (p *Provider) Speech(ctx context.Context, req core.SpeechRequest) (*core.SpeechResponse, error) {
	return openaicompat.PostSpeech(ctx, openaicompat.SpeechParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/audio/speech",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey},
		Label:      Name,
	}, req)
}
