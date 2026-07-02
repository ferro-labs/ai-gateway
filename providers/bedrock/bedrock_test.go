package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestNewBedrock_DefaultRegion(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	p, err := New("")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "bedrock" {
		t.Errorf("Name() = %q, want bedrock", p.Name())
	}
	if p.Region() != defaultBedrockRegion {
		t.Errorf("region = %q, want us-east-1", p.Region())
	}
}

func TestNewBedrockWithOptions_StaticCredentials(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	p, err := NewWithOptions(Options{
		Region:          "us-west-2",
		AccessKeyID:     "test-access-key",
		SecretAccessKey: "test-secret-key",
		SessionToken:    "test-session-token",
	})
	if err != nil {
		t.Fatalf("NewBedrockWithOptions() error: %v", err)
	}
	if p.Region() != "us-west-2" {
		t.Errorf("region = %q, want us-west-2", p.Region())
	}
}

func TestNewBedrockWithOptions_BearerTokenAuthHeaders(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	p, err := NewWithOptions(Options{
		BearerToken: " test-bearer-token ",
	})
	if err != nil {
		t.Fatalf("NewBedrockWithOptions() error: %v", err)
	}
	if p.Region() != defaultBedrockRegion {
		t.Errorf("region = %q, want us-east-1", p.Region())
	}

	headers := p.AuthHeaders()
	if got := headers["Authorization"]; got != "Bearer test-bearer-token" {
		t.Errorf("Authorization = %q, want Bearer test-bearer-token", got)
	}
}

func TestNewBedrockWithOptions_BearerTokenConfiguresSDKAuthScheme(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	p, err := NewWithOptions(Options{ //nolint:gosec // G101: static fake bearer token for tests, not a real credential
		BearerToken: "test-sdk-bearer-token",
	})
	if err != nil {
		t.Fatalf("NewBedrockWithOptions() error: %v", err)
	}

	client, ok := p.client.(realBedrockClient)
	if !ok {
		t.Fatalf("client type = %T, want realBedrockClient", p.client)
	}
	opts := client.Options()
	if !slices.Contains(opts.AuthSchemePreference, "httpBearerAuth") {
		t.Fatalf("AuthSchemePreference = %#v, want httpBearerAuth", opts.AuthSchemePreference)
	}
	if opts.BearerAuthTokenProvider == nil {
		t.Fatal("BearerAuthTokenProvider = nil, want configured provider")
	}
	token, err := opts.BearerAuthTokenProvider.RetrieveBearerToken(context.Background())
	if err != nil {
		t.Fatalf("RetrieveBearerToken() error: %v", err)
	}
	if token.Value != "test-sdk-bearer-token" {
		t.Errorf("bearer token = %q, want test-sdk-bearer-token", token.Value)
	}
}

func TestNewBedrockWithOptions_ExplicitBearerTokenOverridesSDKEnvToken(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "env-bearer-token")

	p, err := NewWithOptions(Options{
		BearerToken: "explicit-bearer-token",
	})
	if err != nil {
		t.Fatalf("NewBedrockWithOptions() error: %v", err)
	}

	client, ok := p.client.(realBedrockClient)
	if !ok {
		t.Fatalf("client type = %T, want realBedrockClient", p.client)
	}
	opts := client.Options()
	if !slices.Contains(opts.AuthSchemePreference, "httpBearerAuth") {
		t.Fatalf("AuthSchemePreference = %#v, want httpBearerAuth", opts.AuthSchemePreference)
	}
	token, err := opts.BearerAuthTokenProvider.RetrieveBearerToken(context.Background())
	if err != nil {
		t.Fatalf("RetrieveBearerToken() error: %v", err)
	}
	if token.Value != "explicit-bearer-token" {
		t.Errorf("bearer token = %q, want explicit-bearer-token", token.Value)
	}
	if got := p.AuthHeaders()["Authorization"]; got != "Bearer explicit-bearer-token" {
		t.Errorf("Authorization = %q, want Bearer explicit-bearer-token", got)
	}
}

