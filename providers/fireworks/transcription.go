package fireworks

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// fireworksAudioHosts maps a whisper model to the dedicated serverless audio
// host Fireworks documents for it. Audio does NOT live on the inference root
// (api.fireworks.ai/inference/v1); each model is served from its own host under
// a plain /v1 prefix.
//
// the two public hosts are hardcoded from the vendor's fixed audio
// topology (same as LiteLLM/Portkey). A FIREWORKS_BASE_URL override points the
// inference API at a proxy; it does not redirect audio. Add a
// FIREWORKS_AUDIO_BASE_URL if a proxied deployment ever needs it.
var fireworksAudioHosts = map[string]string{
	"whisper-v3":       "https://audio-prod.api.fireworks.ai",
	"whisper-v3-turbo": "https://audio-turbo.api.fireworks.ai",
}

// Transcribe transcribes (or, when req.Translate is set, translates to English)
// audio via Fireworks' dedicated audio hosts. The model selects the host, so
// only the two documented whisper ids are routable.
func (p *Provider) Transcribe(ctx context.Context, req core.TranscriptionRequest) (*core.TranscriptionResponse, error) {
	host, ok := fireworksAudioHosts[req.Model]
	if !ok {
		return nil, core.StatusError(Name, http.StatusBadRequest,
			fmt.Sprintf("fireworks audio serves only whisper-v3 and whisper-v3-turbo; got %q", req.Model))
	}
	path := "/v1/audio/transcriptions"
	if req.Translate {
		path = "/v1/audio/translations"
	}
	return openaicompat.PostTranscription(ctx, openaicompat.TranscriptionParams{
		HTTPClient: p.httpClient,
		URL:        host + path,
		Headers:    map[string]string{"Authorization": "Bearer " + p.apiKey},
		Label:      Name,
	}, req)
}
