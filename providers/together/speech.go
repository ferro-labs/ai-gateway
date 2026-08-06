package together

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// Speech synthesizes speech from text via Together's OpenAI-compatible
// /audio/speech endpoint (Orpheus, Kokoro, Cartesia Sonic), returning the raw
// audio bytes. Voice ids are model-specific. Together's schema has no speed
// parameter, so it is dropped rather than forwarded to a field the upstream
// rejects.
func (p *Provider) Speech(ctx context.Context, req core.SpeechRequest) (*core.SpeechResponse, error) {
	req.Speed = nil
	return openaicompat.PostSpeech(ctx, openaicompat.SpeechParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/audio/speech",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey},
		Label:      Name,
	}, req)
}
