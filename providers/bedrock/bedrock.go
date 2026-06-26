// Package bedrock provides a client for AWS Bedrock.
package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go/auth/bearer"

	"github.com/ferro-labs/ai-gateway/internal/anthropicwire"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "bedrock"

// Options configures AWS Bedrock provider initialization.
// If BearerToken is set, bearer auth is used instead of SigV4.
// If AccessKeyID and SecretAccessKey are set, static credentials are used.
// Otherwise the default AWS credential chain is used.
type Options struct {
	Region          string
	BearerToken     string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

type bedrockRuntimeClient interface {
	InvokeModel(context.Context, *bedrockruntime.InvokeModelInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
	InvokeModelWithResponseStream(context.Context, *bedrockruntime.InvokeModelWithResponseStreamInput, ...func(*bedrockruntime.Options)) (bedrockEventStream, error)
}

// bedrockEventStream is the minimal surface CompleteStream needs from a
// streaming invocation. *bedrockruntime.InvokeModelWithResponseStreamEventStream
// satisfies it, and tests can supply a fake without poking unexported fields.
type bedrockEventStream interface {
	Events() <-chan types.ResponseStream
	Close() error
	Err() error
}

// realBedrockClient adapts the AWS SDK client to bedrockRuntimeClient, unwrapping
// the streaming Output to its event stream so the interface stays test-friendly.
type realBedrockClient struct {
	*bedrockruntime.Client
}

func (c realBedrockClient) InvokeModelWithResponseStream(ctx context.Context, in *bedrockruntime.InvokeModelWithResponseStreamInput, opts ...func(*bedrockruntime.Options)) (bedrockEventStream, error) {
	out, err := c.Client.InvokeModelWithResponseStream(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	return out.GetStream(), nil
}

// Provider implements the AWS Bedrock API client.
type Provider struct {
	name        string
	client      bedrockRuntimeClient
	region      string
	bearerToken string
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.EmbeddingProvider = (*Provider)(nil)
	_ core.ImageProvider     = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new AWS Bedrock provider.
// Region defaults to us-east-1.
func New(region string) (*Provider, error) {
	return NewWithOptions(Options{Region: region})
}

// NewWithOptions creates a new AWS Bedrock provider from options.
// defaultBedrockRegion is used when no region is configured via options or env.
const defaultBedrockRegion = "us-east-1"

// NewWithOptions builds a Bedrock provider from explicit options. Region
// defaults to us-east-1. If static credentials are not provided, the AWS
// default credential chain is used.
func NewWithOptions(opts Options) (*Provider, error) {
	region := strings.TrimSpace(opts.Region)
	if region == "" {
		region = defaultBedrockRegion
	}

	cfgOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	var clientOpts []func(*bedrockruntime.Options)

	accessKeyID := strings.TrimSpace(opts.AccessKeyID)
	secretAccessKey := strings.TrimSpace(opts.SecretAccessKey)
	sessionToken := strings.TrimSpace(opts.SessionToken)
	bearerToken := strings.TrimSpace(opts.BearerToken)
	if bearerToken != "" {
		tokenProvider := bearer.StaticTokenProvider{
			Token: bearer.Token{Value: bearerToken},
		}
		cfgOpts = append(cfgOpts,
			awsconfig.WithBearerAuthTokenProvider(tokenProvider),
			awsconfig.WithAuthSchemePreference("httpBearerAuth"),
		)
		clientOpts = append(clientOpts, func(o *bedrockruntime.Options) {
			o.BearerAuthTokenProvider = tokenProvider
			o.AuthSchemePreference = []string{"httpBearerAuth"}
		})
	} else if accessKeyID != "" || secretAccessKey != "" || sessionToken != "" {
		if accessKeyID == "" || secretAccessKey == "" {
			return nil, fmt.Errorf("bedrock static credentials require both access key ID and secret access key")
		}
		staticCreds := credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, sessionToken)
		cfgOpts = append(cfgOpts, awsconfig.WithCredentialsProvider(aws.NewCredentialsCache(staticCreds)))
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), cfgOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := realBedrockClient{bedrockruntime.NewFromConfig(cfg, clientOpts...)}
	return &Provider{
		name:        Name,
		client:      client,
		region:      region,
		bearerToken: bearerToken,
	}, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// Region returns the configured AWS region.
func (p *Provider) Region() string { return p.region }

// BaseURL returns the Bedrock runtime endpoint URL.
func (p *Provider) BaseURL() string {
	return fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", p.region)
}

// AuthHeaders satisfies ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	if p.bearerToken == "" {
		return map[string]string{}
	}
	return map[string]string{"Authorization": "Bearer " + p.bearerToken}
}

// SupportedModels returns well-known Bedrock model IDs.
func (p *Provider) SupportedModels() []string {
	return []string{
		"anthropic.claude-3-5-sonnet-20241022-v2:0",
		"anthropic.claude-3-5-haiku-20241022-v1:0",
		"anthropic.claude-3-opus-20240229-v1:0",
		"anthropic.claude-3-sonnet-20240229-v1:0",
		"anthropic.claude-3-haiku-20240307-v1:0",
		"amazon.titan-text-express-v1",
		"amazon.titan-text-lite-v1",
		"amazon.titan-text-premier-v1:0",
		"amazon.nova-micro-v1:0",
		"amazon.nova-lite-v1:0",
		"amazon.nova-pro-v1:0",
		"amazon.nova-premier-v1:0",
		"meta.llama3-1-405b-instruct-v1:0",
		"meta.llama3-1-70b-instruct-v1:0",
		"meta.llama3-1-8b-instruct-v1:0",
		"meta.llama3-70b-instruct-v1:0",
		"meta.llama3-8b-instruct-v1:0",
		"amazon.titan-embed-text-v1",
		"amazon.titan-embed-text-v2:0",
		"cohere.embed-english-v3",
		"cohere.embed-multilingual-v3",
		"cohere.embed-v4:0",
		"amazon.nova-canvas-v1:0",
		"amazon.titan-image-generator-v1",
		"amazon.titan-image-generator-v2:0",
		"stability.stable-diffusion-xl-v1",
	}
}

// SupportsModel returns true for model families with request shapes implemented
// by this provider. Bedrock still validates the exact model ID upstream.
func (p *Provider) SupportsModel(model string) bool {
	model = bedrockModelRoutingID(model)
	for _, supported := range p.SupportedModels() {
		if model == supported {
			return true
		}
	}
	// Image families are matched here so the Nova-text exclusion guard below does
	// not reject amazon.nova-canvas. The "amazon.titan-image-" prefix is distinct
	// from the "amazon.titan-embed-image-" embeddings family.
	if isBedrockImageModel(model) {
		return true
	}
	for _, prefix := range []string{
		"anthropic.claude-",
		"amazon.titan-text-",
		"amazon.nova-",
		"amazon.titan-embed-text-",
		"cohere.embed-",
		"meta.llama",
	} {
		if strings.HasPrefix(model, "amazon.nova-") && !isBedrockNovaTextModel(model) {
			continue
		}
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// ── Anthropic Claude on Bedrock ───────────────────────────────────────────────

type bedrockAnthropicRequest struct {
	AnthropicVersion string                    `json:"anthropic_version"`
	MaxTokens        int                       `json:"max_tokens"`
	Messages         []bedrockAnthropicMessage `json:"messages"`
	Tools            []anthropicwire.Tool      `json:"tools,omitempty"`
	ToolChoice       any                       `json:"tool_choice,omitempty"`
	Temperature      *float64                  `json:"temperature,omitempty"`
	TopP             *float64                  `json:"top_p,omitempty"`
	StopSequences    []string                  `json:"stop_sequences,omitempty"`
	System           string                    `json:"system,omitempty"`
}

type bedrockAnthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type bedrockAnthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type bedrockAnthropicResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// ── Amazon Titan ─────────────────────────────────────────────────────────────

type bedrockTitanRequest struct {
	InputText            string `json:"inputText"`
	TextGenerationConfig struct {
		MaxTokenCount int      `json:"maxTokenCount,omitempty"`
		Temperature   float64  `json:"temperature,omitempty"`
		TopP          *float64 `json:"topP,omitempty"`
		StopSequences []string `json:"stopSequences,omitempty"`
	} `json:"textGenerationConfig"`
}

type bedrockTitanResponse struct {
	InputTextTokenCount int `json:"inputTextTokenCount"`
	Results             []struct {
		TokenCount       int    `json:"tokenCount"`
		OutputText       string `json:"outputText"`
		CompletionReason string `json:"completionReason"`
	} `json:"results"`
}

// ── Amazon Nova ──────────────────────────────────────────────────────────────

type bedrockNovaRequest struct {
	SchemaVersion   string                      `json:"schemaVersion"`
	Messages        []bedrockNovaMessage        `json:"messages"`
	System          []bedrockNovaTextBlock      `json:"system,omitempty"`
	InferenceConfig *bedrockNovaInferenceConfig `json:"inferenceConfig,omitempty"`
}

type bedrockNovaMessage struct {
	Role    string                 `json:"role"`
	Content []bedrockNovaTextBlock `json:"content"`
}

type bedrockNovaTextBlock struct {
	Text string `json:"text"`
}

type bedrockNovaInferenceConfig struct {
	MaxTokens     int      `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
}

type bedrockNovaResponse struct {
	Output struct {
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

// ── Meta Llama ────────────────────────────────────────────────────────────────

type bedrockLlamaRequest struct {
	Prompt      string   `json:"prompt"`
	MaxGenLen   int      `json:"max_gen_len,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
}

type bedrockLlamaResponse struct {
	Generation           string `json:"generation"`
	PromptTokenCount     int    `json:"prompt_token_count"`
	GenerationTokenCount int    `json:"generation_token_count"`
	StopReason           string `json:"stop_reason"`
}

// ── Embeddings ───────────────────────────────────────────────────────────────

type bedrockTitanEmbedRequest struct {
	InputText  string `json:"inputText"`
	Dimensions *int   `json:"dimensions,omitempty"`
}

type bedrockTitanEmbedResponse struct {
	Embedding           []float64 `json:"embedding"`
	InputTextTokenCount int       `json:"inputTextTokenCount"`
}

type bedrockCohereEmbedRequest struct {
	Texts          []string `json:"texts"`
	InputType      string   `json:"input_type"`
	EmbeddingTypes []string `json:"embedding_types,omitempty"`
}

type bedrockCohereEmbeddingVectors [][]float64

func (v *bedrockCohereEmbeddingVectors) UnmarshalJSON(data []byte) error {
	var vectors [][]float64
	if err := json.Unmarshal(data, &vectors); err == nil {
		*v = vectors
		return nil
	}

	var typed map[string][][]float64
	if err := json.Unmarshal(data, &typed); err != nil {
		return err
	}
	if vectors, ok := typed["float"]; ok {
		*v = vectors
		return nil
	}
	return fmt.Errorf("cohere embedding response did not include float embeddings")
}

type bedrockCohereEmbedResponse struct {
	Embeddings bedrockCohereEmbeddingVectors `json:"embeddings"`
	Meta       struct {
		BilledUnits struct {
			InputTokens int `json:"input_tokens"`
		} `json:"billed_units"`
	} `json:"meta"`
}

// Embed sends a text embedding request to AWS Bedrock.
func (p *Provider) Embed(ctx context.Context, req core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	texts, err := bedrockEmbeddingTexts(req.Input)
	if err != nil {
		return nil, err
	}

	switch req.EncodingFormat {
	case "", "float":
	default:
		return nil, fmt.Errorf("embed: unsupported encoding_format %q; Bedrock embeddings return float vectors", req.EncodingFormat)
	}

	modelID := bedrockModelRoutingID(req.Model)
	switch {
	case isBedrockTitanTextEmbeddingModel(modelID):
		return p.embedTitan(ctx, req, modelID, texts)
	case isBedrockCohereEmbeddingModel(modelID):
		return p.embedCohere(ctx, req, modelID, texts)
	default:
		return nil, fmt.Errorf("unsupported Bedrock embedding model: %s", req.Model)
	}
}

func bedrockEmbeddingTexts(input any) ([]string, error) {
	switch v := input.(type) {
	case string:
		return []string{v}, nil
	case []string:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		return v, nil
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("embed: Input must not be an empty array")
		}
		texts := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("embed: Input[%d] is %T, want string", i, item)
			}
			texts = append(texts, s)
		}
		return texts, nil
	case nil:
		return nil, fmt.Errorf("embed: Input must not be nil")
	default:
		return nil, fmt.Errorf("embed: unsupported Input type %T; want string or []string", input)
	}
}

func (p *Provider) embedTitan(ctx context.Context, req core.EmbeddingRequest, modelID string, texts []string) (*core.EmbeddingResponse, error) {
	if req.Dimensions != nil && !strings.HasPrefix(modelID, "amazon.titan-embed-text-v2") {
		return nil, fmt.Errorf("embed: dimensions are only supported for amazon.titan-embed-text-v2 models")
	}

	data := make([]core.Embedding, 0, len(texts))
	promptTokens := 0
	for i, text := range texts {
		titanReq := bedrockTitanEmbedRequest{
			InputText:  text,
			Dimensions: req.Dimensions,
		}
		var titanResp bedrockTitanEmbedResponse
		if err := p.invokeModelJSON(ctx, req.Model, titanReq, &titanResp); err != nil {
			return nil, err
		}
		data = append(data, core.Embedding{
			Object:    "embedding",
			Embedding: titanResp.Embedding,
			Index:     i,
		})
		promptTokens += titanResp.InputTextTokenCount
	}

	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: core.EmbeddingUsage{
			PromptTokens: promptTokens,
			TotalTokens:  promptTokens,
		},
	}, nil
}

