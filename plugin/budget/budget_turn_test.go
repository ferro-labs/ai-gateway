package budget

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// The budget store is written once, after the whole request. Inside an agentic
// loop the gateway re-runs this check per turn and stamps what the request has
// spent so far on Measurements — without that term every turn reads the same
// stored figure and the check stops nothing, so a key at 99% of its cap got a
// whole loop however far it ran.
func TestBudget_CountsInRequestSpendFromMeasurements(t *testing.T) {
	const storeID = "turn-spend"
	defer ResetStore(storeID)

	p := &Plugin{}
	if err := p.Init(map[string]any{"spend_limit_usd": 1.0, "input_per_m_tokens": 1.0, "output_per_m_tokens": 2.0, "store_id": storeID}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	newCtx := func(spent float64, priced bool) *plugin.Context {
		return &plugin.Context{
			Stage:        plugin.StageBeforeRequest,
			Request:      &providers.Request{Model: "m"},
			Metadata:     map[string]any{"api_key": "k1"},
			Measurements: plugin.Measurements{CostUSD: spent, HasCost: priced},
		}
	}

	// Nothing stored and nothing spent yet: admitted.
	pctx := newCtx(0, false)
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if pctx.Reject {
		t.Fatalf("rejected with no spend recorded: %s", pctx.Reason)
	}

	// The same key, mid-loop, having already spent past the cap on earlier
	// turns. The store still reads zero, so only Measurements can catch this.
	pctx = newCtx(1.5, true)
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("a loop that has already spent past the cap was allowed another turn")
	}

	// An unpriced request carries no cost term and must not be rejected on it.
	pctx = newCtx(1.5, false)
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if pctx.Reject {
		t.Fatalf("an unpriced request was rejected on a cost it does not have: %s", pctx.Reason)
	}
}