func TestBedrockProvider_AuthHeaders_SigV4Default(t *testing.T) {
	p := &Provider{name: Name}
	if headers := p.AuthHeaders(); len(headers) != 0 {
		t.Errorf("AuthHeaders() = %#v, want empty map for SigV4 auth", headers)
	}
}

func TestNewBedrockWithOptions_InvalidStaticCredentials(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	_, err := NewWithOptions(Options{
		Region:      defaultBedrockRegion,
		AccessKeyID: "test-access-key",
	})
	if err == nil {
		t.Fatal("expected error for incomplete static credentials")
	}
	if !strings.Contains(err.Error(), "require both access key ID and secret access key") {
		t.Errorf("error = %q, want static-credentials validation message", err.Error())
	}
}

func TestBedrockProvider_Embed_Interface(_ *testing.T) {
	var _ core.EmbeddingProvider = (*Provider)(nil)
}

func TestBedrockProvider_SupportedEmbeddingModels(t *testing.T) {
	p := &Provider{name: Name}
	models := p.SupportedModels()
	for _, want := range []string{
		"amazon.titan-embed-text-v1",
		"amazon.titan-embed-text-v2:0",
		"cohere.embed-english-v3",
		"cohere.embed-multilingual-v3",
		"cohere.embed-v4:0",
	} {
		if !containsString(models, want) {
			t.Errorf("SupportedModels() missing %q", want)
		}
		if !p.SupportsModel(want) {
			t.Errorf("SupportsModel(%q) = false, want true", want)
		}
	}

	for _, wantSupported := range []string{
		"us.amazon.titan-embed-text-v2:0",
		"global.amazon.titan-embed-text-v2:0",
		"us-gov-east-1/amazon.titan-embed-text-v1",
		"cohere.embed-future:0",
	} {
		if !p.SupportsModel(wantSupported) {
			t.Errorf("SupportsModel(%q) = false, want true", wantSupported)
		}
	}

	for _, wantRejected := range []string{
		"text-embedding-3-small",
		"amazon.titan-embed-image-v1",
		"amazon.nova-2-multimodal-embeddings-v1:0",
	} {
		if p.SupportsModel(wantRejected) {
			t.Errorf("SupportsModel(%q) = true, want false", wantRejected)
		}
	}
}

func TestBedrockProvider_SupportsModel_CrossRegionInferenceProfiles(t *testing.T) {
	p := &Provider{name: Name}

	for _, model := range []string{
		"us.anthropic.claude-3-5-sonnet-20241022-v2:0",
		"eu.amazon.titan-text-express-v1",
		"apac.meta.llama3-1-70b-instruct-v1:0",
		"global.anthropic.claude-sonnet-4-20250514-v1:0",
		"us.amazon.nova-lite-v1:0",
		"global.amazon.nova-pro-v1:0",
		"global.amazon.nova-2-lite-v1:0",
		"us-gov-west-1/amazon.nova-pro-v1:0",
		"us-gov-west-1/amazon.titan-text-premier-v1:0",
	} {
		t.Run(model, func(t *testing.T) {
			if !p.SupportsModel(model) {
				t.Errorf("SupportsModel(%q) = false, want true", model)
			}
		})
	}
}

func TestBedrockProvider_SupportsModel_NovaTextModels(t *testing.T) {
	p := &Provider{name: Name}

	for _, model := range []string{
		"amazon.nova-micro-v1:0",
		"amazon.nova-lite-v1:0",
		"amazon.nova-pro-v1:0",
		"amazon.nova-premier-v1:0",
		"amazon.nova-2-lite-v1:0",
		"amazon.nova-2-pro-preview-20251202-v1:0",
		"us.amazon.nova-lite-v1:0",
		"eu.amazon.nova-micro-v1:0",
		"apac.amazon.nova-pro-v1:0",
		"global.amazon.nova-2-lite-v1:0",
		"us-gov-west-1/amazon.nova-pro-v1:0",
	} {
		t.Run(model, func(t *testing.T) {
			if !p.SupportsModel(model) {
				t.Errorf("SupportsModel(%q) = false, want true", model)
			}
		})
	}

	// amazon.nova-canvas is an image-generation model (see image.go); it is
	// intentionally not in the Nova-text set but IS a supported image model.
	for _, model := range []string{
		"amazon.nova-reel-v1:0",
		"amazon.nova-2-multimodal-embeddings-v1:0",
	} {
		t.Run(model, func(t *testing.T) {
			if p.SupportsModel(model) {
				t.Errorf("SupportsModel(%q) = true, want false", model)
			}
		})
	}
}

