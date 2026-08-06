package deepinfra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

type deepInfraRerankRequest struct {
	Queries   []string `json:"queries"`
	Documents []string `json:"documents"`
}

type deepInfraRerankResponse struct {
	Scores      []float64 `json:"scores"`
	InputTokens int       `json:"input_tokens"`
}

// Rerank ranks documents against a query via DeepInfra's native reranker
// inference endpoint, translating its raw score list into the gateway-standard
// Cohere shape. DeepInfra returns one score per document in input order and has
// no native top_n / return_documents, so both are applied here via
// core.RankedResults.
func (p *Provider) Rerank(ctx context.Context, req core.RerankRequest) (*core.RerankResponse, error) {
	if len(req.Documents) == 0 {
		return nil, fmt.Errorf("deepinfra: rerank requires at least one document")
	}

	diReq := deepInfraRerankRequest{Queries: []string{req.Query}, Documents: req.Documents}
	bodyReader, _, release, err := core.JSONBodyReader(diReq)
	if err != nil {
		return nil, fmt.Errorf("deepinfra: failed to marshal rerank request: %w", err)
	}
	defer release()

	// Rerank lives on DeepInfra's native inference root, resolved once in New
	// alongside the OpenAI-compat base.
	url := p.inferenceBaseURL + "/" + req.Model
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("deepinfra: failed to create rerank request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("deepinfra: rerank request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("deepinfra: failed to read rerank response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIErrorFromResponse(p.name, httpResp, respBody)
	}

	var decoded deepInfraRerankResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("deepinfra: failed to decode rerank response: %w", err)
	}
	// One score per document, in input order; a mismatch means the scores cannot
	// be paired to documents, so fail rather than mis-rank.
	if len(decoded.Scores) != len(req.Documents) {
		return nil, fmt.Errorf("deepinfra: rerank returned %d scores for %d documents", len(decoded.Scores), len(req.Documents))
	}

	results := make([]core.RerankResult, len(decoded.Scores))
	for i, s := range decoded.Scores {
		results[i] = core.RerankResult{Index: i, RelevanceScore: s}
	}
	returnDocs := req.ReturnDocuments != nil && *req.ReturnDocuments

	ranked, err := core.RankedResults(req.Documents, results, req.TopN, returnDocs)
	if err != nil {
		return nil, err
	}

	resp := &core.RerankResponse{
		Model:   req.Model,
		Results: ranked,
	}
	if decoded.InputTokens > 0 {
		resp.Meta = &core.RerankMeta{Tokens: &core.RerankTokens{InputTokens: decoded.InputTokens}}
	}
	return resp, nil
}
