package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/ferro-labs/ai-gateway/providers"
)

type routeChatCompletionRequest struct {
	Model               string                    `json:"model"`
	Messages            []routeChatMessage        `json:"messages"`
	Temperature         *float64                  `json:"temperature,omitempty"`
	TopP                *float64                  `json:"top_p,omitempty"`
	N                   *int                      `json:"n,omitempty"`
	Seed                *int64                    `json:"seed,omitempty"`
	MaxTokens           *int                      `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                      `json:"max_completion_tokens,omitempty"`
	PresencePenalty     *float64                  `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64                  `json:"frequency_penalty,omitempty"`
	Stop                []string                  `json:"stop,omitempty"`
	Tools               []providers.Tool          `json:"tools,omitempty"`
	ToolChoice          json.RawMessage           `json:"tool_choice,omitempty"`
	ResponseFormat      *providers.ResponseFormat `json:"response_format,omitempty"`
	LogProbs            bool                      `json:"logprobs,omitempty"`
	TopLogProbs         *int                      `json:"top_logprobs,omitempty"`
	Stream              bool                      `json:"stream,omitempty"`
	User                string                    `json:"user,omitempty"`
	LogitBias           map[string]float64        `json:"logit_bias,omitempty"`
}

type routeChatMessage struct {
	Role       string               `json:"role"`
	Content    json.RawMessage      `json:"content"`
	Name       string               `json:"name,omitempty"`
	ToolCalls  []providers.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

// chatRequestPool recycles routeChatCompletionRequest objects to reduce GC
// pressure. Every chat completion request through the gateway allocates one
// of these — pooling eliminates that allocation from the hot path entirely.
var chatRequestPool = sync.Pool{
	New: func() interface{} {
		return &routeChatCompletionRequest{}
	},
}

func getRouteChatCompletionRequest() *routeChatCompletionRequest {
	return chatRequestPool.Get().(*routeChatCompletionRequest)
}

func putRouteChatCompletionRequest(r *routeChatCompletionRequest) {
	r.reset()
	chatRequestPool.Put(r)
}

// reset clears all 19 fields before returning to the pool.
// SECURITY: every field must be listed explicitly. Missing a field
// leaks one tenant's data to another in the multi-tenant gateway.
func (r *routeChatCompletionRequest) reset() {
	r.Model = ""                // field 1:  string
	r.Messages = nil            // field 2:  []routeChatMessage
	r.Temperature = nil         // field 3:  *float64
	r.TopP = nil                // field 4:  *float64
	r.N = nil                   // field 5:  *int
	r.Seed = nil                // field 6:  *int64
	r.MaxTokens = nil           // field 7:  *int
	r.MaxCompletionTokens = nil // field 8:  *int
	r.PresencePenalty = nil     // field 9:  *float64
	r.FrequencyPenalty = nil    // field 10: *float64
	r.Stop = nil                // field 11: []string
	r.Tools = nil               // field 12: []providers.Tool
	r.ToolChoice = nil          // field 13: json.RawMessage ([]byte)
	r.ResponseFormat = nil      // field 14: *providers.ResponseFormat
	r.LogProbs = false          // field 15: bool
	r.TopLogProbs = nil         // field 16: *int
	r.Stream = false            // field 17: bool
	r.User = ""                 // field 18: string
	r.LogitBias = nil           // field 19: map[string]float64
}

func decodeChatCompletionRequest(r io.Reader) (providers.Request, error) {
	wire := getRouteChatCompletionRequest()
	defer putRouteChatCompletionRequest(wire)
	if err := json.NewDecoder(r).Decode(wire); err != nil {
		return providers.Request{}, err
	}

	messages := make([]providers.Message, len(wire.Messages))
	for i, msg := range wire.Messages {
		decoded, err := msg.toProviderMessage()
		if err != nil {
			return providers.Request{}, fmt.Errorf("messages[%d]: %w", i, err)
		}
		messages[i] = decoded
	}

	var toolChoice interface{}
	if len(wire.ToolChoice) > 0 && !rawJSONNull(wire.ToolChoice) {
		if err := json.Unmarshal(wire.ToolChoice, &toolChoice); err != nil {
			return providers.Request{}, fmt.Errorf("tool_choice: %w", err)
		}
	}

	return providers.Request{
		Model:               wire.Model,
		Messages:            messages,
		Temperature:         wire.Temperature,
		TopP:                wire.TopP,
		N:                   wire.N,
		Seed:                wire.Seed,
		MaxTokens:           wire.MaxTokens,
		MaxCompletionTokens: wire.MaxCompletionTokens,
		PresencePenalty:     wire.PresencePenalty,
		FrequencyPenalty:    wire.FrequencyPenalty,
		Stop:                wire.Stop,
		Tools:               wire.Tools,
		ToolChoice:          toolChoice,
		ResponseFormat:      wire.ResponseFormat,
		LogProbs:            wire.LogProbs,
		TopLogProbs:         wire.TopLogProbs,
		Stream:              wire.Stream,
		User:                wire.User,
		LogitBias:           wire.LogitBias,
	}, nil
}

func (m routeChatMessage) toProviderMessage() (providers.Message, error) {
	msg := providers.Message{
		Role:       m.Role,
		Name:       m.Name,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	}
	if len(m.Content) == 0 || rawJSONNull(m.Content) {
		return msg, nil
	}

	if m.Content[0] == '"' {
		if err := json.Unmarshal(m.Content, &msg.Content); err != nil {
			return providers.Message{}, err
		}
		return msg, nil
	}

	var parts []providers.ContentPart
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return providers.Message{}, err
	}
	msg.ContentParts = parts
	for _, part := range parts {
		if part.Type == providers.ContentTypeText {
			msg.Content += part.Text
		}
	}
	return msg, nil
}

func rawJSONNull(raw []byte) bool {
	return len(raw) == 4 && raw[0] == 'n' && raw[1] == 'u' && raw[2] == 'l' && raw[3] == 'l'
}
