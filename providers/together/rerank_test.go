package together

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestTogetherProvider_Rerank_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.RerankProvider = p
}

func TestTogetherProvider_Rerank_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// New("", srv.URL) resolves a path-less base to the provider's /v1 root.
		if r.URL.Path != "/v1/rerank" {
			t.Errorf("path = %q, want /v1/rerank", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"rerank","id":"9d","model":"m","results":[{"index":0,"relevance_score":0.3,"document":{"text":"The llama."}}],"usage":{"prompt_tokens":1837,"completion_tokens":0,"total_tokens":1837}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Rerank(context.Background(), core.RerankRequest{
		Model:     "Salesforce/Llama-Rank-v1",
		Query:     "animals near Peru?",
		Documents: []string{"The llama."},
	})
	if err != nil {
		t.Fatalf("Rerank() error: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].RelevanceScore != 0.3 {
		t.Fatalf("results = %+v, want one result score 0.3", resp.Results)
	}
	if resp.Results[0].Document == nil || resp.Results[0].Document.Text != "The llama." {
		t.Errorf("document = %+v, want echoed text", resp.Results[0].Document)
	}
	if resp.Meta == nil || resp.Meta.Tokens == nil || resp.Meta.Tokens.InputTokens != 1837 {
		t.Errorf("meta tokens mismatch: %+v", resp.Meta)
	}
}

func TestTogetherProvider_Rerank_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Rerank(context.Background(), core.RerankRequest{Model: "m", Query: "q", Documents: []string{"d"}})
	if err == nil {
		t.Fatal("Rerank() error = nil, want 429 error")
	}
	if core.ParseStatusCode(err) != http.StatusTooManyRequests {
		t.Errorf("ParseStatusCode = %d, want 429", core.ParseStatusCode(err))
	}
}
