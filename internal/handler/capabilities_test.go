package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/capabilities"
)

// capabilityProfiles calls the handler and returns the decoded provider map.
func capabilityProfiles(t *testing.T, gw *aigateway.Gateway) map[string]map[string]string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/capabilities", nil)
	Capabilities(gw)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var payload struct {
		Providers map[string]map[string]string `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return payload.Providers
}

// TestCapabilities_ReturnsProfiles verifies GET /v1/capabilities reports a
// per-provider parameter profile: matrix exceptions surface as
// "unsupported"/"translate", and providers without an entry are all "forward".
func TestCapabilities_ReturnsProfiles(t *testing.T) {
	gw := completionsGateway(t,
		config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets:  []config.Target{{VirtualKey: "gemini"}, {VirtualKey: "openai"}},
		},
		&nonProxyProvider{name: "gemini", models: []string{"gemini-pro"}},
		&nonProxyProvider{name: "openai", models: []string{"gpt-4o"}},
	)

	profiles := capabilityProfiles(t, gw)

	gemini, ok := profiles["gemini"]
	if !ok {
		t.Fatal("response missing gemini profile")
	}
	if gemini["seed"] != "forward" {
		t.Errorf("gemini.seed = %q, want forward", gemini["seed"])
	}
	if gemini["response_format"] != "translate" {
		t.Errorf("gemini.response_format = %q, want translate", gemini["response_format"])
	}
	if gemini["logit_bias"] != "unsupported" {
		t.Errorf("gemini.logit_bias = %q, want unsupported", gemini["logit_bias"])
	}

	openai, ok := profiles["openai"]
	if !ok {
		t.Fatal("response missing openai profile")
	}
	if openai["seed"] != "forward" {
		t.Errorf("openai.seed = %q, want forward (no matrix entry)", openai["seed"])
	}
	// Every canonical param must be present for a materialised profile, and
	// none beyond it.
	if len(openai) != len(capabilities.AllParams) {
		t.Fatalf("openai profile has %d params, want %d", len(openai), len(capabilities.AllParams))
	}
	for _, param := range capabilities.AllParams {
		if _, ok := openai[param]; !ok {
			t.Errorf("openai profile missing canonical param %q", param)
		}
	}
}

// TestCapabilities_DescribesOnlyServedProviders pins the endpoint to the same
// authority /v1/models answers from: the registered providers a configured
// target names.
//
// It answered from the REGISTERED set, which is the set of providers whose
// credential happens to be in the environment — so a gateway targeting one
// provider described five, and a client that read a profile to decide whether
// a parameter would be forwarded was told yes by a provider every routed
// surface then 404'd. The two halves are separate facts and are asserted
// separately: a target naming a provider with no credential is not served
// either, and describing it would promise a profile no request can reach.
func TestCapabilities_DescribesOnlyServedProviders(t *testing.T) {
	tests := []struct {
		name       string
		targets    []config.Target
		registered []string
		want       []string
	}{
		{
			name:       "an untargeted provider is not described",
			targets:    []config.Target{{VirtualKey: "deepseek"}},
			registered: []string{"deepseek", "openai", "gemini"},
			want:       []string{"deepseek"},
		},
		{
			name:       "a target with no registered provider is not described",
			targets:    []config.Target{{VirtualKey: "deepseek"}, {VirtualKey: "groq"}},
			registered: []string{"deepseek"},
			want:       []string{"deepseek"},
		},
		{
			name:       "every targeted and registered provider is described",
			targets:    []config.Target{{VirtualKey: "openai"}, {VirtualKey: "gemini"}},
			registered: []string{"openai", "gemini"},
			want:       []string{"gemini", "openai"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := make([]providers.Provider, 0, len(tt.registered))
			for _, name := range tt.registered {
				ps = append(ps, &nonProxyProvider{name: name, models: []string{name + "-model"}})
			}
			gw := completionsGateway(t,
				config.Config{
					Strategy: config.StrategyConfig{Mode: config.ModeFallback},
					Targets:  tt.targets,
				},
				ps...,
			)

			got := make([]string, 0, len(tt.want))
			for name := range capabilityProfiles(t, gw) {
				got = append(got, name)
			}
			slices.Sort(got)

			if !slices.Equal(got, tt.want) {
				t.Errorf("described providers = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCapabilities_OmitsTransportControls: stream and stream_options are
// resolved above the provider layer — internal/streamwrap applies the caller's
// include_usage on every routed stream, whatever the provider — so no provider
// answer about them exists. Publishing one would be a claim about the gateway
// dressed as a claim about the provider, wrong in either direction.
func TestCapabilities_OmitsTransportControls(t *testing.T) {
	gw := completionsGateway(t,
		config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets:  []config.Target{{VirtualKey: "anthropic"}},
		},
		&nonProxyProvider{name: "anthropic", models: []string{"claude-3"}},
	)

	profile := capabilityProfiles(t, gw)["anthropic"]
	for _, param := range []string{"stream", "stream_options"} {
		if got, ok := profile[param]; ok {
			t.Errorf("profile publishes %q = %q; it is a gateway transport control, "+
				"not a provider parameter", param, got)
		}
	}
}

// TestCapabilities_ParallelToolCallsIsHonest guards the affirmative false claim
// this endpoint used to make. Materialising every parameter over AllParams meant
// parallel_tool_calls was published "forward" for every served provider,
// including the native-wire ones whose request body has no field to carry it.
func TestCapabilities_ParallelToolCallsIsHonest(t *testing.T) {
	gw := completionsGateway(t,
		config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets: []config.Target{
				{VirtualKey: "anthropic"}, {VirtualKey: "gemini"}, {VirtualKey: "openai"},
			},
		},
		&nonProxyProvider{name: "anthropic", models: []string{"claude-3"}},
		&nonProxyProvider{name: "gemini", models: []string{"gemini-pro"}},
		&nonProxyProvider{name: "openai", models: []string{"gpt-4o"}},
	)

	profiles := capabilityProfiles(t, gw)
	want := map[string]string{
		"anthropic": "translate", // mapped onto disable_parallel_tool_use
		"gemini":    "unsupported",
		"openai":    "forward",
	}
	for provider, expect := range want {
		if got := profiles[provider]["parallel_tool_calls"]; got != expect {
			t.Errorf("%s.parallel_tool_calls = %q, want %q", provider, got, expect)
		}
	}
}

// TestCapabilities_PublishesImageResponseFormats: the image surface restricts
// response_format to what the provider can actually produce and answers 400
// otherwise, so the restriction has to be discoverable. A provider that honours
// both is absent rather than listed with both, so "absent" reads as
// unconstrained.
func TestCapabilities_PublishesImageResponseFormats(t *testing.T) {
	gw := completionsGateway(t,
		config.Config{
			Strategy: config.StrategyConfig{Mode: config.ModeFallback},
			Targets:  []config.Target{{VirtualKey: "bedrock"}, {VirtualKey: "openai"}},
		},
		&nonProxyProvider{name: "bedrock", models: []string{"amazon.nova-canvas-v1:0"}},
		&nonProxyProvider{name: "openai", models: []string{"gpt-4o"}},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/capabilities", nil)
	Capabilities(gw)(rec, req)

	var payload struct {
		ImageResponseFormats map[string][]string `json:"image_response_formats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	got, ok := payload.ImageResponseFormats["bedrock"]
	if !ok {
		t.Fatal("image_response_formats missing bedrock, whose image surface returns base64 only")
	}
	if !slices.Equal(got, []string{"b64_json"}) {
		t.Errorf("bedrock image formats = %v, want [b64_json]", got)
	}
	if _, ok := payload.ImageResponseFormats["openai"]; ok {
		t.Error("openai must be absent: it is unconstrained at the provider level")
	}
}
