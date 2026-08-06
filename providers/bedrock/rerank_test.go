package bedrock

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	agenttypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

type fakeRerankClient struct {
	got *bedrockagentruntime.RerankInput
	out *bedrockagentruntime.RerankOutput
	err error
}

func (f *fakeRerankClient) Rerank(_ context.Context, in *bedrockagentruntime.RerankInput, _ ...func(*bedrockagentruntime.Options)) (*bedrockagentruntime.RerankOutput, error) {
	f.got = in
	return f.out, f.err
}

func TestBedrockProvider_Rerank_Interface(_ *testing.T) {
	var _ core.RerankProvider = (*Provider)(nil)
}

func TestBedrockProvider_Rerank_Translation(t *testing.T) {
	fake := &fakeRerankClient{
		out: &bedrockagentruntime.RerankOutput{
			Results: []agenttypes.RerankResult{
				{Index: aws.Int32(1), RelevanceScore: aws.Float32(0.9)},
				{Index: aws.Int32(0), RelevanceScore: aws.Float32(0.1)},
			},
		},
	}
	p := &Provider{name: Name, region: "us-west-2", rerankClient: fake}

	ret := true
	two := 2
	resp, err := p.Rerank(context.Background(), core.RerankRequest{
		Model:           "amazon.rerank-v1:0",
		Query:           "capital?",
		Documents:       []string{"Carson City", "Washington DC"},
		TopN:            &two,
		ReturnDocuments: &ret,
	})
	if err != nil {
		t.Fatalf("Rerank() error: %v", err)
	}

	// Request translation: one query, two inline sources, the resolved ARN, and
	// numberOfResults from top_n.
	if len(fake.got.Queries) != 1 || aws.ToString(fake.got.Queries[0].TextQuery.Text) != "capital?" {
		t.Errorf("query translation wrong: %+v", fake.got.Queries)
	}
	if len(fake.got.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(fake.got.Sources))
	}
	wantArn := "arn:aws:bedrock:us-west-2::foundation-model/amazon.rerank-v1:0"
	if got := aws.ToString(fake.got.RerankingConfiguration.BedrockRerankingConfiguration.ModelConfiguration.ModelArn); got != wantArn {
		t.Errorf("modelArn = %q, want %q", got, wantArn)
	}
	if n := fake.got.RerankingConfiguration.BedrockRerankingConfiguration.NumberOfResults; n == nil || *n != 2 {
		t.Errorf("numberOfResults = %v, want 2", n)
	}

	// Response translation.
	if len(resp.Results) != 2 || resp.Results[0].Index != 1 || resp.Results[0].RelevanceScore != float64(float32(0.9)) {
		t.Errorf("results = %+v", resp.Results)
	}
	if resp.Results[0].Document == nil || resp.Results[0].Document.Text != "Washington DC" {
		t.Errorf("document = %+v, want re-attached 'Washington DC'", resp.Results[0].Document)
	}
}

func TestBedrockProvider_Rerank_TopNBounds(t *testing.T) {
	t.Run("zero caps to no results without an upstream call", func(t *testing.T) {
		fake := &fakeRerankClient{out: &bedrockagentruntime.RerankOutput{
			Results: []agenttypes.RerankResult{{Index: aws.Int32(0), RelevanceScore: aws.Float32(0.5)}},
		}}
		p := &Provider{name: Name, region: "us-west-2", rerankClient: fake}

		zero := 0
		resp, err := p.Rerank(context.Background(), core.RerankRequest{
			Model:     "amazon.rerank-v1:0",
			Query:     "q",
			Documents: []string{"d1", "d2"},
			TopN:      &zero,
		})
		if err != nil {
			t.Fatalf("Rerank() error: %v", err)
		}
		if len(resp.Results) != 0 {
			t.Errorf("results = %+v, want none for top_n 0", resp.Results)
		}
		if fake.got != nil {
			t.Errorf("upstream was called for top_n 0; zero results need no rerank")
		}
	})

	t.Run("negative is rejected as a client error", func(t *testing.T) {
		fake := &fakeRerankClient{out: &bedrockagentruntime.RerankOutput{}}
		p := &Provider{name: Name, region: "us-west-2", rerankClient: fake}

		neg := -1
		_, err := p.Rerank(context.Background(), core.RerankRequest{
			Model:     "amazon.rerank-v1:0",
			Query:     "q",
			Documents: []string{"d"},
			TopN:      &neg,
		})
		var statusErr *core.HTTPStatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadRequest {
			t.Fatalf("Rerank() error = %v, want a 400 HTTPStatusError", err)
		}
	})
}

func TestBedrockProvider_Rerank_ArnPassthrough(t *testing.T) {
	fake := &fakeRerankClient{out: &bedrockagentruntime.RerankOutput{}}
	p := &Provider{name: Name, region: "us-east-1", rerankClient: fake}

	arn := "arn:aws:bedrock:eu-west-1::foundation-model/cohere.rerank-v3-5:0"
	if _, err := p.Rerank(context.Background(), core.RerankRequest{Model: arn, Query: "q", Documents: []string{"d"}}); err != nil {
		t.Fatalf("Rerank() error: %v", err)
	}
	if got := aws.ToString(fake.got.RerankingConfiguration.BedrockRerankingConfiguration.ModelConfiguration.ModelArn); got != arn {
		t.Errorf("ARN not passed through verbatim: %q", got)
	}
}