func (p *Provider) embedCohere(ctx context.Context, req core.EmbeddingRequest, modelID string, texts []string) (*core.EmbeddingResponse, error) {
	if req.Dimensions != nil {
		return nil, fmt.Errorf("embed: dimensions are not supported for Bedrock Cohere embeddings")
	}

	cohereReq := bedrockCohereEmbedRequest{
		Texts:     texts,
		InputType: "search_document",
	}
	if strings.HasPrefix(modelID, "cohere.embed-v4") {
		cohereReq.EmbeddingTypes = []string{"float"}
	}

	var cohereResp bedrockCohereEmbedResponse
	if err := p.invokeModelJSON(ctx, req.Model, cohereReq, &cohereResp); err != nil {
		return nil, err
	}
	if len(cohereResp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("bedrock cohere embed response returned %d embeddings for %d inputs", len(cohereResp.Embeddings), len(texts))
	}

	data := make([]core.Embedding, len(cohereResp.Embeddings))
	for i, emb := range cohereResp.Embeddings {
		data[i] = core.Embedding{
			Object:    "embedding",
			Embedding: emb,
			Index:     i,
		}
	}
	inputTokens := cohereResp.Meta.BilledUnits.InputTokens
	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: core.EmbeddingUsage{
			PromptTokens: inputTokens,
			TotalTokens:  inputTokens,
		},
	}, nil
}

