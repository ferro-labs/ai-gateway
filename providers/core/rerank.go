package core

import (
	"fmt"
	"sort"
)

// RerankRequest is the gateway-facing rerank request. It adopts the Cohere v2
// /rerank shape verbatim — the de-facto standard LiteLLM and Portkey both expose
// — so an OpenAI-adjacent client already targeting those gateways works
// unchanged. Provider adapters translate this onto their native shape.
//
// Documents is the canonical []string form (Cohere v2 takes plain strings). The
// Cohere v1 object form ([{"text":…}]) is a documented future convenience, not
// accepted here yet.
type RerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	// TopN caps the number of ranked results; nil returns all, ranked.
	TopN *int `json:"top_n,omitempty"`
	// ReturnDocuments, when true, echoes each ranked document's text back in the
	// result. Defaults to false (Cohere v2 behaviour) to keep payloads small.
	ReturnDocuments *bool `json:"return_documents,omitempty"`
	// MaxTokensPerDoc bounds per-document truncation (Cohere v2, default 4096).
	MaxTokensPerDoc *int `json:"max_tokens_per_doc,omitempty"`
}

// RerankResponse is the gateway-standard rerank result: documents ranked by
// descending relevance.
type RerankResponse struct {
	ID      string         `json:"id,omitempty"`
	Results []RerankResult `json:"results"`
	Model   string         `json:"model,omitempty"`
	Meta    *RerankMeta    `json:"meta,omitempty"`
}

// RerankResult is one ranked document.
type RerankResult struct {
	// Index is the 0-based position of this document in the ORIGINAL request
	// documents array.
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
	// Document is present only when the request set return_documents=true.
	Document *RerankDocument `json:"document,omitempty"`
}

// RerankDocument echoes a ranked document's text.
type RerankDocument struct {
	Text string `json:"text"`
}

// RerankMeta carries billing/usage a provider reports for a rerank call. Rerank
// bills in search-units rather than tokens.
type RerankMeta struct {
	BilledUnits *RerankBilledUnits `json:"billed_units,omitempty"`
	Tokens      *RerankTokens      `json:"tokens,omitempty"`
}

// RerankBilledUnits carries the search-unit count a rerank call consumed.
type RerankBilledUnits struct {
	SearchUnits int `json:"search_units,omitempty"`
}

// RerankTokens carries token counts a provider reports for a rerank call, when
// it reports tokens instead of (or alongside) search-units.
type RerankTokens struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// RankedResults finalizes a native reranker's per-document scores into the
// gateway-standard result list: it sorts the (index, relevance_score) pairs by
// descending score, truncates to topN (nil returns all), and attaches each
// document's text when returnDocuments is set. Adapters for providers that
// return a raw score list (DeepInfra) or an unsorted/logit ranking (NVIDIA NIM)
// build the pairs and hand them here, so the top_n / return_documents semantics
// the Cohere contract requires — which those upstreams do not implement — live
// in one place.
//
// A negative top_n is rejected: it cannot name a count of results, so silently
// treating it as "no cap" returned the whole list — the opposite of the caller's
// intent — while top_n == 0 legitimately caps to none. results is sorted in
// place; each element must already carry Index and RelevanceScore, with Document
// nil.
func RankedResults(documents []string, results []RerankResult, topN *int, returnDocuments bool) ([]RerankResult, error) {
	if topN != nil && *topN < 0 {
		return nil, fmt.Errorf("top_n must not be negative, got %d", *topN)
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].RelevanceScore > results[j].RelevanceScore
	})
	if topN != nil && *topN < len(results) {
		results = results[:*topN]
	}
	if returnDocuments {
		for i := range results {
			if idx := results[i].Index; idx >= 0 && idx < len(documents) {
				results[i].Document = &RerankDocument{Text: documents[idx]}
			}
		}
	}
	return results, nil
}
