package bedrock

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/internal/anthropicwire"
)

// CompleteStream sends a streaming request to AWS Bedrock via InvokeModelWithResponseStream.
// Currently only Anthropic Claude streaming is implemented. Each event chunk is
// decoded by the shared anthropicwire StreamDecoder — the same decoder the
// native Anthropic provider uses — so both paths report token usage from
// message_start / message_delta rather than dropping it.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	if !strings.HasPrefix(bedrockModelRoutingID(req.Model), "anthropic.") {
		return nil, fmt.Errorf("streaming on Bedrock is currently only supported for anthropic.claude-* models")
	}
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, bedrockSupportedParams(bedrockModelRoutingID(req.Model))...)

	anthropicReq, err := buildBedrockAnthropicRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	body, err := core.MarshalJSON(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	stream, err := p.client.InvokeModelWithResponseStream(ctx, &bedrockruntime.InvokeModelWithResponseStreamInput{
		ModelId:     aws.String(req.Model),
		ContentType: aws.String("application/json"),
		Body:        body,
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock streaming invoke failed: %w", err)
	}

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = stream.Close() }()

		// Chunks carry the request model ID; Bedrock's event stream reports
		// Anthropic's internal model name on message_start, which callers do not
		// use for Bedrock routing.
		dec := anthropicwire.NewStreamDecoder(Name, req.Model)
		for event := range stream.Events() {
			e, ok := event.(*types.ResponseStreamMemberChunk)
			if !ok {
				continue
			}
			chunks, evtErr := dec.Event(e.Value.Bytes)
			for _, c := range chunks {
				ch <- c
			}
			if evtErr != nil {
				ch <- core.StreamChunk{Error: evtErr}
				return
			}
		}
		if err := stream.Err(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}
