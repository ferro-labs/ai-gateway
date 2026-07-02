package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// CompleteStream sends a streaming request to AWS Bedrock via InvokeModelWithResponseStream.
// Currently only Anthropic Claude streaming is implemented.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	if !strings.HasPrefix(bedrockModelRoutingID(req.Model), "anthropic.") {
		return nil, fmt.Errorf("streaming on Bedrock is currently only supported for anthropic.claude-* models")
	}
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, bedrockSupportedParams(bedrockModelRoutingID(req.Model))...)

	anthropicReq, err := buildBedrockAnthropicRequest(req)
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

		toolCallIndexes := make(map[int]int)
		toolArgsSeen := make(map[int]bool)
		nextToolCallIndex := 0
		for event := range stream.Events() {
			if e, ok := event.(*types.ResponseStreamMemberChunk); ok {
				var eventType struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal(e.Value.Bytes, &eventType); err != nil {
					continue
				}

				switch eventType.Type {
				case "content_block_start":
					var start struct {
						Index        int `json:"index"`
						ContentBlock struct {
							Type string `json:"type"`
							ID   string `json:"id"`
							Name string `json:"name"`
						} `json:"content_block"`
					}
					if err := json.Unmarshal(e.Value.Bytes, &start); err != nil || start.ContentBlock.Type != "tool_use" {
						continue
					}
					toolCallIndex := nextToolCallIndex
					toolCallIndexes[start.Index] = toolCallIndex
					nextToolCallIndex++
					ch <- core.StreamChunk{
						Model: req.Model,
						Choices: []core.StreamChoice{{
							Index: 0,
							Delta: core.MessageDelta{
								ToolCalls: []core.ToolCall{{
									Index: core.Ptr(toolCallIndex),
									ID:    start.ContentBlock.ID,
									Type:  "function",
									Function: core.FunctionCall{
										Name: start.ContentBlock.Name,
									},
								}},
							},
						}},
					}
				case "content_block_delta":
					var delta struct {
						Index int `json:"index"`
						Delta struct {
							Type        string `json:"type"`
							Text        string `json:"text"`
							PartialJSON string `json:"partial_json"`
						} `json:"delta"`
					}
					if err := json.Unmarshal(e.Value.Bytes, &delta); err != nil {
						continue
					}
					if delta.Delta.Type == "input_json_delta" {
						toolCallIndex, ok := toolCallIndexes[delta.Index]
						if !ok {
							toolCallIndex = delta.Index
						}
						toolArgsSeen[toolCallIndex] = true
						ch <- core.StreamChunk{
							Model: req.Model,
							Choices: []core.StreamChoice{{
								Index: 0,
								Delta: core.MessageDelta{
									ToolCalls: []core.ToolCall{{
										Index: core.Ptr(toolCallIndex),
										Type:  "function",
										Function: core.FunctionCall{
											Arguments: delta.Delta.PartialJSON,
										},
									}},
								},
							}},
						}
						continue
					}
					if delta.Delta.Type != "text_delta" {
						continue
					}
					ch <- core.StreamChunk{
						Model: req.Model,
						Choices: []core.StreamChoice{{
							Index: 0,
							Delta: core.MessageDelta{Content: delta.Delta.Text},
						}},
					}
				case "message_delta":
					var delta struct {
						Delta struct {
							StopReason string `json:"stop_reason"`
						} `json:"delta"`
					}
					if err := json.Unmarshal(e.Value.Bytes, &delta); err != nil {
						continue
					}
					// Tool calls that never received an input_json_delta (zero-argument
					// tool calls) still need valid empty-object arguments emitted before
					// the finish chunk, matching the native Anthropic stream behavior.
					for toolCallIndex := 0; toolCallIndex < nextToolCallIndex; toolCallIndex++ {
						if toolArgsSeen[toolCallIndex] {
							continue
						}
						ch <- core.StreamChunk{
							Model: req.Model,
							Choices: []core.StreamChoice{{
								Index: 0,
								Delta: core.MessageDelta{
									ToolCalls: []core.ToolCall{{
										Index: core.Ptr(toolCallIndex),
										Type:  "function",
										Function: core.FunctionCall{
											Arguments: "{}",
										},
									}},
								},
							}},
						}
					}
					ch <- core.StreamChunk{
						Model: req.Model,
						Choices: []core.StreamChoice{{
							Index:        0,
							FinishReason: core.NormalizeFinishReason(delta.Delta.StopReason),
						}},
					}
				}
			}
		}
		if err := stream.Err(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}
