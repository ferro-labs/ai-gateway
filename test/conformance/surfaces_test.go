package conformance

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// This file extends the chat/stream conformance suite to the non-chat surfaces
// whose translation is non-trivial: rerank (five distinct native shapes) and
// moderations (per-category maps). Each provider is built through its OWN
// ProviderEntry against a stub serving that provider's REAL native payload
// (providers/<id>/testdata/<surface>.response.json, sourced from the vendor's
// official docs/OpenAPI/SDK), and the translated core.*Response is asserted
// against the surface's invariants. A payload that stopped matching the adapter
// — a renamed field, a changed shape — fails here, which is the drift the suite
// exists to catch.

// ---------------------------------------------------------------------------
// Rerank
// ---------------------------------------------------------------------------

type rerankFixture struct {
	model string
	// numDocs is how many documents the request carries. It must cover every
	// index the fixture's native payload references (a Cohere top-3 of 5 needs 5).
	numDocs    int
	extraCfg   providers.ProviderConfig
	baseURLKey string
}

// rerankFixtures maps a provider to its rerank conformance case. The native
// payload each stub serves is that provider's testdata/rerank.response.json.
func rerankFixtures() map[string]rerankFixture {
	return map[string]rerankFixture{
		// Cohere v2: {results:[{index, relevance_score}], meta}. Payload is a top-3
		// over 5 documents (indices 3,4,0), pre-sorted by the vendor.
		providers.NameCohere: {model: "rerank-v3.5", numDocs: 5},
		// Together (Cohere-compatible): {results:[{index, relevance_score, document}]}.
		providers.NameTogether: {model: "mixedbread-ai/mxbai-rerank-large-v2", numDocs: 4},
		// DeepInfra native: {scores:[…one per document, input order]}; the adapter
		// pairs by position and core.RankedResults sorts.
		providers.NameDeepInfra: {model: "Qwen/Qwen3-Reranker-4B", numDocs: 4},
		// NVIDIA NIM: {rankings:[{index, logit}]}; the adapter maps logit→sigmoid
		// then core.RankedResults sorts.
		providers.NameNVIDIANIM: {model: "nvidia/rerank-qa-mistral-4b", numDocs: 4},
	}
}

// rerankUncovered lists CapabilityRerank providers deliberately without a
// fixture, with the reason. TestSurfaceConformanceCoverage fails on any
// rerank-capable provider in neither map.
func rerankUncovered() map[string]string {
	return map[string]string{
		providers.NameBedrock: "rerank is an AWS-SDK-signed call (bedrock-agent-runtime:Rerank), " +
			"not an httptest-stubbable HTTP/JSON endpoint — the same reason its chat surface is uncovered",
	}
}