func TestBedrockProvider_Complete_CrossRegionInferenceProfileDispatchesWithOriginalModelID(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.Complete(context.Background(), core.Request{
		Model: "global.anthropic.claude-sonnet-4-20250514-v1:0",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if len(fake.invokeCalls) != 1 {
		t.Fatalf("InvokeModel calls = %d, want 1", len(fake.invokeCalls))
	}
	if got := aws.ToString(fake.invokeCalls[0].ModelId); got != "global.anthropic.claude-sonnet-4-20250514-v1:0" {
		t.Errorf("ModelId = %q, want original inference profile ID", got)
	}
	if resp.Model != "global.anthropic.claude-sonnet-4-20250514-v1:0" {
		t.Errorf("response Model = %q, want original inference profile ID", resp.Model)
	}
}

func TestBedrockProvider_Complete_NovaDispatchesWithOriginalModelID(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"hello"},{"text":" world"}]}},"stopReason":"end_turn","usage":{"inputTokens":3,"outputTokens":2,"totalTokens":5}}`),
		},
	}
	p := &Provider{name: Name, client: fake}
	maxTokens := 64
	temperature := 0.3
	topP := 0.9

	resp, err := p.Complete(context.Background(), core.Request{
		Model:       "global.amazon.nova-pro-v1:0",
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
		TopP:        &topP,
		Stop:        []string{"STOP"},
		Messages: []core.Message{
			{Role: core.RoleSystem, Content: "be concise"},
			{Role: core.RoleUser, Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if len(fake.invokeCalls) != 1 {
		t.Fatalf("InvokeModel calls = %d, want 1", len(fake.invokeCalls))
	}
	if got := aws.ToString(fake.invokeCalls[0].ModelId); got != "global.amazon.nova-pro-v1:0" {
		t.Errorf("ModelId = %q, want original inference profile ID", got)
	}
	var body bedrockNovaRequest
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if body.SchemaVersion != "messages-v1" {
		t.Errorf("schemaVersion = %q, want messages-v1", body.SchemaVersion)
	}
	if len(body.System) != 1 || body.System[0].Text != "be concise" {
		t.Errorf("system = %#v, want system prompt", body.System)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" || len(body.Messages[0].Content) != 1 || body.Messages[0].Content[0].Text != "hello" {
		t.Errorf("messages = %#v, want user text message", body.Messages)
	}
	if body.InferenceConfig == nil {
		t.Fatal("inferenceConfig = nil, want configured sampling params")
	}
	if body.InferenceConfig.MaxTokens != maxTokens || body.InferenceConfig.Temperature == nil || *body.InferenceConfig.Temperature != temperature || body.InferenceConfig.TopP == nil || *body.InferenceConfig.TopP != topP {
		t.Errorf("inferenceConfig = %+v, want configured sampling params", body.InferenceConfig)
	}
	if len(body.InferenceConfig.StopSequences) != 1 || body.InferenceConfig.StopSequences[0] != "STOP" {
		t.Errorf("stopSequences = %#v, want STOP", body.InferenceConfig.StopSequences)
	}
	if resp.Model != "global.amazon.nova-pro-v1:0" || resp.Choices[0].Message.Content != "hello world" || resp.Choices[0].FinishReason != "stop" {
		t.Errorf("response = %+v, want mapped Nova response with normalized finish reason", resp)
	}
	if resp.Usage.PromptTokens != 3 || resp.Usage.CompletionTokens != 2 || resp.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v, want Nova usage", resp.Usage)
	}
}

func TestBedrockProvider_Complete_NovaUsageFallsBackToInputPlusOutputTokens(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":2}}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.Complete(context.Background(), core.Request{
		Model: "amazon.nova-micro-v1:0",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Usage.TotalTokens != 3 {
		t.Errorf("TotalTokens = %d, want input+output fallback 3", resp.Usage.TotalTokens)
	}
}

func TestBedrockProvider_Complete_NovaNormalizesFinishReason(t *testing.T) {
	cases := []struct {
		name       string
		stopReason string
		want       string
	}{
		{name: "end_turn -> stop", stopReason: "end_turn", want: "stop"},
		{name: "max_tokens -> length", stopReason: "max_tokens", want: "length"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"output":{"message":{"role":"assistant","content":[{"text":"hi"}]}},"stopReason":%q,"usage":{"inputTokens":1,"outputTokens":1}}`, tc.stopReason)
			fake := &fakeBedrockRuntimeClient{responses: [][]byte{[]byte(body)}}
			p := &Provider{name: Name, client: fake}

			resp, err := p.Complete(context.Background(), core.Request{
				Model:    "amazon.nova-micro-v1:0",
				Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("Complete() error: %v", err)
			}
			if got := resp.Choices[0].FinishReason; got != tc.want {
				t.Errorf("finish_reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBedrockProvider_Embed_TitanTextLoopsAndMapsResponse(t *testing.T) {
	// embedTitan now issues one InvokeModel call per input text concurrently
	// (bounded by bedrockTitanEmbedConcurrency), so call arrival order is not
	// guaranteed. responseFor matches each response to its request by the
	// InputText it carries instead of by call index.
	responseByText := map[string][]byte{
		"first":  []byte(`{"embedding":[0.1,0.2],"inputTextTokenCount":2}`),
		"second": []byte(`{"embedding":[0.3,0.4],"inputTextTokenCount":3}`),
	}
	fake := &fakeBedrockRuntimeClient{
		responseFor: func(input *bedrockruntime.InvokeModelInput) ([]byte, error) {
			var req bedrockTitanEmbedRequest
			if err := json.Unmarshal(input.Body, &req); err != nil {
				return nil, err
			}
			body, ok := responseByText[req.InputText]
			if !ok {
				return nil, fmt.Errorf("no fake response for input text %q", req.InputText)
			}
			return body, nil
		},
	}
	p := &Provider{name: Name, client: fake}
	dimensions := 512

	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model:      "amazon.titan-embed-text-v2:0",
		Input:      []string{"first", "second"},
		Dimensions: &dimensions,
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}

	if len(fake.invokeCalls) != 2 {
		t.Fatalf("InvokeModel calls = %d, want 2", len(fake.invokeCalls))
	}
	seenTexts := make(map[string]bool)
	for i, call := range fake.invokeCalls {
		if got := aws.ToString(call.ModelId); got != "amazon.titan-embed-text-v2:0" {
			t.Errorf("call %d ModelId = %q", i, got)
		}
		if got := aws.ToString(call.ContentType); got != "application/json" {
			t.Errorf("call %d ContentType = %q", i, got)
		}
		if got := aws.ToString(call.Accept); got != "application/json" {
			t.Errorf("call %d Accept = %q", i, got)
		}
		var body bedrockTitanEmbedRequest
		mustUnmarshalBody(t, call.Body, &body)
		seenTexts[body.InputText] = true
		if body.Dimensions == nil || *body.Dimensions != dimensions {
			t.Errorf("call %d Titan dimensions = %v, want %d", i, body.Dimensions, dimensions)
		}
	}
	if !seenTexts["first"] || !seenTexts["second"] {
		t.Errorf("Titan inputText values = %v, want both %q and %q present", seenTexts, "first", "second")
	}

	if resp.Object != "list" || resp.Model != "amazon.titan-embed-text-v2:0" {
		t.Errorf("response metadata = (%q, %q), want (list, amazon.titan-embed-text-v2:0)", resp.Object, resp.Model)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("len(Data) = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].Index != 0 || resp.Data[0].Embedding[0] != 0.1 || resp.Data[1].Index != 1 || resp.Data[1].Embedding[0] != 0.3 {
		t.Errorf("embedding data = %+v, want order preserved", resp.Data)
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v, want 5 prompt/total tokens", resp.Usage)
	}
}

func TestBedrockProvider_Embed_CohereBatchesAndMapsArrayResponse(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"embeddings":[[0.1,0.2],[0.3,0.4]],"meta":{"billed_units":{"input_tokens":7}}}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "cohere.embed-english-v3",
		Input: []any{"first", "second"},
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}

	if len(fake.invokeCalls) != 1 {
		t.Fatalf("InvokeModel calls = %d, want 1", len(fake.invokeCalls))
	}
	var body bedrockCohereEmbedRequest
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if len(body.Texts) != 2 || body.Texts[0] != "first" || body.Texts[1] != "second" {
		t.Errorf("Cohere texts = %#v, want [first second]", body.Texts)
	}
	if body.InputType != "search_document" {
		t.Errorf("Cohere input_type = %q, want search_document", body.InputType)
	}
	if len(body.EmbeddingTypes) != 0 {
		t.Errorf("Cohere embedding_types = %#v, want omitted for v3", body.EmbeddingTypes)
	}

	if len(resp.Data) != 2 || resp.Data[0].Embedding[0] != 0.1 || resp.Data[1].Embedding[0] != 0.3 {
		t.Errorf("embedding data = %+v, want mapped array response", resp.Data)
	}
	if resp.Usage.PromptTokens != 7 || resp.Usage.TotalTokens != 7 {
		t.Errorf("usage = %+v, want 7 prompt/total tokens", resp.Usage)
	}
}

