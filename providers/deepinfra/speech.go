package deepinfra

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// Speech synthesizes speech from text via DeepInfra's OpenAI-compatible
// /audio/speech endpoint (Kokoro, Orpheus, Chatterbox, …), returning the raw
// audio bytes. DeepInfra also exposes a native /v1/inference/{model} TTS route
// that wraps the audio in base64-in-JSON; this uses the OpenAI /audio/speech
// route on the same base, which streams raw bytes, so the shared helper applies.
func (p *Provider) Speech(ctx context.Context, req core.SpeechRequest) (*core.SpeechResponse, error) {
	return openaicompat.PostSpeech(ctx, openaicompat.SpeechParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/audio/speech",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey},
		Label:      Name,
	}, req)
}
