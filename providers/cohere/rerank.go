package cohere

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// cohereRerankRequest is Cohere's native v2/rerank body. It IS the gateway
// standard, minus the fields v2 does not accept: return_documents (v2 never
// echoes documents) is handled gateway-side by re-attaching text below.
type cohereRerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            *int     `json:"top_n,omitempty"`
	MaxTokensPerDoc *int     `json:"max_tokens_per_doc,omitempty"`
}

type cohereRerankResponse struct {
	ID      string `json:"id"`
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
	Meta *struct {
		BilledUnits *struct {
			SearchUnits int `json:"search_units"`
		} `json:"billed_units"`
	} `json:"meta"`
}

// Rerank ranks documents against a query via Cohere's v2/rerank endpoint —
// Cohere IS the reference rerank shape, so this is a near-passthrough.
func (p *Provider) Rerank(ctx context.Context, req core.RerankRequest) (*core.RerankResponse, error) {
	cohReq := cohereRerankRequest{
		Model:           req.Model,
		Query:           req.Query,
		Documents:       req.Documents,
		TopN:            req.TopN,
		MaxTokensPerDoc: req.MaxTokensPerDoc,
	}

	bodyReader, _, release, err := core.JSONBodyReader(cohReq)
	if err != nil {
		return nil, fmt.Errorf("cohere: failed to marshal rerank request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v2/rerank", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("cohere: failed to create rerank request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("cohere: rerank request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("cohere: failed to read rerank response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, cohereAPIError(httpResp, respBody)
	}

	var decoded cohereRerankResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("cohere: failed to decode rerank response: %w", err)
	}

	returnDocs := req.ReturnDocuments != nil && *req.ReturnDocuments
	results := make([]core.RerankResult, len(decoded.Results))
	for i, r := range decoded.Results {
		res := core.RerankResult{Index: r.Index, RelevanceScore: r.RelevanceScore}
		// Cohere v2 does not echo document text; re-attach it from the original
		// request when the caller asked for it, so the gateway's return_documents
		// contract holds regardless of the upstream's shape.
		if returnDocs && r.Index >= 0 && r.Index < len(req.Documents) {
			res.Document = &core.RerankDocument{Text: req.Documents[r.Index]}
		}
		results[i] = res
	}

	resp := &core.RerankResponse{ID: decoded.ID, Results: results, Model: req.Model}
	if decoded.Meta != nil && decoded.Meta.BilledUnits != nil {
		resp.Meta = &core.RerankMeta{
			BilledUnits: &core.RerankBilledUnits{SearchUnits: decoded.Meta.BilledUnits.SearchUnits},
		}
	}
	return resp, nil
}