func TestBedrockProvider_Embed_CohereV4MapsTypedFloatResponse(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"embeddings":{"float":[[1.1,1.2]]},"meta":{"billed_units":{"input_tokens":4}}}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.Embed(context.Background(), core.EmbeddingRequest{
		Model: "cohere.embed-v4:0",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}

	var body bedrockCohereEmbedRequest
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if len(body.EmbeddingTypes) != 1 || body.EmbeddingTypes[0] != "float" {
		t.Errorf("Cohere v4 embedding_types = %#v, want [float]", body.EmbeddingTypes)
	}
	if len(resp.Data) != 1 || resp.Data[0].Embedding[0] != 1.1 {
		t.Errorf("embedding data = %+v, want typed float embeddings", resp.Data)
	}
}

func TestBedrockProvider_CompleteAnthropic_ForwardsToolsAndDecodesToolUse(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{
				"id":"msg_1",
				"type":"message",
				"role":"assistant",
				"content":[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"city":"SF"}}],
				"stop_reason":"tool_use",
				"usage":{"input_tokens":1,"output_tokens":1}
			}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather?"}},
		Tools: []core.Tool{{
			Type: "function",
			Function: core.Function{
				Name:        "lookup",
				Description: "Lookup weather",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
			},
		}},
		ToolChoice: "required",
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	var body bedrockAnthropicRequest
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if len(body.Tools) != 1 || body.Tools[0].Name != "lookup" {
		t.Fatalf("tools = %#v, want lookup", body.Tools)
	}
	choice, ok := body.ToolChoice.(map[string]any)
	if !ok || choice["type"] != "any" {
		t.Fatalf("tool_choice = %#v, want type any", body.ToolChoice)
	}
	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one", resp.Choices)
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Function.Name != "lookup" || tc.Function.Arguments != `{"city":"SF"}` {
		t.Fatalf("tool call = %#v, want lookup", tc)
	}
}

