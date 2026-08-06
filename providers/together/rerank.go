package together

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// togetherRerankRequest is the Cohere-standard rerank body Together AI accepts
// verbatim on /rerank.
type togetherRerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            *int     `json:"top_n,omitempty"`
	ReturnDocuments *bool    `json:"return_documents,omitempty"`
	MaxTokensPerDoc *int     `json:"max_tokens_per_doc,omitempty"`
}

type togetherRerankResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Document       *struct {
			Text string `json:"text"`
		} `json:"document"`
	} `json:"results"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Rerank ranks documents against a query via Together AI's /rerank endpoint,
// which speaks the Cohere-standard shape — and, unlike Cohere v2, echoes the
// document text when return_documents is set, so it is forwarded through.
func (p *Provider) Rerank(ctx context.Context, req core.RerankRequest) (*core.RerankResponse, error) {
	togReq := togetherRerankRequest{
		Model:           req.Model,
		Query:           req.Query,
		Documents:       req.Documents,
		TopN:            req.TopN,
		ReturnDocuments: req.ReturnDocuments,
		MaxTokensPerDoc: req.MaxTokensPerDoc,
	}

	bodyReader, _, release, err := core.JSONBodyReader(togReq)
	if err != nil {
		return nil, fmt.Errorf("together: failed to marshal rerank request: %w", err)
	}
	defer release()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/rerank", bodyReader)
	if err != nil {
		return nil, fmt.Errorf("together: failed to create rerank request: %w", err)
	}
	for k, v := range p.headers() {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("together: rerank request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("together: failed to read rerank response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIErrorFromResponse(p.name, httpResp, respBody)
	}

	var decoded togetherRerankResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("together: failed to decode rerank response: %w", err)
	}

	results := make([]core.RerankResult, len(decoded.Results))
	for i, r := range decoded.Results {
		res := core.RerankResult{Index: r.Index, RelevanceScore: r.RelevanceScore}
		if r.Document != nil {
			res.Document = &core.RerankDocument{Text: r.Document.Text}
		}
		results[i] = res
	}

	resp := &core.RerankResponse{ID: decoded.ID, Results: results, Model: req.Model}
	if decoded.Usage != nil {
		resp.Meta = &core.RerankMeta{Tokens: &core.RerankTokens{
			InputTokens:  decoded.Usage.PromptTokens,
			OutputTokens: decoded.Usage.CompletionTokens,
		}}
	}
	return resp, nil
}
