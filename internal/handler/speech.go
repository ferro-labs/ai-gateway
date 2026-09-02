package handler

import (
	"fmt"
	"net/http"
	"unicode/utf8"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/providers"
)

// maxSpeechInputChars is the ceiling on a TTS input's length, in characters —
// the OpenAI contract's own limit for this surface. Enforced here at the trust
// boundary because TTS is unpriced (no usage comes back to bill), so no budget
// guardrail bounds what would otherwise ride the whole JSON body limit upstream.
const maxSpeechInputChars = 4096

// Speech handles POST /v1/audio/speech (text-to-speech).
// It routes to the first registered SpeechProvider that serves the requested
// model and streams the synthesized audio bytes back with the media type the
// requested response_format implies.
func Speech(gw *aigateway.Gateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req providers.SpeechRequest
		if !decodeJSONBody(w, r, &req) {
			return
		}
		if req.Model == "" {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_request")
			return
		}
		if req.Input == "" {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "input is required", "invalid_request_error", "invalid_request")
			return
		}
		if req.Voice == "" {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "voice is required", "invalid_request_error", "invalid_request")
			return
		}
		if n := utf8.RuneCountInString(req.Input); n > maxSpeechInputChars {
			apierror.WriteOpenAI(w, http.StatusBadRequest,
				fmt.Sprintf("input must be at most %d characters, got %d", maxSpeechInputChars, n),
				"invalid_request_error", "invalid_request")
			return
		}

		attribution := &aigateway.RoutingAttribution{}
		ctx := aigateway.WithRoutingAttribution(r.Context(), attribution)
		resp, err := gw.Speech(ctx, req)
		attribution.SetHeaders(w.Header())
		if err != nil {
			apierror.WriteRouteError(w, err)
			return
		}

		w.Header().Set("Content-Type", resp.ContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.Audio)
	}
}