func TestBedrockProvider_CompleteAnthropic_ForwardsToolResultAndDecodesFinalAnswer(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{
				"id":"msg_2",
				"type":"message",
				"role":"assistant",
				"content":[{"type":"text","text":"It is 72F in SF."}],
				"stop_reason":"end_turn",
				"usage":{"input_tokens":2,"output_tokens":3}
			}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.Complete(context.Background(), core.Request{
		Model: "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "weather?"},
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{
				ID:       "toolu_1",
				Type:     "function",
				Function: core.FunctionCall{Name: "lookup", Arguments: `{"city":"SF"}`},
			}}},
			{Role: core.RoleTool, ToolCallID: "toolu_1", Content: `{"temp":"72F"}`},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if len(body.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(body.Messages))
	}
	var resultBlocks []map[string]json.RawMessage
	if err := json.Unmarshal(body.Messages[2].Content, &resultBlocks); err != nil {
		t.Fatalf("decode tool result blocks: %v", err)
	}
	if body.Messages[2].Role != core.RoleUser || jsonString(resultBlocks[0]["type"]) != "tool_result" || jsonString(resultBlocks[0]["tool_use_id"]) != "toolu_1" {
		t.Fatalf("tool result message = %#v blocks=%#v", body.Messages[2], resultBlocks)
	}
	if got := resp.Choices[0].Message.Content; got != "It is 72F in SF." {
		t.Fatalf("final answer = %q, want weather answer", got)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 0 {
		t.Fatalf("final answer tool calls = %#v, want none", resp.Choices[0].Message.ToolCalls)
	}
}

