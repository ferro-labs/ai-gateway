package cohere

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestCohereProvider_Rerank_Interface(_ *testing.T) {
	p, _ := New("test-key", "")
	var _ core.RerankProvider = p
}

func TestCohereProvider_Rerank_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/rerank" {
			t.Errorf("path = %q, want /v2/rerank", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc","results":[{"index":1,"relevance_score":0.99},{"index":0,"relevance_score":0.12}],"meta":{"billed_units":{"search_units":1}}}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ret := true
	resp, err := p.Rerank(context.Background(), core.RerankRequest{
		Model:           "rerank-v3.5",
		Query:           "what is the capital?",
		Documents:       []string{"Carson City is the capital of Nevada.", "Washington DC is the capital."},
		ReturnDocuments: &ret,
	})
	if err != nil {
		t.Fatalf("Rerank() error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].Index != 1 || resp.Results[0].RelevanceScore != 0.99 {
		t.Errorf("first result = %+v, want index 1 score 0.99", resp.Results[0])
	}
	// return_documents re-attaches from the original array (v2 does not echo text).
	if resp.Results[0].Document == nil || resp.Results[0].Document.Text != "Washington DC is the capital." {
		t.Errorf("document = %+v, want re-attached text", resp.Results[0].Document)
	}
	if resp.Meta == nil || resp.Meta.BilledUnits == nil || resp.Meta.BilledUnits.SearchUnits != 1 {
		t.Errorf("meta search_units mismatch: %+v", resp.Meta)
	}
}

func TestCohereProvider_Rerank_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid api token"}`))
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Rerank(context.Background(), core.RerankRequest{Model: "rerank-v3.5", Query: "q", Documents: []string{"d"}})
	if err == nil {
		t.Fatal("Rerank() error = nil, want 401 error")
	}
	if core.ParseStatusCode(err) != http.StatusUnauthorized {
		t.Errorf("ParseStatusCode = %d, want 401", core.ParseStatusCode(err))
	}
}