func (p *Provider) invokeModelJSON(ctx context.Context, modelID string, payload any, out any) error {
	body, err := core.MarshalJSON(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	output, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(modelID),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        body,
	})
	if err != nil {
		return fmt.Errorf("bedrock invoke failed: %w", err)
	}

	if err := json.Unmarshal(output.Body, out); err != nil {
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}
	return nil
}

func bedrockModelRoutingID(model string) string {
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		model = model[idx+1:]
	}
	for _, prefix := range []string{"us.", "eu.", "apac.", "global."} {
		if strings.HasPrefix(model, prefix) {
			return strings.TrimPrefix(model, prefix)
		}
	}
	return model
}

func isBedrockTitanTextEmbeddingModel(model string) bool {
	return strings.HasPrefix(model, "amazon.titan-embed-text-")
}

func isBedrockCohereEmbeddingModel(model string) bool {
	return strings.HasPrefix(model, "cohere.embed-")
}

func isBedrockNovaTextModel(model string) bool {
	for _, prefix := range []string{
		"amazon.nova-micro-",
		"amazon.nova-lite-",
		"amazon.nova-pro-",
		"amazon.nova-premier-",
		"amazon.nova-2-lite-",
		"amazon.nova-2-pro-",
	} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// Complete sends a request to AWS Bedrock and returns the response.
// bedrockSupportedParams returns the OpenAI parameters expressible on the given
// Bedrock model family's inference shape. Anything else the caller set is
// warn-and-dropped (#140).
func bedrockSupportedParams(modelID string) []string {
	switch {
	case strings.HasPrefix(modelID, "anthropic."):
		return []string{"temperature", "top_p", "max_tokens", "stop", "tools", "tool_choice"}
	case strings.HasPrefix(modelID, "amazon.titan"):
		return []string{"temperature", "top_p", "max_tokens", "stop"}
	case isBedrockNovaTextModel(modelID):
		return []string{"temperature", "top_p", "max_tokens", "stop"}
	case strings.HasPrefix(modelID, "meta.llama"):
		return []string{"temperature", "top_p", "max_tokens"}
	default:
		return nil
	}
}

func bedrockBuildAnthropicMessages(req core.Request) ([]bedrockAnthropicMessage, string) {
	var systemParts []string
	var messages []bedrockAnthropicMessage
	for _, msg := range req.Messages {
		switch msg.Role {
		case core.RoleSystem:
			systemParts = append(systemParts, msg.Content)
		case core.RoleTool:
			block := bedrockAnthropicBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   msg.Content,
			}
			if n := len(messages); n > 0 && messages[n-1].Role == core.RoleUser {
				if blocks, ok := messages[n-1].Content.([]bedrockAnthropicBlock); ok {
					blocks = append(blocks, block)
					messages[n-1].Content = blocks
					continue
				}
			}
			messages = append(messages, bedrockAnthropicMessage{Role: core.RoleUser, Content: []bedrockAnthropicBlock{block}})
		default:
			messages = append(messages, bedrockAnthropicMessage{Role: msg.Role, Content: bedrockAnthropicContent(msg)})
		}
	}
	return messages, strings.Join(systemParts, "\n")
}

