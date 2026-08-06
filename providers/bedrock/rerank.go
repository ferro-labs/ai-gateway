package bedrock

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	agenttypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// rerankModelArn resolves a rerank model id to the foundation-model ARN the
// agent-runtime API requires. An id that is already an ARN is used verbatim.
func (p *Provider) rerankModelArn(model string) string {
	if strings.HasPrefix(model, "arn:") {
		return model
	}
	return fmt.Sprintf("arn:aws:bedrock:%s::foundation-model/%s", p.region, model)
}

// Rerank ranks documents against a query via the Bedrock agent-runtime Rerank
// API (amazon.rerank / cohere.rerank models), translating the gateway-standard
// Cohere shape to and from the AWS typed request/response. Bedrock accepts
// exactly one query, which is the gateway contract's single-query shape.
func (p *Provider) Rerank(ctx context.Context, req core.RerankRequest) (*core.RerankResponse, error) {
	if len(req.Documents) == 0 {
		return nil, core.StatusError(Name, http.StatusBadRequest, "bedrock: rerank requires at least one document")
	}
	// Mirror core.RankedResults, which the other gateway-capped adapters route
	// through: a negative top_n cannot name a count, and top_n == 0 legitimately
	// caps to none — answered here without paying for an upstream call.
	if req.TopN != nil && *req.TopN < 0 {
		return nil, core.StatusError(Name, http.StatusBadRequest,
			fmt.Sprintf("bedrock: top_n must not be negative, got %d", *req.TopN))
	}
	if req.TopN != nil && *req.TopN == 0 {
		return &core.RerankResponse{Model: req.Model, Results: []core.RerankResult{}}, nil
	}

	sources := make([]agenttypes.RerankSource, len(req.Documents))
	for i, d := range req.Documents {
		sources[i] = agenttypes.RerankSource{
			Type: agenttypes.RerankSourceTypeInline,
			InlineDocumentSource: &agenttypes.RerankDocument{
				Type:         agenttypes.RerankDocumentTypeText,
				TextDocument: &agenttypes.RerankTextDocument{Text: aws.String(d)},
			},
		}
	}

	rerankCfg := &agenttypes.RerankingConfiguration{
		Type: agenttypes.RerankingConfigurationTypeBedrockRerankingModel,
		BedrockRerankingConfiguration: &agenttypes.BedrockRerankingConfiguration{
			ModelConfiguration: &agenttypes.BedrockRerankingModelConfiguration{
				ModelArn: aws.String(p.rerankModelArn(req.Model)),
			},
		},
	}
	if req.TopN != nil {
		// Asking for more results than there are documents returns them all, so
		// clamp to the document count — which also bounds the int32 conversion.
		n := min(*req.TopN, len(req.Documents))
		rerankCfg.BedrockRerankingConfiguration.NumberOfResults = aws.Int32(int32(n)) //nolint:gosec // n is clamped to len(documents), a small result count
	}

	out, err := p.rerankClient.Rerank(ctx, &bedrockagentruntime.RerankInput{
		Queries: []agenttypes.RerankQuery{{
			Type:      agenttypes.RerankQueryContentTypeText,
			TextQuery: &agenttypes.RerankTextDocument{Text: aws.String(req.Query)},
		}},
		Sources:                sources,
		RerankingConfiguration: rerankCfg,
	})
	if err != nil {
		return nil, bedrockInvokeError("rerank", err)
	}

	returnDocs := req.ReturnDocuments != nil && *req.ReturnDocuments
	results := make([]core.RerankResult, len(out.Results))
	for i, r := range out.Results {
		res := core.RerankResult{
			Index:          int(aws.ToInt32(r.Index)),
			RelevanceScore: float64(aws.ToFloat32(r.RelevanceScore)),
		}
		// Bedrock can echo the document, but mirror Cohere: only when asked, and
		// fill from the original array by index so the text is exact.
		if returnDocs {
			if idx := res.Index; idx >= 0 && idx < len(req.Documents) {
				res.Document = &core.RerankDocument{Text: req.Documents[idx]}
			}
		}
		results[i] = res
	}

	// Bedrock returns results already sorted best-first; preserve that order.
	return &core.RerankResponse{Model: req.Model, Results: results}, nil
}
