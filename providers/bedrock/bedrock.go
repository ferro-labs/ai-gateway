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

	// context.Background() is intentional: this loads the AWS config once at
	// provider construction time and the resulting credential providers live for
	// the whole lifetime of the provider (refreshing credentials as needed). It
	// is not request-scoped, so binding it to a request's context would wrongly
	// cancel config loading / credential refresh when that request completes.
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

// ── Amazon Titan ─────────────────────────────────────────────────────────────

// ── Amazon Nova ──────────────────────────────────────────────────────────────

// ── Meta Llama ────────────────────────────────────────────────────────────────

// ── Embeddings ───────────────────────────────────────────────────────────────

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
