package anthropic

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/anthropicwire"
)

// CompleteStream sends a streaming chat completion request to Anthropic. The
// Anthropic Messages event stream is decoded by the shared anthropicwire
// StreamDecoder, which is also driven by the Anthropic-on-Bedrock path so the
// two providers cannot drift.
//
// It reuses the non-streaming HTTP client. That client's ResponseHeaderTimeout
// bounds time-to-first-byte (headers), not the duration of the streamed body, so
// it is safe for streaming: a slow model still streams tokens past the timeout.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, anthropicSupportedParams...)

	aReq := buildAnthropicRequest(ctx, req, true)

	httpResp, release, err := p.newMessagesRequest(ctx, aReq)
	if err != nil {
		return nil, err
	}
	defer release()

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}
		return nil, core.APIError("anthropic", httpResp.StatusCode, respBody)
	}

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		dec := anthropicwire.NewStreamDecoder("anthropic", "")
		lines, scanErr := core.SSEDataLines(httpResp.Body)
		for data := range lines {
			chunks, evtErr := dec.Event([]byte(data))
			for _, c := range chunks {
				if !core.SendChunk(ctx, ch, c) {
					return
				}
			}
			if evtErr != nil {
				core.SendChunk(ctx, ch, core.StreamChunk{Error: evtErr})
				return
			}
		}
		if err := scanErr(); err != nil {
			core.SendChunk(ctx, ch, core.StreamChunk{Error: err})
		}
	}()

	return ch, nil
}
