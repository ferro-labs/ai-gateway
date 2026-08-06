package core

import "testing"

func intPtr(n int) *int { return &n }

// TestRankedResults_TopN pins the top_n contract the shared finalizer applies
// for reranker adapters that have no native top_n: a negative value is a
// validation error rather than a silent "return everything", zero caps to none,
// and an in-range value truncates. nil and an oversized value both return the
// full ranked list.
func TestRankedResults_TopN(t *testing.T) {
	// Three documents, deliberately out of score order so the sort is exercised.
	docs := []string{"a", "b", "c"}
	newResults := func() []RerankResult {
		return []RerankResult{
			{Index: 0, RelevanceScore: 0.1},
			{Index: 1, RelevanceScore: 0.9},
			{Index: 2, RelevanceScore: 0.5},
		}
	}

	tests := []struct {
		name      string
		topN      *int
		wantErr   bool
		wantCount int
	}{
		{name: "negative top_n is an error", topN: intPtr(-1), wantErr: true},
		{name: "zero top_n caps to none", topN: intPtr(0), wantCount: 0},
		{name: "in-range top_n truncates", topN: intPtr(2), wantCount: 2},
		{name: "nil top_n returns all", topN: nil, wantCount: 3},
		{name: "top_n above length returns all", topN: intPtr(10), wantCount: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RankedResults(docs, newResults(), tt.topN, false)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("RankedResults(topN=%v) = nil error, want a validation error", *tt.topN)
				}
				if got != nil {
					t.Errorf("RankedResults(topN=%v) returned %v alongside the error, want nil", *tt.topN, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RankedResults = %v, want no error", err)
			}
			if len(got) != tt.wantCount {
				t.Fatalf("RankedResults returned %d results, want %d", len(got), tt.wantCount)
			}
			// Whatever survives the cap must be in descending score order.
			for i := 1; i < len(got); i++ {
				if got[i-1].RelevanceScore < got[i].RelevanceScore {
					t.Errorf("results not sorted by descending score: %v", got)
				}
			}
		})
	}
}
