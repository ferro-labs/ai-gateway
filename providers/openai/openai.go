// Package openai provides a client for the OpenAI API using the official Go SDK.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "openai"

const defaultBaseURL = "https://api.openai.com"

// Provider implements the OpenAI API client using the official Go SDK.
type Provider struct {
	name       string
	apiKey     string
	baseURL    string
	httpClient *http.Client
	client     oai.Client
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.ImageProvider     = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new OpenAI provider.
// The optional baseURL parameter allows overriding the API endpoint (pass "" for the default).
func New(apiKey, baseURL string) (*Provider, error) {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(providerhttp.Shared()),
	}
	resolvedBase := defaultBaseURL
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
		resolvedBase = baseURL
	}
	client := oai.NewClient(opts...)
	return &Provider{
		name:       Name,
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(resolvedBase, "/"),
		httpClient: providerhttp.Shared(),
		client:     client,
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// BaseURL implements core.ProxiableProvider.
func (p *Provider) BaseURL() string { return p.baseURL }

// AuthHeaders implements core.ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

// SupportedModels returns the list of models supported by this provider.
func (p *Provider) SupportedModels() []string {
	return []string{
		"gpt-4o",
		"gpt-4-turbo",
		"gpt-4",
		"gpt-3.5-turbo",
	}
}

// SupportsModel returns true if the model matches known OpenAI prefixes.
func (p *Provider) SupportsModel(model string) bool {
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
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// Embed sends an embedding request to OpenAI.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	params := oai.EmbeddingNewParams{
		Model: req.Model,
	}

	switch v := req.Input.(type) {
	case string:
		params.Input = oai.EmbeddingNewParamsInputUnion{OfString: oai.String(v)}
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		params.Input = oai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: v}
	case []interface{}:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		strs := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("embed: Input[%d] is %T, want string", i, item)
			}
			strs = append(strs, s)
		}
		params.Input = oai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: strs}
	case nil:
		return nil, fmt.Errorf("embed: Input must not be nil")
	default:
		return nil, fmt.Errorf("embed: unsupported Input type %T; want string or []string", req.Input)
	}

	switch req.EncodingFormat {
	case "", "float":
		params.EncodingFormat = oai.EmbeddingNewParamsEncodingFormatFloat
	case "base64":
		params.EncodingFormat = oai.EmbeddingNewParamsEncodingFormatBase64
	default:
		return nil, fmt.Errorf("embed: unsupported encoding_format %q; valid values are \"float\" and \"base64\"", req.EncodingFormat)
	}

	if req.Dimensions != nil {
		params.Dimensions = oai.Int(int64(*req.Dimensions))
	}
	if req.User != "" {
		params.User = oai.String(req.User)
	}

	result, err := p.client.Embeddings.New(ctx, params)
	if err != nil {
		return nil, err
	}

	embeddings := make([]core.Embedding, len(result.Data))
	for i, d := range result.Data {
		embeddings[i] = core.Embedding{
			Object:    string(d.Object),
			Embedding: d.Embedding,
			Index:     int(d.Index),
		}
	}

	return &core.EmbeddingResponse{
		Object: string(result.Object),
		Data:   embeddings,
		Model:  string(result.Model),
		Usage: core.EmbeddingUsage{
			PromptTokens: int(result.Usage.PromptTokens),
			TotalTokens:  int(result.Usage.TotalTokens),
		},
	}, nil
}

// GenerateImage sends an image generation request to OpenAI (DALL-E).
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	params := oai.ImageGenerateParams{
		Prompt: req.Prompt,
		Model:  oai.ImageModel(req.Model),
	}
	if req.N != nil {
		params.N = oai.Int(int64(*req.N))
	}
	if req.Size != "" {
		params.Size = oai.ImageGenerateParamsSize(req.Size)
	}
	if req.Quality != "" {
		params.Quality = oai.ImageGenerateParamsQuality(req.Quality)
	}
	if req.Style != "" {
		params.Style = oai.ImageGenerateParamsStyle(req.Style)
	}
	if req.ResponseFormat == "b64_json" {
		params.ResponseFormat = oai.ImageGenerateParamsResponseFormatB64JSON
	} else {
		params.ResponseFormat = oai.ImageGenerateParamsResponseFormatURL
	}
	if req.User != "" {
		params.User = oai.String(req.User)
	}

	result, err := p.client.Images.Generate(ctx, params)
	if err != nil {
		return nil, err
	}

	images := make([]core.GeneratedImage, len(result.Data))
	for i, d := range result.Data {
		images[i] = core.GeneratedImage{
			URL:           d.URL,
			B64JSON:       d.B64JSON,
			RevisedPrompt: d.RevisedPrompt,
		}
	}

	return &core.ImageResponse{
		Created: result.Created,
		Data:    images,
	}, nil
}

