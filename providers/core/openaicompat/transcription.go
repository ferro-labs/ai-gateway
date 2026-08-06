package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TranscriptionParams configures a request to an OpenAI-compatible audio
// transcription/translation endpoint.
type TranscriptionParams struct {
	HTTPClient *http.Client
	URL        string            // full endpoint URL (transcriptions or translations)
	Headers    map[string]string // auth only; the multipart Content-Type is set here
	Label      string
}

// PostTranscription sends an OpenAI-compatible multipart audio request and
// decodes the transcript. The upstream returns {text} for json/verbose_json and
// the raw transcript for text/srt/vtt; both resolve to
// TranscriptionResponse.Text.
func PostTranscription(ctx context.Context, p TranscriptionParams, req core.TranscriptionRequest) (*core.TranscriptionResponse, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	filename := req.Filename
	if filename == "" {
		filename = "audio"
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("failed to build multipart file: %w", err)
	}
	if _, err := fw.Write(req.File); err != nil {
		return nil, fmt.Errorf("failed to write audio bytes: %w", err)
	}
	_ = mw.WriteField("model", req.Model)
	if req.Language != "" {
		_ = mw.WriteField("language", req.Language)
	}
	if req.Prompt != "" {
		_ = mw.WriteField("prompt", req.Prompt)
	}
	if req.ResponseFormat != "" {
		_ = mw.WriteField("response_format", req.ResponseFormat)
	}
	if req.Temperature != nil {
		_ = mw.WriteField("temperature", strconv.FormatFloat(*req.Temperature, 'f', -1, 64))
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize multipart body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, &buf)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	for k, v := range p.Headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())

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

	var decoded core.TranscriptionResponse
	switch req.ResponseFormat {
	case "text", "srt", "vtt":
		// These formats return the raw transcript, not a {text} envelope. The
		// transcript may itself be valid JSON (e.g. spoken "{}"), so it must be
		// taken verbatim rather than unmarshalled, which would discard it.
		decoded.Text = string(respBody)
	default:
		// json, verbose_json, and the empty/default case return a {text} envelope.
		if err := json.Unmarshal(respBody, &decoded); err != nil {
			decoded.Text = string(respBody)
		}
	}
	return &decoded, nil
}
