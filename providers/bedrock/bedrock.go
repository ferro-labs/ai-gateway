// Package bedrock provides a client for AWS Bedrock.
package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go/auth/bearer"

	providerhttp "github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
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

// bedrockRerankClient is the minimal surface Rerank needs from the Bedrock
// agent-runtime service (a different AWS service than bedrockruntime).
// *bedrockagentruntime.Client satisfies it; tests supply a fake.
type bedrockRerankClient interface {
	Rerank(context.Context, *bedrockagentruntime.RerankInput, ...func(*bedrockagentruntime.Options)) (*bedrockagentruntime.RerankOutput, error)
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
	name         string
	client       bedrockRuntimeClient
	rerankClient bedrockRerankClient
	region       string
	bearerToken  string
}

// Compile-time interface assertions.
var (
	_ core.Provider              = (*Provider)(nil)
	_ core.StreamProvider        = (*Provider)(nil)
	_ core.EmbeddingProvider     = (*Provider)(nil)
	_ core.ImageProvider         = (*Provider)(nil)
	_ core.RerankProvider        = (*Provider)(nil)
	_ core.ProxiableProvider     = (*Provider)(nil)
	_ core.NonOpenAIWireProvider = (*Provider)(nil)
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
	cfg, err := loadAWSConfig(context.Background(), cfgOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Use the gateway's tuned per-provider HTTP client (higher dial/header
	// timeouts for Bedrock's cold starts) rather than the SDK default. Set again
	// rather than relying on loadAWSConfig: on its CA-bundle path the SDK owns
	// cfg.HTTPClient, and the data plane must not inherit that client's
	// redirect-following behaviour.
	cfg.HTTPClient = providerhttp.ForProvider(Name)

	client := realBedrockClient{bedrockruntime.NewFromConfig(cfg, clientOpts...)}
	// Rerank lives on the separate bedrock-agent-runtime service; it is SigV4 via
	// the same resolved config (and inherits cfg.HTTPClient set above).
	return &Provider{
		name:         Name,
		client:       client,
		rerankClient: bedrockagentruntime.NewFromConfig(cfg),
		region:       region,
		bearerToken:  bearerToken,
	}, nil
}

// loadAWSConfig loads the AWS config with the gateway's own HTTP client passed
// IN, not assigned afterwards.
//
// The credential chain — IMDS, SSO, STS, ssooidc — is constructed inside
// LoadDefaultConfig (resolveCredentials calls imds/sso/sts.NewFromConfig), and
// resolveHTTPClient runs well before it. A client assigned to cfg.HTTPClient
// after the call therefore reaches the Bedrock data plane only; the credential
// clients keep the SDK's own BuildableClient, which follows 307 and 308. Go
// strips Authorization across hosts and the SDK strips X-Amz-Security-Token,
// but the IMDSv2 x-aws-ec2-metadata-token and SSO x-amz-sso_bearer_token
// headers are custom and are replayed verbatim to the redirect target.
//
// A configured CA bundle (AWS_CA_BUNDLE, or the shared-config ca_bundle key)
// makes that impossible, and the bundle wins. resolveCustomCABundle can only
// add RootCAs to a *awshttp.BuildableClient and hard-errors on anything else,
// so passing our own client turns those deployments into a startup failure.
// Resolving the bundle ourselves does not avoid it: the SDK resolves it
// independently from env and shared config and fails the same way, and there is
// no option that suppresses that. Silently dropping an explicit TLS trust
// decision would be worse than following a redirect, so the fallback keeps the
// SDK's client and the pre-existing behaviour.
//
// The retry is not error-string matching: WithHTTPClient can only change
// whether resolveCustomCABundle succeeds, so a load that fails with it and
// succeeds without it is that case and no other. A genuinely broken config
// fails both times and reports the first error.
func loadAWSConfig(ctx context.Context, cfgOpts []func(*awsconfig.LoadOptions) error) (aws.Config, error) {
	withClient := append(slices.Clone(cfgOpts), awsconfig.WithHTTPClient(providerhttp.ForProvider(Name)))
	cfg, err := awsconfig.LoadDefaultConfig(ctx, withClient...)
	if err == nil {
		return cfg, nil
	}

	cfg, fallbackErr := awsconfig.LoadDefaultConfig(ctx, cfgOpts...)
	if fallbackErr != nil {
		return aws.Config{}, err
	}
	logger.Ctx(ctx).Warn(
		"a custom CA bundle is configured, so the AWS credential chain keeps the SDK's HTTP client and will follow 307/308 redirects from the metadata, SSO or STS endpoint",
		"provider", Name,
	)
	return cfg, nil
}

// Name implements core.Provider.
func (p *Provider) Name() string { return p.name }

// Region returns the configured AWS region.
func (p *Provider) Region() string { return p.region }

// BaseURL returns the Bedrock runtime endpoint URL.
func (p *Provider) BaseURL() string {
	return fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", p.region)
}

// NonOpenAIWire marks Bedrock as ineligible for transparent OpenAI-wire proxy
// pass-through: its upstream is the AWS Bedrock API (SigV4-signed and not
// OpenAI-shaped). It remains fully usable via its native translated endpoints,
// and can graduate to signed pass-through by implementing RequestSigner. See
// core.NonOpenAIWireProvider.
func (*Provider) NonOpenAIWire() {}

// AuthHeaders satisfies ProxiableProvider.
func (p *Provider) AuthHeaders() map[string]string {
	if p.bearerToken == "" {
		return map[string]string{}
	}
	return map[string]string{"Authorization": "Bearer " + p.bearerToken}
}

// knownModelIDs are Bedrock IDs this provider has a translated request shape
// for but whose names the prefix rules below do not reach — a titan or
// stability id carries no family prefix to match on.
//
// This is not an inventory and is not advertised anywhere: the catalog and
// live discovery answer what Bedrock serves. It exists only so SupportsModel
// can say yes to an id it can actually translate.
var knownModelIDs = map[string]struct{}{
	"amazon.titan-text-express-v1":      {},
	"amazon.titan-text-lite-v1":         {},
	"amazon.titan-text-premier-v1:0":    {},
	"amazon.titan-embed-text-v1":        {},
	"amazon.titan-embed-text-v2:0":      {},
	"amazon.titan-image-generator-v1":   {},
	"amazon.titan-image-generator-v2:0": {},
	"stability.stable-diffusion-xl-v1":  {},
}

// SupportsModel returns true for model families with request shapes implemented
// by this provider. Bedrock still validates the exact model ID upstream.
func (p *Provider) SupportsModel(model string) bool {
	model = bedrockModelRoutingID(model)
	if _, ok := knownModelIDs[model]; ok {
		return true
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
		return bedrockInvokeError("invoke", err)
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

// bedrockSupportedParams returns the OpenAI parameters expressible on the given
// Bedrock model family's inference shape. Anything else the caller set is
// warn-and-dropped (#140). Callers must only pass a modelID that
// bedrockKnownModelFamily accepts — the nil default case here means "no
// params supported", not "unrecognized model", so it is not a safe stand-in
// for that check.
func bedrockSupportedParams(modelID string) []string {
	switch {
	case strings.HasPrefix(modelID, "anthropic."):
		return []string{"temperature", "top_p", "max_tokens", "stop", "tools", "tool_choice", "parallel_tool_calls"}
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

// bedrockKnownModelFamily reports whether modelID matches one of the model
// families Complete dispatches to. It must be checked before
// bedrockSupportedParams is used for enforcement: an unrecognized model has
// no supported-params list to violate, so parameter enforcement must not run
// (and misreport a parameter error) ahead of the real "unsupported model"
// error.
func bedrockKnownModelFamily(modelID string) bool {
	return strings.HasPrefix(modelID, "anthropic.") ||
		isBedrockNovaTextModel(modelID) ||
		strings.HasPrefix(modelID, "amazon.titan") ||
		strings.HasPrefix(modelID, "meta.llama")
}

// Complete sends a non-streaming chat completion request to Bedrock, dispatching
// to the model family (Anthropic, Titan, Llama) that matches the model prefix.
func (p *Provider) Complete(ctx context.Context, req core.Request) (*core.Response, error) {
	modelID := bedrockModelRoutingID(req.Model)
	if !bedrockKnownModelFamily(modelID) {
		return nil, fmt.Errorf("unsupported Bedrock model prefix for model: %s", modelID)
	}
	if err := core.EnforceUnsupportedParamsList(ctx, p.Name(), modelID, req, bedrockSupportedParams(modelID)...); err != nil {
		return nil, err
	}
	if strings.HasPrefix(modelID, "anthropic.") {
		return p.completeAnthropic(ctx, req)
	}
	if isBedrockNovaTextModel(modelID) {
		return p.completeNova(ctx, req)
	}
	if strings.HasPrefix(modelID, "amazon.titan") {
		return p.completeTitan(ctx, req)
	}
	return p.completeLlama(ctx, req)
}
