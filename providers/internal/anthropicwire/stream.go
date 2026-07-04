package anthropicwire

import (
	"encoding/json"
	"fmt"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// StreamDecoder converts a sequence of Anthropic Messages streaming events
// (message_start, content_block_start, content_block_delta, message_delta and
// mid-stream error frames) into canonical core.StreamChunk values.
//
// It is transport-agnostic: the standalone Anthropic provider feeds it SSE
// "data:" payloads and the Anthropic-on-Bedrock path feeds it
// InvokeModelWithResponseStream chunk bytes. Both wire the same event schema, so
// centralising the decode here removes the copy-paste between the two providers
// — and, in doing so, gives the Bedrock path the token-usage capture it
// previously dropped. A StreamDecoder is stateful and NOT safe for concurrent
// use; construct one per stream with NewStreamDecoder.
type StreamDecoder struct {
	label         string // provider name used in mid-stream error messages
	fallbackModel string // emitted when the stream carries no message_start model

	msgID string
	model string

	promptTokens     int
	cacheReadTokens  int
	cacheWriteTokens int

	toolCallIndexes   map[int]int  // Anthropic content-block index -> OpenAI tool-call index
	toolArgsSeen      map[int]bool // OpenAI tool-call index -> received any input_json_delta
	nextToolCallIndex int
}

// NewStreamDecoder returns a StreamDecoder. label prefixes mid-stream error
// messages (e.g. "anthropic"). fallbackModel is stamped on chunks when the
// caller wants the request's model reported (Bedrock's event stream); pass ""
// to use the model the upstream reports on message_start.
func NewStreamDecoder(label, fallbackModel string) *StreamDecoder {
	return &StreamDecoder{
		label:           label,
		fallbackModel:   fallbackModel,
		toolCallIndexes: make(map[int]int),
		toolArgsSeen:    make(map[int]bool),
	}
}

// chunkModel returns the model to stamp on emitted chunks: the caller-supplied
// fallback when set, otherwise the model reported by message_start.
func (d *StreamDecoder) chunkModel() string {
	if d.fallbackModel != "" {
		return d.fallbackModel
	}
	return d.model
}

// Event decodes one raw Anthropic streaming event and returns the chunks to emit
// (possibly none). A mid-stream "error" frame returns a non-nil error; the
// caller should surface it on the channel and stop reading — no further events
// are processed after an error. Frames that fail to decode yield no chunks and
// no error (they are skipped, matching the prior per-provider handling of
// keep-alives and unknown event types).
func (d *StreamDecoder) Event(data []byte) ([]core.StreamChunk, error) {
	var typed struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &typed) != nil {
		return nil, nil
	}

	switch typed.Type {
	case "error":
		return nil, d.errorEvent(data)
	case "message_start":
		d.messageStart(data)
		return nil, nil
	case "content_block_start":
		return d.contentBlockStart(data), nil
	case "content_block_delta":
		return d.contentBlockDelta(data), nil
	case "message_delta":
		return d.messageDelta(data), nil
	default:
		return nil, nil
	}
}

func (d *StreamDecoder) errorEvent(data []byte) error {
	var evt struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &evt) == nil && evt.Error.Message != "" {
		return fmt.Errorf("%s stream error (%s): %s", d.label, evt.Error.Type, evt.Error.Message)
	}
	return fmt.Errorf("%s stream error: %s", d.label, data)
}