// Complete sends a chat completion request to OpenAI.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	req.Stream = false

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.chatCompletionsEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusOK {
		respBody, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		var errResp openAIErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("openai API error (%d): %s", httpResp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("openai API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	var completion openAIChatCompletionResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&completion); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &core.Response{
		ID:       completion.ID,
		Object:   completion.Object,
		Created:  completion.Created,
		Model:    completion.Model,
		Provider: p.name,
		Choices:  completion.Choices,
		Usage: core.Usage{
			PromptTokens:     completion.Usage.PromptTokens,
			CompletionTokens: completion.Usage.CompletionTokens,
			TotalTokens:      completion.Usage.TotalTokens,
			ReasoningTokens:  completion.Usage.CompletionTokensDetails.ReasoningTokens,
			CacheReadTokens:  completion.Usage.PromptTokensDetails.CachedTokens,
		},
	}, nil
}

// CompleteStream sends a streaming chat completion request to OpenAI.
// stream_options.include_usage=true is set so that the final SSE chunk
// carries token-usage statistics for cost and metrics tracking.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	params := oai.ChatCompletionNewParams{
		Messages: buildMessages(req.Messages),
		Model:    req.Model,
		StreamOptions: oai.ChatCompletionStreamOptionsParam{
			IncludeUsage: oai.Bool(true),
		},
	}
	applyParams(&params, req)

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	ch := make(chan core.StreamChunk)
	go func() {
		defer close(ch)
		for stream.Next() {
			chunk := stream.Current()
			sc := core.StreamChunk{
				ID:    chunk.ID,
				Model: chunk.Model,
			}
			for _, c := range chunk.Choices {
				sc.Choices = append(sc.Choices, core.StreamChoice{
					Index: int(c.Index),
					Delta: core.MessageDelta{
						Role:    c.Delta.Role,
						Content: c.Delta.Content,
					},
					FinishReason: c.FinishReason,
				})
			}
			if chunk.Usage.TotalTokens > 0 {
				usage := &core.Usage{
					PromptTokens:     int(chunk.Usage.PromptTokens),
					CompletionTokens: int(chunk.Usage.CompletionTokens),
					TotalTokens:      int(chunk.Usage.TotalTokens),
				}
				if chunk.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
					usage.ReasoningTokens = int(chunk.Usage.CompletionTokensDetails.ReasoningTokens)
				}
				if chunk.Usage.PromptTokensDetails.CachedTokens > 0 {
					usage.CacheReadTokens = int(chunk.Usage.PromptTokensDetails.CachedTokens)
				}
				sc.Usage = usage
			}
			ch <- sc
		}
		if err := stream.Err(); err != nil {
			ch <- core.StreamChunk{Error: err}
		}
	}()

	return ch, nil
}

type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type openAIChatCompletionResponse struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []core.Choice `json:"choices"`
	Usage   struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func (p *Provider) chatCompletionsEndpoint() string {
	if strings.HasSuffix(p.baseURL, "/v1") {
		return p.baseURL + "/chat/completions"
	}
	return p.baseURL + "/v1/chat/completions"
}

// buildMessages converts gateway Messages to the openai-go SDK union type.
func buildMessages(msgs []core.Message) []oai.ChatCompletionMessageParamUnion {
	out := make([]oai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case core.RoleUser:
			out = append(out, oai.UserMessage(msg.Content))
		case core.RoleAssistant:
			out = append(out, oai.AssistantMessage(msg.Content))
		case core.RoleSystem:
			out = append(out, oai.SystemMessage(msg.Content))
		case core.RoleTool:
			out = append(out, oai.ToolMessage(msg.Content, msg.ToolCallID))
		default:
			out = append(out, oai.UserMessage(msg.Content))
		}
	}
	return out
}

// applyParams applies all optional Request fields to the SDK params struct.
func applyParams(params *oai.ChatCompletionNewParams, req core.Request) {
	if req.Temperature != nil {
		params.Temperature = oai.Float(*req.Temperature)
	}
	if req.TopP != nil {
		params.TopP = oai.Float(*req.TopP)
	}
	if req.N != nil {
		params.N = oai.Int(int64(*req.N))
	}
	if req.Seed != nil {
		params.Seed = oai.Int(*req.Seed)
	}
	if req.MaxTokens != nil {
		params.MaxTokens = oai.Int(int64(*req.MaxTokens))
	}
	if req.PresencePenalty != nil {
		params.PresencePenalty = oai.Float(*req.PresencePenalty)
	}
	if req.FrequencyPenalty != nil {
		params.FrequencyPenalty = oai.Float(*req.FrequencyPenalty)
	}
	if req.User != "" {
		params.User = oai.String(req.User)
	}
	if req.LogProbs {
		params.Logprobs = oai.Bool(true)
	}
	if req.TopLogProbs != nil {
		params.TopLogprobs = oai.Int(int64(*req.TopLogProbs))
	}
	if len(req.Stop) > 0 {
		params.Stop = oai.ChatCompletionNewParamsStopUnion{
			OfStringArray: req.Stop,
		}
	}
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "json_object":
			params.ResponseFormat = oai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONObject: &oai.ResponseFormatJSONObjectParam{},
			}
		case "json_schema":
			if len(req.ResponseFormat.JSONSchema) > 0 {
				var schema oai.ResponseFormatJSONSchemaJSONSchemaParam
				if err := json.Unmarshal(req.ResponseFormat.JSONSchema, &schema); err == nil {
					params.ResponseFormat = oai.ChatCompletionNewParamsResponseFormatUnion{
						OfJSONSchema: &oai.ResponseFormatJSONSchemaParam{
							JSONSchema: schema,
						},
					}
				}
			}
		}
	}
	if len(req.Tools) > 0 {
		tools := make([]oai.ChatCompletionToolParam, 0, len(req.Tools))
		for _, t := range req.Tools {
			var paramSchema oai.FunctionParameters
			if len(t.Function.Parameters) > 0 {
				json.Unmarshal(t.Function.Parameters, &paramSchema) //nolint:errcheck,gosec
			}
			tools = append(tools, oai.ChatCompletionToolParam{
				Function: oai.FunctionDefinitionParam{
					Name:        t.Function.Name,
					Description: oai.String(t.Function.Description),
					Parameters:  paramSchema,
					Strict:      oai.Bool(t.Function.Strict),
				},
			})
		}
		params.Tools = tools
	}
}
