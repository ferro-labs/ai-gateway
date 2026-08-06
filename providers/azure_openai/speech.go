package azureopenai

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// Speech synthesizes speech from text via Azure OpenAI's deployment-based
// /audio/speech route, returning the raw audio bytes. As with transcription the
// deployment is resolved from req.Model (falling back to the configured one) and
// carried in the URL path, auth is the api-key header, and the body carries no
// model field.
//
// Azure serves speech only on a preview api-version (2025-04-01-preview); the GA
// version has no /audio/speech, so the configured AZURE_OPENAI_API_VERSION must
// name a speech-capable preview for this surface.
func (p *Provider) Speech(ctx context.Context, req core.SpeechRequest) (*core.SpeechResponse, error) {
	endpoint, err := p.opEndpoint(p.deploymentFor(req.Model), "audio/speech")
	if err != nil {
		return nil, err
	}
	req.Model = ""
	return openaicompat.PostSpeech(ctx, openaicompat.SpeechParams{
		HTTPClient: p.httpClient,
		URL:        endpoint,
		Headers:    map[string]string{"api-key": p.apiKey},
		Label:      Name,
	}, req)
}