func TestBedrockProvider_CompleteAnthropic_MergesParallelToolResults(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{
				"id":"msg_2",
				"type":"message",
				"role":"assistant",
				"content":[{"type":"text","text":"done"}],
				"stop_reason":"end_turn",
				"usage":{"input_tokens":2,"output_tokens":3}
			}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	_, err := p.Complete(context.Background(), core.Request{
		Model: "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Messages: []core.Message{
			{Role: core.RoleUser, Content: "weather and time?"},
			{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{
				{ID: "toolu_weather", Type: "function", Function: core.FunctionCall{Name: "weather", Arguments: `{"city":"SF"}`}},
				{ID: "toolu_time", Type: "function", Function: core.FunctionCall{Name: "time", Arguments: `{"city":"SF"}`}},
			}},
			{Role: core.RoleTool, ToolCallID: "toolu_weather", Content: `{"temp":"72F"}`},
			{Role: core.RoleTool, ToolCallID: "toolu_time", Content: `{"time":"10:00"}`},
		},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}

	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if len(body.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3 with merged tool results", len(body.Messages))
	}
	var resultBlocks []map[string]json.RawMessage
	if err := json.Unmarshal(body.Messages[2].Content, &resultBlocks); err != nil {
		t.Fatalf("decode tool result blocks: %v", err)
	}
	if body.Messages[2].Role != core.RoleUser || len(resultBlocks) != 2 {
		t.Fatalf("tool result message = %#v blocks=%#v, want one user turn with two tool results", body.Messages[2], resultBlocks)
	}
	if jsonString(resultBlocks[0]["tool_use_id"]) != "toolu_weather" || jsonString(resultBlocks[1]["tool_use_id"]) != "toolu_time" {
		t.Fatalf("tool result ids = %#v, want weather/time", resultBlocks)
	}
}

func TestBedrockProvider_CompleteStreamAnthropic_ForwardsToolUseDeltas(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		streamResponses: [][]byte{
			[]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`),
			[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\""}}`),
			[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"SF\"}"}}`),
			[]byte(`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather?"}},
		Tools: []core.Tool{{
			Type: "function",
			Function: core.Function{
				Name: "lookup",
			},
		}},
		ToolChoice: "required",
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}

	if len(chunks) != 4 {
		t.Fatalf("chunks len = %d, want 4: %#v", len(chunks), chunks)
	}
	start := chunks[0].Choices[0].Delta.ToolCalls[0]
	if start.Index == nil || *start.Index != 0 || start.ID != "toolu_1" || start.Function.Name != "lookup" {
		t.Fatalf("start tool call = %#v, want lookup at index 0", start)
	}
	if chunks[1].Choices[0].Delta.ToolCalls[0].Function.Arguments != `{"city"` {
		t.Fatalf("first args delta = %#v", chunks[1].Choices[0].Delta.ToolCalls)
	}
	if chunks[2].Choices[0].Delta.ToolCalls[0].Function.Arguments != `:"SF"}` {
		t.Fatalf("second args delta = %#v", chunks[2].Choices[0].Delta.ToolCalls)
	}
	if chunks[3].Choices[0].FinishReason != core.FinishReasonToolCalls {
		t.Fatalf("finish_reason = %q, want %q", chunks[3].Choices[0].FinishReason, core.FinishReasonToolCalls)
	}
}