func (d *StreamDecoder) messageStart(data []byte) {
	var evt struct {
		Message struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage Usage  `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &evt) != nil {
		return
	}
	d.msgID = evt.Message.ID
	d.model = evt.Message.Model
	// Anthropic reports prompt + cache tokens once, on message_start;
	// output_tokens arrive later on message_delta.
	d.promptTokens = evt.Message.Usage.InputTokens
	d.cacheReadTokens = evt.Message.Usage.CacheReadInputTokens
	d.cacheWriteTokens = evt.Message.Usage.CacheCreationInputTokens
}

func (d *StreamDecoder) contentBlockStart(data []byte) []core.StreamChunk {
	var evt struct {
		Index        int `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
	}
	if json.Unmarshal(data, &evt) != nil || evt.ContentBlock.Type != BlockTypeToolUse {
		return nil
	}
	toolCallIndex := d.nextToolCallIndex
	d.toolCallIndexes[evt.Index] = toolCallIndex
	d.nextToolCallIndex++
	return []core.StreamChunk{d.toolChunk(core.ToolCall{
		Index:    core.Ptr(toolCallIndex),
		ID:       evt.ContentBlock.ID,
		Type:     "function",
		Function: core.FunctionCall{Name: evt.ContentBlock.Name},
	})}
}

func (d *StreamDecoder) contentBlockDelta(data []byte) []core.StreamChunk {
	var evt struct {
		Index int `json:"index"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if json.Unmarshal(data, &evt) != nil {
		return nil
	}
	switch evt.Delta.Type {
	case "input_json_delta":
		// evt.Index is Anthropic's content-block index; map it to the OpenAI
		// tool-call index assigned at content_block_start.
		toolCallIndex, ok := d.toolCallIndexes[evt.Index]
		if !ok {
			toolCallIndex = evt.Index
		}
		d.toolArgsSeen[toolCallIndex] = true
		return []core.StreamChunk{d.toolChunk(core.ToolCall{
			Index:    core.Ptr(toolCallIndex),
			Type:     "function",
			Function: core.FunctionCall{Arguments: evt.Delta.PartialJSON},
		})}
	case "text_delta":
		return []core.StreamChunk{{
			ID:    d.msgID,
			Model: d.chunkModel(),
			// Single completion: the OpenAI choice index is always 0 (evt.Index
			// is Anthropic's content-block index, not a choice index).
			Choices: []core.StreamChoice{{
				Index: 0,
				Delta: core.MessageDelta{Content: evt.Delta.Text},
			}},
		}}
	default:
		return nil
	}
}

func (d *StreamDecoder) messageDelta(data []byte) []core.StreamChunk {
	var chunks []core.StreamChunk
	// Emit "{}" arguments for any tool call that produced no input_json_delta
	// (zero-argument tools) so clients that JSON.parse the arguments don't choke
	// on an empty string. Iterate in assignment order for a deterministic stream.
	for i := 0; i < d.nextToolCallIndex; i++ {
		if d.toolArgsSeen[i] {
			continue
		}
		chunks = append(chunks, d.toolChunk(core.ToolCall{
			Index:    core.Ptr(i),
			Type:     "function",
			Function: core.FunctionCall{Arguments: "{}"},
		}))
	}

	var evt struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage Usage `json:"usage"`
	}
	_ = json.Unmarshal(data, &evt)
	completionTokens := evt.Usage.OutputTokens
	chunks = append(chunks, core.StreamChunk{
		ID:    d.msgID,
		Model: d.chunkModel(),
		Choices: []core.StreamChoice{{
			Index:        0,
			FinishReason: core.NormalizeFinishReason(evt.Delta.StopReason),
		}},
		Usage: &core.Usage{
			PromptTokens:     d.promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      d.promptTokens + completionTokens,
			CacheReadTokens:  d.cacheReadTokens,
			CacheWriteTokens: d.cacheWriteTokens,
		},
	})
	return chunks
}

// toolChunk wraps a single tool-call delta in a StreamChunk stamped with the
// current message ID and model.
func (d *StreamDecoder) toolChunk(tc core.ToolCall) core.StreamChunk {
	return core.StreamChunk{
		ID:    d.msgID,
		Model: d.chunkModel(),
		Choices: []core.StreamChoice{{
			Index: 0,
			Delta: core.MessageDelta{ToolCalls: []core.ToolCall{tc}},
		}},
	}
}
