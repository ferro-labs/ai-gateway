package main

import (
	"encoding/json"
	"fmt"
	"io"

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

func decodeChatCompletionRequest(r io.Reader) (providers.Request, error) {
	var wire routeChatCompletionRequest
	if err := json.NewDecoder(r).Decode(&wire); err != nil {
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
