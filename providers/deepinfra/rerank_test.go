package deepinfra

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestDeepInfraProvider_Rerank_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.RerankProvider = p
}

func TestDeepInfraProvider_Rerank_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference/BAAI/bge-reranker-v2-m3" {
			t.Errorf("path = %q, want /inference/BAAI/bge-reranker-v2-m3", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// scores in INPUT order: doc0=0.2, doc1=0.9.
		_, _ = w.Write([]byte(`{"scores":[0.2,0.9],"input_tokens":42}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ret := true
	resp, err := p.Rerank(context.Background(), core.RerankRequest{
		Model:           "BAAI/bge-reranker-v2-m3",
		Query:           "q",
		Documents:       []string{"first doc", "second doc"},
		ReturnDocuments: &ret,
	})
	if err != nil {
		t.Fatalf("Rerank() error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	// Sorted descending and re-indexed: doc1 (0.9) first, doc0 (0.2) second.
	if resp.Results[0].Index != 1 || resp.Results[0].RelevanceScore != 0.9 {
		t.Errorf("top result = %+v, want index 1 score 0.9", resp.Results[0])
	}
	if resp.Results[0].Document == nil || resp.Results[0].Document.Text != "second doc" {
		t.Errorf("document = %+v, want 'second doc'", resp.Results[0].Document)
	}
	if resp.Meta == nil || resp.Meta.Tokens == nil || resp.Meta.Tokens.InputTokens != 42 {
		t.Errorf("meta tokens mismatch: %+v", resp.Meta)
	}
}

func TestDeepInfraProvider_Rerank_ScoreCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"scores":[0.5],"input_tokens":1}`)) // 1 score, 2 docs
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Rerank(context.Background(), core.RerankRequest{Model: "m", Query: "q", Documents: []string{"a", "b"}})
	if err == nil {
		t.Fatal("Rerank() error = nil, want score/document count mismatch error")
	}
}
