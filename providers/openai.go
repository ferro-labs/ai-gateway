package providers

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// OpenAIProvider implements the Provider interface for OpenAI.
type OpenAIProvider struct {
	Base
	client openai.Client
}

// NewOpenAI creates a new OpenAI provider. The optional baseURL parameter
// allows overriding the API endpoint (pass "" for the default).
func NewOpenAI(apiKey string, baseURL string) (*OpenAIProvider, error) {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	resolvedBase := "https://api.openai.com"
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
		resolvedBase = baseURL
	}
	client := openai.NewClient(opts...)
	return &OpenAIProvider{
		Base:   Base{name: "openai", apiKey: apiKey, baseURL: resolvedBase},
		client: client,
	}, nil
}

// AuthHeaders implements ProxiableProvider.
func (p *OpenAIProvider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns the list of models supported by this provider.
// For now, we return a static list, but this could be dynamic.
func (p *OpenAIProvider) SupportedModels() []string {
	return []string{
		"gpt-4o",
		"gpt-4-turbo",
		"gpt-4",
		"gpt-3.5-turbo",
	}
}

// SupportsModel returns true if the model matches known OpenAI prefixes.
func (p *OpenAIProvider) SupportsModel(model string) bool {
	for _, prefix := range []string{"gpt-", "chatgpt-", "dall-e-", "whisper-", "tts-", "text-embedding-", "ft:", "babbage-", "davinci-"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	if len(model) >= 2 && model[0] == 'o' && model[1] >= '0' && model[1] <= '9' {
		return true
	}
	return false
}

// Models returns model information for all supported models.
func (p *OpenAIProvider) Models() []ModelInfo {
	return ModelsFromList(p.name, p.SupportedModels())
}

// Embed sends an embedding request to OpenAI.
func (p *OpenAIProvider) Embed(ctx context.Context, req EmbeddingRequest) (*EmbeddingResponse, error) {
	params := openai.EmbeddingNewParams{
		Model: req.Model,
	}
	switch v := req.Input.(type) {
	case string:
		params.Input = openai.EmbeddingNewParamsInputUnion{OfString: openai.String(v)}
	case []string:
		params.Input = openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: v}
	case []interface{}:
		strs := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				strs = append(strs, s)
			}
		}
		params.Input = openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: strs}
	}
	if req.EncodingFormat == "float" || req.EncodingFormat == "" {
		params.EncodingFormat = openai.EmbeddingNewParamsEncodingFormatFloat
	}
	if req.Dimensions != nil {
		params.Dimensions = openai.Int(int64(*req.Dimensions))
	}
	if req.User != "" {
		params.User = openai.String(req.User)
	}

	result, err := p.client.Embeddings.New(ctx, params)
	if err != nil {
		return nil, err
	}

	embeddings := make([]Embedding, len(result.Data))
	for i, d := range result.Data {
		embeddings[i] = Embedding{
			Object:    string(d.Object),
			Embedding: d.Embedding,
			Index:     int(d.Index),
		}
	}

	return &EmbeddingResponse{
		Object: string(result.Object),
		Data:   embeddings,
		Model:  string(result.Model),
		Usage: EmbeddingUsage{
			PromptTokens: int(result.Usage.PromptTokens),
			TotalTokens:  int(result.Usage.TotalTokens),
		},
	}, nil
}

// GenerateImage sends an image generation request to OpenAI (DALL-E).
func (p *OpenAIProvider) GenerateImage(ctx context.Context, req ImageRequest) (*ImageResponse, error) {
	params := openai.ImageGenerateParams{
		Prompt: req.Prompt,
		Model:  openai.ImageModel(req.Model),
	}
	if req.N != nil {
		params.N = openai.Int(int64(*req.N))
	}
	if req.Size != "" {
		params.Size = openai.ImageGenerateParamsSize(req.Size)
	}
	if req.Quality != "" {
		params.Quality = openai.ImageGenerateParamsQuality(req.Quality)
	}
	if req.Style != "" {
		params.Style = openai.ImageGenerateParamsStyle(req.Style)
	}
	if req.ResponseFormat == "b64_json" {
		params.ResponseFormat = openai.ImageGenerateParamsResponseFormatB64JSON
	} else {
		params.ResponseFormat = openai.ImageGenerateParamsResponseFormatURL
	}
	if req.User != "" {
		params.User = openai.String(req.User)
	}

	result, err := p.client.Images.Generate(ctx, params)
	if err != nil {
		return nil, err
	}

	images := make([]GeneratedImage, len(result.Data))
	for i, d := range result.Data {
		images[i] = GeneratedImage{
			URL:           d.URL,
			B64JSON:       d.B64JSON,
			RevisedPrompt: d.RevisedPrompt,
		}
	}

	return &ImageResponse{
		Created: result.Created,
		Data:    images,
	}, nil
}

