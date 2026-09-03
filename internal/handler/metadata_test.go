package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
)

func TestRoutingMetadata_Parses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		header  string
		want    map[string]string
		wantErr string
	}{
		{"absent", "", nil, ""},
		{"scalars", `{"tier":"gold","priority":3,"beta":true}`, map[string]string{"tier": "gold", "priority": "3", "beta": "true"}, ""},
		{"not an object", `["gold"]`, nil, "not a JSON object"},
		{"null is not an object", `null`, nil, "not a JSON object"},
		{"trailing text", `{"tier":"gold"} trailing`, nil, "single JSON object"},
		{"second document", `{"tier":"gold"}{"tier":"silver"}`, nil, "single JSON object"},
		{"large integer kept exact", `{"account":12345678901234567890}`, map[string]string{"account": "12345678901234567890"}, ""},
		{"nested", `{"tier":{"level":"gold"}}`, nil, "must be a string, number or boolean"},
		{"too large", `{"k":"` + strings.Repeat("x", maxRoutingMetadataBytes) + `"}`, nil, "exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.header != "" {
				h.Set(HeaderRoutingMetadata, tc.header)
			}
			got, err := routingMetadata(h)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("got[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestChat_MetadataHeaderRoutesAConditionalRule: the allow-listed header
// reaches the conditional predicate, and a malformed one is the caller's 400.
func TestChat_MetadataHeaderRoutesAConditionalRule(t *testing.T) {
	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{
			Mode:       config.ModeConditional,
			Conditions: []config.Condition{{Key: config.ConditionKeyMetadata, Field: "tier", Value: "gold", TargetKey: "gold"}},
		},
		Targets: []config.Target{{VirtualKey: "standard", Models: []string{attributedModel}}, {VirtualKey: "gold", Models: []string{attributedModel}}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gw.RegisterProviderAs("standard", &everySurfaceProvider{name: "standard-vendor"})
	gw.RegisterProviderAs("gold", &everySurfaceProvider{name: "gold-vendor"})
	body := `{"model":"` + attributedModel + `","messages":[{"role":"user","content":"hi"}]}`

	r := jsonRequest(t, "/v1/chat/completions", body)
	r.Header.Set(HeaderRoutingMetadata, `{"tier":"gold"}`)
	w := httptest.NewRecorder()
	ChatCompletions(gw)(w, r)
	if w.Code != http.StatusOK || w.Header().Get("X-Gateway-Target") != "gold" {
		t.Fatalf("status %d target %q, want the metadata rule's target", w.Code, w.Header().Get("X-Gateway-Target"))
	}

	r = jsonRequest(t, "/v1/chat/completions", body)
	w = httptest.NewRecorder()
	ChatCompletions(gw)(w, r)
	if w.Header().Get("X-Gateway-Target") != "standard" {
		t.Fatalf("target %q without the header, want the fallback", w.Header().Get("X-Gateway-Target"))
	}

	r = jsonRequest(t, "/v1/chat/completions", body)
	r.Header.Set(HeaderRoutingMetadata, `not json`)
	w = httptest.NewRecorder()
	ChatCompletions(gw)(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d for a malformed header, want 400", w.Code)
	}
}
