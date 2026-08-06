package azureopenai

import (
	"context"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// Transcribe transcribes (or, when req.Translate is set, translates to English)
// audio via Azure OpenAI's deployment-based audio route. Azure routes by the
// deployment name in the URL path and carries no "model" body field — so the
// deployment is resolved from req.Model (falling back to the configured one)
// and req.Model is cleared before the multipart body is built, matching what
// the Azure SDKs send. Auth is the api-key header, not Bearer.
func (p *Provider) Transcribe(ctx context.Context, req core.TranscriptionRequest) (*core.TranscriptionResponse, error) {
	op := "audio/transcriptions"
	if req.Translate {
		op = "audio/translations"
	}
	endpoint, err := p.opEndpoint(p.deploymentFor(req.Model), op)
	if err != nil {
		return nil, err
	}
	req.Model = ""
	return openaicompat.PostTranscription(ctx, openaicompat.TranscriptionParams{
		HTTPClient: p.httpClient,
		URL:        endpoint,
		Headers:    map[string]string{"api-key": p.apiKey},
		Label:      Name,
	}, req)
}
