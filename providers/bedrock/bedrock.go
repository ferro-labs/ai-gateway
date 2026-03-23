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

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Name is the canonical provider identifier.
const Name = "bedrock"

// Options configures AWS Bedrock provider initialization.
// If AccessKeyID and SecretAccessKey are set, static credentials are used.
// Otherwise the default AWS credential chain is used.
type Options struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// Provider implements the AWS Bedrock API client.
type Provider struct {
	name   string
	client *bedrockruntime.Client
	region string
}

// Compile-time interface assertions.
var (
	_ core.Provider          = (*Provider)(nil)
	_ core.StreamProvider    = (*Provider)(nil)
	_ core.ProxiableProvider = (*Provider)(nil)
)

// New creates a new AWS Bedrock provider.
// Region defaults to us-east-1.
func New(region string) (*Provider, error) {
	return NewWithOptions(Options{Region: region})
}

// NewWithOptions creates a new AWS Bedrock provider from options.
// Region defaults to us-east-1. If static credentials are not provided,
// the AWS default credential chain is used.
func NewWithOptions(opts Options) (*Provider, error) {
	region := strings.TrimSpace(opts.Region)
	if region == "" {
		region = "us-east-1"
	}

	cfgOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}

	accessKeyID := strings.TrimSpace(opts.AccessKeyID)
	secretAccessKey := strings.TrimSpace(opts.SecretAccessKey)
	sessionToken := strings.TrimSpace(opts.SessionToken)
	if accessKeyID != "" || secretAccessKey != "" || sessionToken != "" {
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

	client := bedrockruntime.NewFromConfig(cfg)
	return &Provider{
		name:   Name,
		client: client,
		region: region,
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

// AuthHeaders satisfies ProxiableProvider (Bedrock uses AWS Sig4, not Bearer).
func (p *Provider) AuthHeaders() map[string]string { return map[string]string{} }

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
		"meta.llama3-1-405b-instruct-v1:0",
		"meta.llama3-1-70b-instruct-v1:0",
		"meta.llama3-1-8b-instruct-v1:0",
		"meta.llama3-70b-instruct-v1:0",
		"meta.llama3-8b-instruct-v1:0",
	}
}

// SupportsModel returns true for any model — AWS Bedrock validates model IDs.
func (p *Provider) SupportsModel(_ string) bool { return true }

// Models returns structured model metadata.
func (p *Provider) Models() []core.ModelInfo {
	return core.ModelsFromList(p.name, p.SupportedModels())
}

// ── Anthropic Claude on Bedrock ───────────────────────────────────────────────

type bedrockAnthropicRequest struct {
	AnthropicVersion string         `json:"anthropic_version"`
	MaxTokens        int            `json:"max_tokens"`
	Messages         []core.Message `json:"messages"`
	Temperature      *float64       `json:"temperature,omitempty"`
	TopP             *float64       `json:"top_p,omitempty"`
	StopSequences    []string       `json:"stop_sequences,omitempty"`
	System           string         `json:"system,omitempty"`
}

type bedrockAnthropicResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
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

// Complete sends a request to AWS Bedrock and returns the response.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	modelID := req.Model
	if strings.HasPrefix(modelID, "anthropic.") {
		return p.completeAnthropic(ctx, req)
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

	var system string
	var messages []core.Message
	for _, msg := range req.Messages {
		if msg.Role == core.RoleSystem {
			system = msg.Content
		} else {
			messages = append(messages, msg)
		}
	}

	anthropicReq := bedrockAnthropicRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        maxTokens,
		Messages:         messages,
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
	for _, c := range anthropicResp.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}

	return &core.Response{
		ID:       anthropicResp.ID,
		Model:    req.Model,
		Provider: p.name,
		Choices: []core.Choice{{
			Index:        0,
			Message:      core.Message{Role: core.RoleAssistant, Content: text},
			FinishReason: anthropicResp.StopReason,
		}},
		Usage: core.Usage{
			PromptTokens:     anthropicResp.Usage.InputTokens,
			CompletionTokens: anthropicResp.Usage.OutputTokens,
			TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
		},
	}, nil
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
			FinishReason: result.CompletionReason,
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
		sb.WriteString(fmt.Sprintf("<|start_header_id|>%s<|end_header_id|>\n\n%s<|eot_id|>\n", msg.Role, msg.Content))
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
			FinishReason: llamaResp.StopReason,
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
	if !strings.HasPrefix(req.Model, "anthropic.") {
		return nil, fmt.Errorf("streaming on Bedrock is currently only supported for anthropic.claude-* models")
	}

	maxTokens := 1024
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	var system string
	var messages []core.Message
	for _, msg := range req.Messages {
		if msg.Role == core.RoleSystem {
			system = msg.Content
		} else {
			messages = append(messages, msg)
		}
	}

	anthropicReq := bedrockAnthropicRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        maxTokens,
		Messages:         messages,
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		StopSequences:    req.Stop,
		System:           system,
	}

	body, err := core.MarshalJSON(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	output, err := p.client.InvokeModelWithResponseStream(ctx, &bedrockruntime.InvokeModelWithResponseStreamInput{
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
		stream := output.GetStream()
		defer func() { _ = stream.Close() }()

		for event := range stream.Events() {
			if e, ok := event.(*types.ResponseStreamMemberChunk); ok {
				var delta struct {
					Type  string `json:"type"`
					Index int    `json:"index"`
					Delta struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"delta"`
				}
				if err := json.Unmarshal(e.Value.Bytes, &delta); err != nil {
					continue
				}
				if delta.Type == "content_block_delta" && delta.Delta.Type == "text_delta" {
					ch <- core.StreamChunk{
						Model: req.Model,
						Choices: []core.StreamChoice{{
							Index: delta.Index,
							Delta: core.MessageDelta{Content: delta.Delta.Text},
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
