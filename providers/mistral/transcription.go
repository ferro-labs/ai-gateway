package mistral

import (
	"context"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// Transcribe transcribes audio via Mistral's OpenAI-compatible
// /audio/transcriptions endpoint (voxtral-mini-latest). Mistral has no
// translations endpoint, so a translate request is refused rather than routed
// to a path that does not exist. The default JSON response is richer than
// OpenAI's, but it carries a top-level "text" the shared decoder reads.
func (p *Provider) Transcribe(ctx context.Context, req core.TranscriptionRequest) (*core.TranscriptionResponse, error) {
	if req.Translate {
		return nil, core.StatusError(Name, http.StatusBadRequest, "mistral does not support audio translation; only transcription is available")
	}
	return openaicompat.PostTranscription(ctx, openaicompat.TranscriptionParams{
		HTTPClient: p.httpClient,
		URL:        p.baseURL + "/audio/transcriptions",
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey},
		Label:      Name,
	}, req)
}
