package core

import (
	"context"
	"encoding/json"
)

// SendChunk sends c on ch unless ctx is done. It returns false when ctx was
// cancelled before the send completed, signalling the producer goroutine to
// stop and close its upstream response body. Streaming providers use it for
// every send so a direct consumer that stops reading after cancellation cannot
// block the producer forever and leak it along with the upstream connection.
func SendChunk(ctx context.Context, ch chan<- StreamChunk, c StreamChunk) bool {
	select {
	case ch <- c:
		return true
	case <-ctx.Done():
		return false
	}
}

// StreamChunk represents a single SSE chunk in a streaming response.
type StreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	// Usage is populated in the final chunk by providers that support streaming
	// usage reporting (e.g. OpenAI with stream_options.include_usage=true);
	// non-final chunks leave this nil so it is omitted from SSE payloads.
	Usage *Usage `json:"usage,omitempty"`
	Error error  `json:"-"` // non-nil signals a stream failure
}

// StreamChoice is a single choice in a streaming chunk.
type StreamChoice struct {
	Index        int          `json:"index"`
	Delta        MessageDelta `json:"delta"`
	FinishReason string       `json:"finish_reason,omitempty"`
}

// MessageDelta carries incremental content in a streaming response.
type MessageDelta struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ReasoningContent streams the model's chain-of-thought for reasoning
	// models (e.g. deepseek-reasoner). Empty for models that don't emit it.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// messageDeltaAlias strips the JSON methods from MessageDelta so UnmarshalJSON
// can embed it without recursing into itself.
type messageDeltaAlias MessageDelta

// UnmarshalJSON decodes a streaming delta, accepting either spelling of the
// reasoning field: reasoning_content, and the plain "reasoning" that Ollama's
// OpenAI-compatible endpoint sends. Without this the whole reasoning stream of
// such a provider is silently dropped. The gateway always re-emits
// reasoning_content, so only decoding is affected.
func (d *MessageDelta) UnmarshalJSON(data []byte) error {
	var raw struct {
		messageDeltaAlias
		Reasoning string `json:"reasoning"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*d = MessageDelta(raw.messageDeltaAlias)
	if d.ReasoningContent == "" {
		d.ReasoningContent = raw.Reasoning
	}
	return nil
}

// StreamNormalizer enforces the parts of the OpenAI streaming contract that a
// single chunk cannot satisfy on its own, because they are properties of the
// stream as a whole:
//
//   - every chunk carries the same id and model. Providers that report either
//     one only on an opening event leave it off later chunks; clients that
//     correlate chunks by id cannot follow those.
//   - delta.role appears exactly once per choice, on that choice's first chunk.
//     Providers disagree here — some repeat the role on every chunk, some never
//     send it — and a client that appends every delta verbatim ends up with a
//     message whose role was written many times or never.
//
// It carries values forward rather than inventing them: a stream whose provider
// never reports an id still has none. The zero value is ready to use, and one
// normalizer belongs to one stream.
type StreamNormalizer struct {
	id       string
	model    string
	roleSeen map[int]bool
}

// Normalize applies the stream-wide envelope invariants to chunk in place.
func (n *StreamNormalizer) Normalize(chunk *StreamChunk) {
	if chunk.ID != "" {
		n.id = chunk.ID
	} else {
		chunk.ID = n.id
	}
	if chunk.Model != "" {
		n.model = chunk.Model
	} else {
		chunk.Model = n.model
	}
	for i := range chunk.Choices {
		choice := &chunk.Choices[i]
		if n.roleSeen[choice.Index] {
			choice.Delta.Role = ""
			continue
		}
		if n.roleSeen == nil {
			n.roleSeen = make(map[int]bool, len(chunk.Choices))
		}
		n.roleSeen[choice.Index] = true
		if choice.Delta.Role == "" {
			choice.Delta.Role = RoleAssistant
		}
	}
}
