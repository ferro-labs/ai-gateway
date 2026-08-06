package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// SpeechParams configures a request to an OpenAI-compatible /audio/speech
// endpoint (JSON request, raw-audio-bytes response).
type SpeechParams struct {
	HTTPClient *http.Client
	URL        string
	Headers    map[string]string // auth; the JSON Content-Type is set here
	Label      string
}

// SpeechContentType maps an OpenAI speech response_format to the media type the
// gateway serves the bytes as. The upstream's own Content-Type is not relied on
// (some send a generic octet-stream, or JSON around base64); the requested
// format is authoritative.
func SpeechContentType(format string) string {
	switch format {
	case "opus":
		return "audio/opus"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/pcm"
	case "", "mp3":
		return "audio/mpeg"
	default:
		return "application/octet-stream"
	}
}

// PostSpeech sends an OpenAI-compatible JSON speech request and returns the raw
// audio bytes with the media type implied by the requested response_format.
//
// An empty response_format is resolved to mp3 before the request is sent, so the
// format is always explicit on the wire. That matters beyond OpenAI's own mp3
// default: Together defaults to wav, so without this a client that omits the
// format would get wav bytes served under the audio/mpeg the default implies.
func PostSpeech(ctx context.Context, p SpeechParams, req core.SpeechRequest) (*core.SpeechResponse, error) {
	if req.ResponseFormat == "" {
		req.ResponseFormat = "mp3"
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal speech request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	for k, v := range p.Headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return nil, APIErrorFromResponse(p.Label, httpResp, respBody)
	}

	return &core.SpeechResponse{Audio: respBody, ContentType: SpeechContentType(req.ResponseFormat)}, nil
}
