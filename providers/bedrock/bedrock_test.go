package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

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
	if p.Region() != "us-east-1" {
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

func TestNewBedrockWithOptions_InvalidStaticCredentials(t *testing.T) {
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	_, err := NewWithOptions(Options{
		Region:      "us-east-1",
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

func TestBedrockProvider_Embed_TitanTextLoopsAndMapsResponse(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{
			[]byte(`{"embedding":[0.1,0.2],"inputTextTokenCount":2}`),
			[]byte(`{"embedding":[0.3,0.4],"inputTextTokenCount":3}`),
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
	}

	var firstReq, secondReq bedrockTitanEmbedRequest
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &firstReq)
	mustUnmarshalBody(t, fake.invokeCalls[1].Body, &secondReq)
	if firstReq.InputText != "first" || secondReq.InputText != "second" {
		t.Errorf("Titan inputText values = %q, %q; want first, second", firstReq.InputText, secondReq.InputText)
	}
	if firstReq.Dimensions == nil || *firstReq.Dimensions != dimensions {
		t.Errorf("Titan dimensions = %v, want %d", firstReq.Dimensions, dimensions)
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
		Input: []interface{}{"first", "second"},
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
			req:  core.EmbeddingRequest{Model: "amazon.titan-embed-text-v1", Input: []interface{}{}},
		},
		{
			name: "non-string interface item",
			req:  core.EmbeddingRequest{Model: "amazon.titan-embed-text-v1", Input: []interface{}{"ok", 42}},
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
	invokeCalls []*bedrockruntime.InvokeModelInput
	responses   [][]byte
	err         error
}

func (f *fakeBedrockRuntimeClient) InvokeModel(_ context.Context, input *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	copied := *input
	copied.Body = append([]byte(nil), input.Body...)
	f.invokeCalls = append(f.invokeCalls, &copied)
	if f.err != nil {
		return nil, f.err
	}
	idx := len(f.invokeCalls) - 1
	if idx >= len(f.responses) {
		return nil, fmt.Errorf("missing fake response for call %d", idx)
	}
	return &bedrockruntime.InvokeModelOutput{Body: f.responses[idx]}, nil
}

func (f *fakeBedrockRuntimeClient) InvokeModelWithResponseStream(context.Context, *bedrockruntime.InvokeModelWithResponseStreamInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelWithResponseStreamOutput, error) {
	return nil, fmt.Errorf("streaming not implemented in fake")
}

func mustUnmarshalBody(t *testing.T, body []byte, out interface{}) {
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