func bedrockAnthropicContent(msg core.Message) any {
	var blocks []bedrockAnthropicBlock
	if msg.Content != "" {
		blocks = append(blocks, bedrockAnthropicBlock{Type: "text", Text: msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if len(input) == 0 || !json.Valid(input) {
			input = json.RawMessage(`{}`)
		}
		blocks = append(blocks, bedrockAnthropicBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}
	if len(msg.ToolCalls) == 0 {
		return msg.Content
	}
	return blocks
}

func bedrockAnthropicToolChoice(choice any, tools []core.Tool) any {
	// tool_choice is only valid alongside tools; Anthropic-on-Bedrock 400s otherwise.
	if len(tools) == 0 {
		return nil
	}
	switch kind, name := core.NormalizeToolChoice(choice); kind {
	case core.ToolChoiceAuto:
		return map[string]string{"type": "auto"}
	case core.ToolChoiceNone:
		return map[string]string{"type": "none"}
	case core.ToolChoiceRequired:
		return map[string]string{"type": "any"}
	case core.ToolChoiceFunction:
		return map[string]string{"type": "tool", "name": name}
	default:
		return nil
	}
}

// Complete sends a non-streaming chat completion request to Bedrock, dispatching
// to the model family (Anthropic, Titan, Llama) that matches the model prefix.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	modelID := bedrockModelRoutingID(req.Model)
	core.WarnUnsupportedParams(ctx, p.Name(), modelID, req, bedrockSupportedParams(modelID)...)
	if strings.HasPrefix(modelID, "anthropic.") {
		return p.completeAnthropic(ctx, req)
	}
	if isBedrockNovaTextModel(modelID) {
		return p.completeNova(ctx, req)
	}
	if strings.HasPrefix(modelID, "amazon.titan") {
		return p.completeTitan(ctx, req)
	}
	if strings.HasPrefix(modelID, "meta.llama") {
		return p.completeLlama(ctx, req)
	}
	return nil, fmt.Errorf("unsupported Bedrock model prefix for model: %s", modelID)
}

func (p *Provider) completeAnthropic(ctx context.Context, req core.Request) (*core.Response, error) {
	maxTokens := 1024
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	messages, system := bedrockBuildAnthropicMessages(req)

	anthropicReq := bedrockAnthropicRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        maxTokens,
		Messages:         messages,
		Tools:            anthropicwire.MapTools(req.Tools),
		ToolChoice:       bedrockAnthropicToolChoice(req.ToolChoice, req.Tools),
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		StopSequences:    req.Stop,
		System:           system,
	}

	body, err := core.MarshalJSON(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	output, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(req.Model),
		ContentType: aws.String("application/json"),
		Body:        body,
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock invoke failed: %w", err)
	}

	var anthropicResp bedrockAnthropicResponse
	if err := json.Unmarshal(output.Body, &anthropicResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	text := ""
	var toolCalls []core.ToolCall
	for _, c := range anthropicResp.Content {
		if c.Type == "text" {
			text += c.Text
			continue
		}
		if c.Type == "tool_use" {
			args := string(c.Input)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, core.ToolCall{
				ID:   c.ID,
				Type: "function",
				Function: core.FunctionCall{
					Name:      c.Name,
					Arguments: args,
				},
			})
		}
	}

	return &core.Response{
		ID:       anthropicResp.ID,
		Model:    req.Model,
		Provider: p.name,
		Choices: []core.Choice{{
			Index:        0,
			Message:      core.Message{Role: core.RoleAssistant, Content: text, ToolCalls: toolCalls},
			FinishReason: core.NormalizeFinishReason(anthropicResp.StopReason),
		}},
		Usage: core.Usage{
			PromptTokens:     anthropicResp.Usage.InputTokens,
			CompletionTokens: anthropicResp.Usage.OutputTokens,
			TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
		},
	}, nil
}

func (p *Provider) completeNova(ctx context.Context, req core.Request) (*core.Response, error) {
	novaReq := bedrockNovaRequest{
		SchemaVersion: "messages-v1",
	}
	for _, msg := range req.Messages {
		content := bedrockNovaMessageTextContent(msg)
		if msg.Role == core.RoleSystem {
			novaReq.System = append(novaReq.System, content...)
			continue
		}
		novaReq.Messages = append(novaReq.Messages, bedrockNovaMessage{
			Role:    msg.Role,
			Content: content,
		})
	}

	if req.MaxTokens != nil || req.Temperature != nil || req.TopP != nil || len(req.Stop) > 0 {
		novaReq.InferenceConfig = &bedrockNovaInferenceConfig{
			Temperature:   req.Temperature,
			TopP:          req.TopP,
			StopSequences: req.Stop,
		}
		if req.MaxTokens != nil {
			novaReq.InferenceConfig.MaxTokens = *req.MaxTokens
		}
	}

	var novaResp bedrockNovaResponse
	if err := p.invokeModelJSON(ctx, req.Model, novaReq, &novaResp); err != nil {
		return nil, err
	}

	var text strings.Builder
	for _, c := range novaResp.Output.Message.Content {
		text.WriteString(c.Text)
	}

	totalTokens := novaResp.Usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = novaResp.Usage.InputTokens + novaResp.Usage.OutputTokens
	}

	return &core.Response{
		Model:    req.Model,
		Provider: p.name,
		Choices: []core.Choice{{
			Index:        0,
			Message:      core.Message{Role: core.RoleAssistant, Content: text.String()},
			FinishReason: core.NormalizeFinishReason(novaResp.StopReason),
		}},
		Usage: core.Usage{
			PromptTokens:     novaResp.Usage.InputTokens,
			CompletionTokens: novaResp.Usage.OutputTokens,
			TotalTokens:      totalTokens,
		},
	}, nil
}

