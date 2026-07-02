package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

type anthropicStreamMessageStart struct {
	Type    string `json:"type"`
	Message struct {
		ID    string         `json:"id"`
		Model string         `json:"model"`
		Role  string         `json:"role"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
}

type anthropicStreamContentDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
}

type anthropicStreamContentBlockStart struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
}

type anthropicStreamMessageDelta struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage anthropicUsage `json:"usage"`
}

// anthropicStreamError is the payload of a mid-stream "event: error" frame,
// e.g. {"type":"error","error":{"type":"overloaded_error","message":"..."}}.
type anthropicStreamError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// CompleteStream sends a streaming chat completion request to Anthropic.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, anthropicSupportedParams...)

	aReq := buildAnthropicRequest(req, true)

	httpResp, release, err := p.newMessagesRequest(ctx, aReq)
	if err != nil {
		return nil, err
	}
	defer release()

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, core.APIError("anthropic", httpResp.StatusCode, respBody)
	}

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = httpResp.Body.Close() }()

		var msgID, model string
		var promptTokens, cacheReadTokens, cacheWriteTokens int
		toolCallIndexes := make(map[int]int)
		toolArgsSeen := make(map[int]bool) // tool-call index -> received any input_json_delta
		nextToolCallIndex := 0
		lines, scanErr := core.SSEDataLines(httpResp.Body)
		for data := range lines {

			var raw map[string]any
			if json.Unmarshal([]byte(data), &raw) != nil {
				continue
			}

			eventType, _ := raw["type"].(string)
			switch eventType {
			case "error":
				var evt anthropicStreamError
				if json.Unmarshal([]byte(data), &evt) == nil && evt.Error.Message != "" {
					ch <- core.StreamChunk{Error: fmt.Errorf("anthropic stream error (%s): %s", evt.Error.Type, evt.Error.Message)}
				} else {
					ch <- core.StreamChunk{Error: fmt.Errorf("anthropic stream error: %s", data)}
				}
				return
			case "message_start":
				var evt anthropicStreamMessageStart
				if json.Unmarshal([]byte(data), &evt) == nil {
					msgID = evt.Message.ID
					model = evt.Message.Model
					// Anthropic reports prompt + cache tokens once, on
					// message_start; output_tokens arrive later on message_delta.
					promptTokens = evt.Message.Usage.InputTokens
					cacheReadTokens = evt.Message.Usage.CacheReadInputTokens
					cacheWriteTokens = evt.Message.Usage.CacheCreationInputTokens
				}
			case "content_block_start":
				var evt anthropicStreamContentBlockStart
				if json.Unmarshal([]byte(data), &evt) == nil && evt.ContentBlock.Type == blockTypeToolUse {
					toolCallIndex := nextToolCallIndex
					toolCallIndexes[evt.Index] = toolCallIndex
					nextToolCallIndex++
					ch <- core.StreamChunk{
						ID:    msgID,
						Model: model,
						Choices: []core.StreamChoice{
							{
								Index: 0,
								Delta: core.MessageDelta{
									ToolCalls: []core.ToolCall{
										{
											Index: core.Ptr(toolCallIndex),
											ID:    evt.ContentBlock.ID,
											Type:  "function",
											Function: core.FunctionCall{
												Name: evt.ContentBlock.Name,
											},
										},
									},
								},
							},
						},
					}
				}
			case "content_block_delta":
				var evt anthropicStreamContentDelta
				if json.Unmarshal([]byte(data), &evt) == nil {
					if evt.Delta.Type == "input_json_delta" {
						toolCallIndex, ok := toolCallIndexes[evt.Index]
						if !ok {
							toolCallIndex = evt.Index
						}
						toolArgsSeen[toolCallIndex] = true
						ch <- core.StreamChunk{
							ID:    msgID,
							Model: model,
							Choices: []core.StreamChoice{
								{
									Index: 0,
									Delta: core.MessageDelta{
										ToolCalls: []core.ToolCall{
											{
												Index: core.Ptr(toolCallIndex),
												Type:  "function",
												Function: core.FunctionCall{
													Arguments: evt.Delta.PartialJSON,
												},
											},
										},
									},
								},
							},
						}
						continue
					}
					if evt.Delta.Type != "text_delta" {
						continue
					}
					ch <- core.StreamChunk{
						ID:    msgID,
						Model: model,
						Choices: []core.StreamChoice{
							{
								// Single completion: the OpenAI choice index is
								// always 0 (evt.Index is Anthropic's content-block
								// index, not a choice index).
								Index: 0,
								Delta: core.MessageDelta{
									Content: evt.Delta.Text,
								},
							},
						},
					}
				}
			case "message_delta":
				// Emit "{}" arguments for any tool call that produced no
				// input_json_delta (zero-argument tools), so clients that
				// JSON.parse the arguments don't choke on an empty string.
				for _, toolCallIndex := range toolCallIndexes {
					if toolArgsSeen[toolCallIndex] {
						continue
					}
					ch <- core.StreamChunk{
						ID:    msgID,
						Model: model,
						Choices: []core.StreamChoice{{
							Index: 0,
							Delta: core.MessageDelta{
								ToolCalls: []core.ToolCall{{
									Index:    core.Ptr(toolCallIndex),
									Type:     "function",
									Function: core.FunctionCall{Arguments: "{}"},
								}},
							},
						}},
					}
				}
				var evt anthropicStreamMessageDelta
				_ = json.Unmarshal([]byte(data), &evt)
				completionTokens := evt.Usage.OutputTokens
				ch <- core.StreamChunk{
					ID:    msgID,
					Model: model,
					Choices: []core.StreamChoice{
						{
							Index:        0,
							FinishReason: core.NormalizeFinishReason(evt.Delta.StopReason),
						},
					},
					Usage: &core.Usage{
						PromptTokens:     promptTokens,
						CompletionTokens: completionTokens,
						TotalTokens:      promptTokens + completionTokens,
						CacheReadTokens:  cacheReadTokens,
						CacheWriteTokens: cacheWriteTokens,
					},
				}
			}
		}
		if err := scanErr(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}