func TestBedrockProvider_CompleteStreamAnthropic_MapsContentBlockIndexToToolCallIndex(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		streamResponses: [][]byte{
			[]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me check."}}`),
			[]byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`),
			[]byte(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\""}}`),
			[]byte(`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"lookup_time","input":{}}}`),
			[]byte(`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\""}}`),
			[]byte(`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`),
		},
	}
	p := &Provider{name: Name, client: fake}

	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Messages: []core.Message{{Role: core.RoleUser, Content: "weather?"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}

	if len(chunks) != 6 {
		t.Fatalf("chunks len = %d, want 6: %#v", len(chunks), chunks)
	}
	start := chunks[1].Choices[0].Delta.ToolCalls[0]
	if chunks[1].Choices[0].Index != 0 {
		t.Fatalf("choice index = %d, want sole completion index 0", chunks[1].Choices[0].Index)
	}
	if start.Index == nil || *start.Index != 0 {
		t.Fatalf("tool call index = %#v, want OpenAI tool index 0", start.Index)
	}
	args := chunks[2].Choices[0].Delta.ToolCalls[0]
	if chunks[2].Choices[0].Index != 0 {
		t.Fatalf("args choice index = %d, want sole completion index 0", chunks[2].Choices[0].Index)
	}
	if args.Index == nil || *args.Index != 0 || args.Function.Arguments != `{"city"` {
		t.Fatalf("args delta = %#v, want tool index 0 with city fragment", args)
	}
	secondStart := chunks[3].Choices[0].Delta.ToolCalls[0]
	if chunks[3].Choices[0].Index != 0 {
		t.Fatalf("second choice index = %d, want sole completion index 0", chunks[3].Choices[0].Index)
	}
	if secondStart.Index == nil || *secondStart.Index != 1 {
		t.Fatalf("second tool call index = %#v, want OpenAI tool index 1", secondStart.Index)
	}
	secondArgs := chunks[4].Choices[0].Delta.ToolCalls[0]
	if chunks[4].Choices[0].Index != 0 {
		t.Fatalf("second args choice index = %d, want sole completion index 0", chunks[4].Choices[0].Index)
	}
	if secondArgs.Index == nil || *secondArgs.Index != 1 || secondArgs.Function.Arguments != `{"city"` {
		t.Fatalf("second args delta = %#v, want tool index 1 with city fragment", secondArgs)
	}
}

