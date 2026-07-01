package strategies

import "testing"

// TestWeightedPick exercises the weighted random selection helper used by the
// LoadBalance and ABTest strategies. Each case supplies its own weight function
// so the branches can be driven deterministically regardless of the RNG draw.
func TestWeightedPick(t *testing.T) {
	weight := func(target Target) float64 { return target.Weight }

	tests := []struct {
		name    string
		items   []Target
		wantOK  bool
		wantKey string // expected VirtualKey when wantOK is true
	}{
		{
			name:   "zero total weight returns not ok",
			items:  []Target{{VirtualKey: "a", Weight: 0}, {VirtualKey: "b", Weight: 0}},
			wantOK: false,
		},
		{
			name:    "single item returns that item",
			items:   []Target{{VirtualKey: "solo", Weight: 1}},
			wantOK:  true,
			wantKey: "solo",
		},
		{
			// The first item has an empty cumulative range (weight 0), so no draw
			// can fall within it; every draw resolves to the last item.
			name:    "draw beyond earlier ranges returns last item",
			items:   []Target{{VirtualKey: "first", Weight: 0}, {VirtualKey: "last", Weight: 1}},
			wantOK:  true,
			wantKey: "last",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, ok := weightedPick(tt.items, weight)

			// Assert
			if ok != tt.wantOK {
				t.Fatalf("weightedPick ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if got.VirtualKey != tt.wantKey {
				t.Errorf("weightedPick returned %q, want %q", got.VirtualKey, tt.wantKey)
			}
		})
	}
}