// Complete sends a chat completion request to OpenAI.
func (p *OpenAIProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	params := openai.ChatCompletionNewParams{
		Messages: buildOpenAIMessages(req.Messages),
		Model:    req.Model,
	}
	applyOpenAIParams(&params, req)

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, err
	}

	resp := &Response{
		ID:    completion.ID,
		Model: completion.Model,
		Usage: Usage{
			PromptTokens:     int(completion.Usage.PromptTokens),
			CompletionTokens: int(completion.Usage.CompletionTokens),
			TotalTokens:      int(completion.Usage.TotalTokens),
		},
	}
	for i, choice := range completion.Choices {
		msg := Message{
			Role:    string(choice.Message.Role),
			Content: choice.Message.Content,
		}
		for _, tc := range choice.Message.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:   tc.ID,
				Type: string(tc.Type),
				Function: FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		resp.Choices = append(resp.Choices, Choice{
			Index:        i,
			Message:      msg,
			FinishReason: string(choice.FinishReason),
		})
	}
	return resp, nil
}

// CompleteStream sends a streaming chat completion request to OpenAI.
func (p *OpenAIProvider) CompleteStream(ctx context.Context, req Request) (<-chan StreamChunk, error) {
	params := openai.ChatCompletionNewParams{
		Messages: buildOpenAIMessages(req.Messages),
		Model:    req.Model,
	}
	applyOpenAIParams(&params, req)

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	ch := make(chan StreamChunk)
	go func() {
		defer close(ch)
		for stream.Next() {
			chunk := stream.Current()
			sc := StreamChunk{
				ID:    chunk.ID,
				Model: chunk.Model,
			}
			for _, c := range chunk.Choices {
				sc.Choices = append(sc.Choices, StreamChoice{
					Index: int(c.Index),
					Delta: MessageDelta{
						Role:    c.Delta.Role,
						Content: c.Delta.Content,
					},
					FinishReason: c.FinishReason,
				})
			}
			ch <- sc
		}
		if err := stream.Err(); err != nil {
			ch <- StreamChunk{Error: err}
		}
	}()

	return ch, nil
}

// buildOpenAIMessages converts gateway Messages to the openai-go SDK union type.
func buildOpenAIMessages(msgs []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case RoleUser:
			out = append(out, openai.UserMessage(msg.Content))
		case RoleAssistant:
			out = append(out, openai.AssistantMessage(msg.Content))
		case RoleSystem:
			out = append(out, openai.SystemMessage(msg.Content))
		case RoleTool:
			out = append(out, openai.ToolMessage(msg.Content, msg.ToolCallID))
		default:
			out = append(out, openai.UserMessage(msg.Content))
		}
	}
	return out
}

// applyOpenAIParams applies all optional Request fields to the SDK params struct.
func applyOpenAIParams(params *openai.ChatCompletionNewParams, req Request) {
	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	}
	if req.TopP != nil {
		params.TopP = openai.Float(*req.TopP)
	}
	if req.N != nil {
		params.N = openai.Int(int64(*req.N))
	}
	if req.Seed != nil {
		params.Seed = openai.Int(*req.Seed)
	}
	if req.MaxTokens != nil {
		params.MaxTokens = openai.Int(int64(*req.MaxTokens))
	}
	if req.PresencePenalty != nil {
		params.PresencePenalty = openai.Float(*req.PresencePenalty)
	}
	if req.FrequencyPenalty != nil {
		params.FrequencyPenalty = openai.Float(*req.FrequencyPenalty)
	}
	if req.User != "" {
		params.User = openai.String(req.User)
	}
	if req.LogProbs {
		params.Logprobs = openai.Bool(true)
	}
	if req.TopLogProbs != nil {
		params.TopLogprobs = openai.Int(int64(*req.TopLogProbs))
	}
	if len(req.Stop) > 0 {
		params.Stop = openai.ChatCompletionNewParamsStopUnion{
			OfStringArray: req.Stop,
		}
	}
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "json_object":
			params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONObject: &openai.ResponseFormatJSONObjectParam{},
			}
		case "json_schema":
			if len(req.ResponseFormat.JSONSchema) > 0 {
				var schema openai.ResponseFormatJSONSchemaJSONSchemaParam
				if err := json.Unmarshal(req.ResponseFormat.JSONSchema, &schema); err == nil {
					params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
						OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{
							JSONSchema: schema,
						},
					}
				}
			}
		}
	}
	if len(req.Tools) > 0 {
		tools := make([]openai.ChatCompletionToolParam, 0, len(req.Tools))
		for _, t := range req.Tools {
			var paramSchema openai.FunctionParameters
			if len(t.Function.Parameters) > 0 {
				json.Unmarshal(t.Function.Parameters, &paramSchema) //nolint:errcheck,gosec
			}
			tools = append(tools, openai.ChatCompletionToolParam{
				Function: openai.FunctionDefinitionParam{
					Name:        t.Function.Name,
					Description: openai.String(t.Function.Description),
					Parameters:  paramSchema,
					Strict:      openai.Bool(t.Function.Strict),
				},
			})
		}
		params.Tools = tools
	}
}