func TestBedrockProvider_Embed_Validation(t *testing.T) {
	cases := []struct {
		name string
		req  core.EmbeddingRequest
	}{
		{
			name: "nil input",
			req:  core.EmbeddingRequest{Model: "amazon.titan-embed-text-v1", Input: nil},
		},
		{
			name: "unsupported input type",
			req:  core.EmbeddingRequest{Model: "amazon.titan-embed-text-v1", Input: 42},
		},
		{
			name: "empty string slice",
			req:  core.EmbeddingRequest{Model: "amazon.titan-embed-text-v1", Input: []string{}},
		},
		{
			name: "empty interface slice",
			req:  core.EmbeddingRequest{Model: "amazon.titan-embed-text-v1", Input: []any{}},
		},
		{
			name: "non-string interface item",
			req:  core.EmbeddingRequest{Model: "amazon.titan-embed-text-v1", Input: []any{"ok", 42}},
		},
		{
			name: "base64 encoding",
			req:  core.EmbeddingRequest{Model: "amazon.titan-embed-text-v1", Input: "hello", EncodingFormat: "base64"},
		},
		{
			name: "unsupported model family",
			req:  core.EmbeddingRequest{Model: "amazon.titan-embed-image-v1", Input: "hello"},
		},
		{
			name: "dimensions on cohere",
			req:  core.EmbeddingRequest{Model: "cohere.embed-english-v3", Input: "hello", Dimensions: intPtr(256)},
		},
		{
			name: "dimensions on titan v1",
			req:  core.EmbeddingRequest{Model: "amazon.titan-embed-text-v1", Input: "hello", Dimensions: intPtr(256)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeBedrockRuntimeClient{}
			p := &Provider{name: Name, client: fake}
			_, err := p.Embed(context.Background(), tc.req)
			if err == nil {
				t.Fatal("Embed() error = nil, want error")
			}
			if len(fake.invokeCalls) != 0 {
				t.Fatalf("InvokeModel calls = %d, want 0 for validation error", len(fake.invokeCalls))
			}
		})
	}
}

type fakeBedrockRuntimeClient struct {
	mu                sync.Mutex
	invokeCalls       []*bedrockruntime.InvokeModelInput
	invokeStreamCalls []*bedrockruntime.InvokeModelWithResponseStreamInput
	responses         [][]byte
	streamResponses   [][]byte
	err               error
	// responseFor, when set, selects the response body for a given call
	// instead of the index-based responses slice. It lets tests that issue
	// concurrent InvokeModel calls (e.g. bedrock_embed.go's parallel Titan
	// requests) match a response to its request deterministically rather than
	// relying on call arrival order, which is not guaranteed under concurrency.
	responseFor func(*bedrockruntime.InvokeModelInput) ([]byte, error)
}

func (f *fakeBedrockRuntimeClient) InvokeModel(_ context.Context, input *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	copied := *input
	copied.Body = append([]byte(nil), input.Body...)

	f.mu.Lock()
	f.invokeCalls = append(f.invokeCalls, &copied)
	idx := len(f.invokeCalls) - 1
	responseFor := f.responseFor
	callErr := f.err
	responses := f.responses
	f.mu.Unlock()

	if callErr != nil {
		return nil, callErr
	}
	if responseFor != nil {
		body, err := responseFor(&copied)
		if err != nil {
			return nil, err
		}
		return &bedrockruntime.InvokeModelOutput{Body: body}, nil
	}
	if idx >= len(responses) {
		return nil, fmt.Errorf("missing fake response for call %d", idx)
	}
	return &bedrockruntime.InvokeModelOutput{Body: responses[idx]}, nil
}

func (f *fakeBedrockRuntimeClient) InvokeModelWithResponseStream(_ context.Context, input *bedrockruntime.InvokeModelWithResponseStreamInput, _ ...func(*bedrockruntime.Options)) (bedrockEventStream, error) {
	copied := *input
	copied.Body = append([]byte(nil), input.Body...)
	f.invokeStreamCalls = append(f.invokeStreamCalls, &copied)
	if f.err != nil {
		return nil, f.err
	}
	events := make(chan types.ResponseStream, len(f.streamResponses))
	for _, body := range f.streamResponses {
		events <- &types.ResponseStreamMemberChunk{Value: types.PayloadPart{Bytes: body}}
	}
	close(events)
	return fakeBedrockResponseStreamReader{events: events}, nil
}

type fakeBedrockResponseStreamReader struct {
	events <-chan types.ResponseStream
}

func (f fakeBedrockResponseStreamReader) Events() <-chan types.ResponseStream {
	return f.events
}

func (f fakeBedrockResponseStreamReader) Close() error {
	return nil
}

func (f fakeBedrockResponseStreamReader) Err() error {
	return nil
}

func mustUnmarshalBody(t *testing.T, body []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("failed to unmarshal body %s: %v", string(body), err)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func intPtr(v int) *int { return &v }

func jsonString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}
