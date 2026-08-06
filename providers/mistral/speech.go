package mistral

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/openaicompat"
)

// mistralSpeechBody is Mistral's /v1/audio/speech request. Voice selection is a
// voice_id (a UUID of a voice profile created via /v1/audio/voices), not
// OpenAI's preset voice names, and there is no speed field.
type mistralSpeechBody struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	VoiceID        string `json:"voice_id,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
}

// Speech synthesizes speech from text via Mistral's /v1/audio/speech endpoint
// (voxtral TTS). Unlike the OpenAI contract, Mistral returns the audio
// base64-encoded inside a JSON envelope ({"audio_data": "..."}) rather than as
// raw bytes, and selects the voice by a voice_id UUID rather than a preset name,
// so this is a custom adapter rather than the shared PostSpeech.
func (p *Provider) Speech(ctx context.Context, req core.SpeechRequest) (*core.SpeechResponse, error) {
	format := req.ResponseFormat
	if format == "" {
		format = "mp3"
	}
	body, err := json.Marshal(mistralSpeechBody{
		Model:          req.Model,
		Input:          req.Input,
		VoiceID:        req.Voice,
		ResponseFormat: format,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal speech request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return nil, openaicompat.APIErrorFromResponse(Name, httpResp, respBody)
	}

	var decoded struct {
		AudioData string `json:"audio_data"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("mistral: failed to decode speech response: %w", err)
	}
	audio, err := base64.StdEncoding.DecodeString(decoded.AudioData)
	if err != nil {
		return nil, fmt.Errorf("mistral: failed to base64-decode audio_data: %w", err)
	}
	return &core.SpeechResponse{Audio: audio, ContentType: openaicompat.SpeechContentType(format)}, nil
}