func bedrockNovaMessageTextContent(msg core.Message) []bedrockNovaTextBlock {
	if len(msg.ContentParts) == 0 {
		return []bedrockNovaTextBlock{{Text: msg.Content}}
	}

	content := make([]bedrockNovaTextBlock, 0, len(msg.ContentParts))
	for _, part := range msg.ContentParts {
		if part.Type == core.ContentTypeText {
			content = append(content, bedrockNovaTextBlock{Text: part.Text})
		}
	}
	if len(content) == 0 && msg.Content != "" {
		content = append(content, bedrockNovaTextBlock{Text: msg.Content})
	}
	return content
}

func (p *Provider) completeTitan(ctx context.Context, req core.Request) (*core.Response, error) {
	var sb strings.Builder
	for _, msg := range req.Messages {
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}

	titanReq := bedrockTitanRequest{InputText: sb.String()}
	if req.MaxTokens != nil {
		titanReq.TextGenerationConfig.MaxTokenCount = *req.MaxTokens
	}
	if req.Temperature != nil {
		titanReq.TextGenerationConfig.Temperature = *req.Temperature
	}
	titanReq.TextGenerationConfig.TopP = req.TopP
	titanReq.TextGenerationConfig.StopSequences = req.Stop

	body, err := core.MarshalJSON(titanReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	output, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(req.Model),
		ContentType: aws.String("application/json"),
		Body:        body,
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock invoke failed: %w", err)
	}

	var titanResp bedrockTitanResponse
	if err := json.Unmarshal(output.Body, &titanResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var choices []core.Choice
	for i, result := range titanResp.Results {
		choices = append(choices, core.Choice{
			Index:        i,
			Message:      core.Message{Role: core.RoleAssistant, Content: result.OutputText},
			FinishReason: core.NormalizeFinishReason(result.CompletionReason),
		})
	}

	totalCompletion := 0
	for _, r := range titanResp.Results {
		totalCompletion += r.TokenCount
	}

	return &core.Response{
		Model:    req.Model,
		Provider: p.name,
		Choices:  choices,
		Usage: core.Usage{
			PromptTokens:     titanResp.InputTextTokenCount,
			CompletionTokens: totalCompletion,
		},
	}, nil
}

func (p *Provider) completeLlama(ctx context.Context, req core.Request) (*core.Response, error) {
	var sb strings.Builder
	sb.WriteString("<|begin_of_text|>")
	for _, msg := range req.Messages {
		fmt.Fprintf(&sb, "<|start_header_id|>%s<|end_header_id|>\n\n%s<|eot_id|>\n", msg.Role, msg.Content)
	}
	sb.WriteString("<|start_header_id|>assistant<|end_header_id|>\n\n")

	llamaReq := bedrockLlamaRequest{
		Prompt:      sb.String(),
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}
	if req.MaxTokens != nil {
		llamaReq.MaxGenLen = *req.MaxTokens
	}

	body, err := core.MarshalJSON(llamaReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	output, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(req.Model),
		ContentType: aws.String("application/json"),
		Body:        body,
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock invoke failed: %w", err)
	}

	var llamaResp bedrockLlamaResponse
	if err := json.Unmarshal(output.Body, &llamaResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &core.Response{
		Model:    req.Model,
		Provider: p.name,
		Choices: []core.Choice{{
			Index:        0,
			Message:      core.Message{Role: core.RoleAssistant, Content: llamaResp.Generation},
			FinishReason: core.NormalizeFinishReason(llamaResp.StopReason),
		}},
		Usage: core.Usage{
			PromptTokens:     llamaResp.PromptTokenCount,
			CompletionTokens: llamaResp.GenerationTokenCount,
			TotalTokens:      llamaResp.PromptTokenCount + llamaResp.GenerationTokenCount,
		},
	}, nil
}

// CompleteStream sends a streaming request to AWS Bedrock via InvokeModelWithResponseStream.
// Currently only Anthropic Claude streaming is implemented.
func (p *Provider) CompleteStream(ctx context.Context, req core.Request) (<-chan core.StreamChunk, error) {
	if !strings.HasPrefix(bedrockModelRoutingID(req.Model), "anthropic.") {
		return nil, fmt.Errorf("streaming on Bedrock is currently only supported for anthropic.claude-* models")
	}
	core.WarnUnsupportedParams(ctx, p.Name(), req.Model, req, bedrockSupportedParams(bedrockModelRoutingID(req.Model))...)

	maxTokens := 1024
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	messages, system := bedrockBuildAnthropicMessages(req)

	anthropicReq := bedrockAnthropicRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        maxTokens,
		Messages:         messages,
		Tools:            anthropicwire.MapTools(req.Tools),
		ToolChoice:       bedrockAnthropicToolChoice(req.ToolChoice, req.Tools),
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		StopSequences:    req.Stop,
		System:           system,
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
							Index: delta.Index,
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
