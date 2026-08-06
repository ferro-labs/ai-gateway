package nvidianim

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

type nvidiaText struct {
	Text string `json:"text"`
}

type nvidiaRerankRequest struct {
	Model    string       `json:"model"`
	Query    nvidiaText   `json:"query"`
	Passages []nvidiaText `json:"passages"`
	Truncate string       `json:"truncate,omitempty"`
}

type nvidiaRerankResponse struct {
	Rankings []struct {
		Index int     `json:"index"`
		Logit float64 `json:"logit"`
	} `json:"rankings"`
	Usage *struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// Rerank ranks documents against a query via NVIDIA's native reranking endpoint
// (p.rerankURL, resolved at construction — see hostedRerankURL), translating its
// logit-scored rankings into the gateway-standard Cohere shape. NVIDIA has no
// native top_n / return_documents, so both are applied here via core.RankedResults.
func (p *Provider) Rerank(ctx context.Context, req core.RerankRequest) (*core.RerankResponse, error) {
	if len(req.Documents) == 0 {
		return nil, fmt.Errorf("nvidia-nim: rerank requires at least one document")
	}

	passages := make([]nvidiaText, len(req.Documents))
	for i, d := range req.Documents {
		passages[i] = nvidiaText{Text: d}
	}
	nvReq := nvidiaRerankRequest{
		Model:    req.Model,
		Query:    nvidiaText{Text: req.Query},
		Passages: passages,
		// END truncation avoids a 400 on inputs longer than the model's context.
		Truncate: "END",
	}

	bodyReader, _, release, err := core.JSONBodyReader(nvReq)
	if err != nil {
		return nil, fmt.Errorf("nvidia-nim: failed to marshal rerank request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.rerankURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("nvidia-nim: failed to create rerank request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("nvidia-nim: rerank request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("nvidia-nim: failed to read rerank response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIErrorFromResponse(p.name, httpResp, respBody)
	}

	var decoded nvidiaRerankResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("nvidia-nim: failed to decode rerank response: %w", err)
	}

	results := make([]core.RerankResult, len(decoded.Rankings))
	for i, r := range decoded.Rankings {
		// NVIDIA returns a raw, unbounded logit; map it to a 0..1 relevance via
		// the logistic function so the gateway's relevance_score contract holds.
		results[i] = core.RerankResult{Index: r.Index, RelevanceScore: 1.0 / (1.0 + math.Exp(-r.Logit))}
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
	if decoded.Usage != nil {
		resp.Meta = &core.RerankMeta{Tokens: &core.RerankTokens{InputTokens: decoded.Usage.PromptTokens}}
	}
	return resp, nil
}