func TestRerankResponseConformance(t *testing.T) {
	for id, fx := range rerankFixtures() {
		t.Run(id, func(t *testing.T) {
			srv := newNativeStub(loadTestdata(t, id, "rerank.response.json"))
			defer srv.Close()

			p := buildProvider(t, id, fx.extraCfg, fx.baseURLKey, srv.URL)
			rp, ok := p.(core.RerankProvider)
			if !ok {
				t.Fatalf("%s does not implement core.RerankProvider", id)
			}

			docs := make([]string, fx.numDocs)
			for i := range docs {
				docs[i] = fmt.Sprintf("document number %d about llamas", i)
			}
			resp, err := rp.Rerank(context.Background(), core.RerankRequest{
				Model: fx.model, Query: "what is a llama", Documents: docs,
			})
			if err != nil {
				t.Fatalf("Rerank(): %v", err)
			}

			// 1. Ranked documents survived translation.
			if len(resp.Results) == 0 {
				t.Fatal("Results is empty; the ranked documents were dropped in translation")
			}

			// 2. Sorted by relevance descending — the gateway-facing contract, applied
			//    by core.RankedResults regardless of the native order. 3. Every index is
			//    a valid, unique position in the original request documents.
			seen := make(map[int]bool, len(resp.Results))
			for i, r := range resp.Results {
				if r.Index < 0 || r.Index >= fx.numDocs {
					t.Errorf("Results[%d].Index = %d, out of range [0,%d)", i, r.Index, fx.numDocs)
				}
				if seen[r.Index] {
					t.Errorf("Results[%d].Index = %d is duplicated", i, r.Index)
				}
				seen[r.Index] = true
				if i > 0 && resp.Results[i].RelevanceScore > resp.Results[i-1].RelevanceScore {
					t.Errorf("Results not sorted descending: [%d]=%v > [%d]=%v",
						i, resp.Results[i].RelevanceScore, i-1, resp.Results[i-1].RelevanceScore)
				}
			}

			// 4. Model is set (every adapter echoes the request model).
			if strings.TrimSpace(resp.Model) == "" {
				t.Error("Model is empty")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Moderations
// ---------------------------------------------------------------------------

type moderationFixture struct {
	model string
	// numInputs is how many inputs the request carries; it must equal the number
	// of results the fixture returns.
	numInputs  int
	extraCfg   providers.ProviderConfig
	baseURLKey string
}

func moderationFixtures() map[string]moderationFixture {
	return map[string]moderationFixture{
		// OpenAI omni-moderation: full {flagged, categories, category_scores,
		// category_applied_input_types}. One input → one result.
		providers.NameOpenAI: {model: "omni-moderation-latest", numInputs: 1},
		// Mistral: {categories, category_scores} with its own category names and NO
		// flagged field — the adapter derives flagged from the categories. Two inputs.
		providers.NameMistral: {model: "mistral-moderation-latest", numInputs: 2},
	}
}

func moderationUncovered() map[string]string {
	// Every CapabilityModeration provider (openai, mistral) has a fixture.
	return map[string]string{}
}

func TestModerationResponseConformance(t *testing.T) {
	for id, fx := range moderationFixtures() {
		t.Run(id, func(t *testing.T) {
			srv := newNativeStub(loadTestdata(t, id, "moderation.response.json"))
			defer srv.Close()

			p := buildProvider(t, id, fx.extraCfg, fx.baseURLKey, srv.URL)
			mp, ok := p.(core.ModerationProvider)
			if !ok {
				t.Fatalf("%s does not implement core.ModerationProvider", id)
			}

			inputs := make([]string, fx.numInputs)
			for i := range inputs {
				inputs[i] = fmt.Sprintf("input text %d", i)
			}
			resp, err := mp.Moderate(context.Background(), core.ModerationRequest{Model: fx.model, Input: inputs})
			if err != nil {
				t.Fatalf("Moderate(): %v", err)
			}

			// 1. One result per input survived translation.
			if len(resp.Results) != fx.numInputs {
				t.Fatalf("len(Results) = %d, want %d (one per input)", len(resp.Results), fx.numInputs)
			}

			for i, r := range resp.Results {
				// 2. The per-category maps carried through.
				if len(r.Categories) == 0 {
					t.Errorf("Results[%d].Categories is empty; the classification was dropped", i)
				}
				if len(r.CategoryScores) == 0 {
					t.Errorf("Results[%d].CategoryScores is empty", i)
				}
				// 3. flagged is consistent with the categories — the OpenAI moderation
				//    contract ("flagged" == whether any category is flagged). Mistral omits
				//    flagged natively, so this asserts the adapter derives it.
				wantFlagged := false
				for _, on := range r.Categories {
					if on {
						wantFlagged = true
						break
					}
				}
				if r.Flagged != wantFlagged {
					t.Errorf("Results[%d].Flagged = %v, want %v (flagged must reflect the categories)", i, r.Flagged, wantFlagged)
				}
			}

			// 4. Model is set.
			if strings.TrimSpace(resp.Model) == "" {
				t.Error("Model is empty")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Coverage guard — every capable provider is fixtured or allowlisted
// ---------------------------------------------------------------------------

// TestSurfaceConformanceCoverage keeps rerank/moderation conformance honest as
// providers gain the capability: a provider that declares CapabilityRerank or
// CapabilityModeration must have a fixture or an allowlisted reason, mirroring
// TestConformanceCoverage for chat.
func TestSurfaceConformanceCoverage(t *testing.T) {
	checks := []struct {
		capability string
		fixtures   map[string]bool
		uncovered  map[string]string
		fixtureCmd string
	}{
		{providers.CapabilityRerank, keySet(rerankFixtures()), rerankUncovered(), "rerank.response.json"},
		{providers.CapabilityModeration, keySetMod(moderationFixtures()), moderationUncovered(), "moderation.response.json"},
	}

	for _, entry := range providers.AllProviders() {
		for _, c := range checks {
			if !providers.ProviderHasCapability(entry.ID, c.capability) {
				continue
			}
			_, hasFixture := c.fixtures[entry.ID]
			_, isAllowlisted := c.uncovered[entry.ID]
			switch {
			case hasFixture && isAllowlisted:
				t.Errorf("provider %q is both fixtured and allowlisted for %s conformance; remove one", entry.ID, c.capability)
			case !hasFixture && !isAllowlisted:
				t.Errorf("provider %q declares %s but has no conformance fixture (%s) and no allowlisted reason; "+
					"add a fixture with its native payload, or allowlist it", entry.ID, c.capability, c.fixtureCmd)
			}
		}
	}

	// Allowlisted names and fixture names must still be registered providers with a reason.
	for id, reason := range rerankUncovered() {
		if _, ok := providers.GetProviderEntry(id); !ok {
			t.Errorf("rerankUncovered() names unregistered provider %q", id)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("rerankUncovered()[%q] has no reason", id)
		}
	}
}

func keySet(m map[string]rerankFixture) map[string]bool {
	s := make(map[string]bool, len(m))
	for k := range m {
		s[k] = true
	}
	return s
}

func keySetMod(m map[string]moderationFixture) map[string]bool {
	s := make(map[string]bool, len(m))
	for k := range m {
		s[k] = true
	}
	return s
}
