package capabilities

import "testing"

// TestSupportString verifies the wire names used by the /v1/capabilities API.
func TestSupportString(t *testing.T) {
	cases := []struct {
		support Support
		want    string
	}{
		{Forward, "forward"},
		{Translate, "translate"},
		{Unsupported, "unsupported"},
		{Support(99), "forward"}, // unknown values fall back to forward
	}
	for _, tc := range cases {
		if got := tc.support.String(); got != tc.want {
			t.Errorf("Support(%d).String() = %q, want %q", tc.support, got, tc.want)
		}
	}
}

// TestSupportOf_DerivedMatrix pins the per-provider matrix to the supported
// lists it was derived from. Each row asserts a parameter's declared Support;
// a wrong derivation OR a typo'd provider key (which would silently fall back to
// Forward) fails here.
func TestSupportOf_DerivedMatrix(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		param    string
		want     Support
	}{
		// anthropic: supports temperature/top_p/max_tokens/stop/tools/tool_choice/user.
		{"anthropic supported temperature", "anthropic", "temperature", Forward},
		{"anthropic supported user", "anthropic", "user", Forward},
		{"anthropic drops seed", "anthropic", "seed", Unsupported},
		{"anthropic drops response_format", "anthropic", "response_format", Unsupported},
		{"anthropic drops logit_bias", "anthropic", "logit_bias", Unsupported},

		// bedrock: provider-level common base is temperature/top_p/max_tokens only.
		{"bedrock supported top_p", "bedrock", "top_p", Forward},
		{"bedrock drops stop", "bedrock", "stop", Unsupported},
		{"bedrock drops tools", "bedrock", "tools", Unsupported},

		// cohere: supports seed and penalties; drops n/user/response_format.
		{"cohere supported seed", "cohere", "seed", Forward},
		{"cohere supported frequency_penalty", "cohere", "frequency_penalty", Forward},
		{"cohere drops n", "cohere", "n", Unsupported},
		{"cohere drops user", "cohere", "user", Unsupported},

		// gemini: supports n/seed/penalties; translates response_format; drops logit_bias.
		{"gemini supported n", "gemini", "n", Forward},
		{"gemini translates response_format", "gemini", "response_format", Translate},
		{"gemini drops logit_bias", "gemini", "logit_bias", Unsupported},
		{"gemini drops user", "gemini", "user", Unsupported},

		// replicate: forwards sampling + penalties; drops tools/tool_choice.
		{"replicate supported seed", "replicate", "seed", Forward},
		{"replicate drops tools", "replicate", "tools", Unsupported},
		{"replicate drops response_format", "replicate", "response_format", Unsupported},

		// ai21: supports only max_tokens/temperature/top_p/stop.
		{"ai21 supported stop", "ai21", "stop", Forward},
		{"ai21 drops tools", "ai21", "tools", Unsupported},
		{"ai21 drops seed", "ai21", "seed", Unsupported},

		// Streaming-control params are outside the warn mechanism ⇒ always Forward.
		{"anthropic forwards stream", "anthropic", "stream", Forward},
		{"gemini forwards parallel_tool_calls", "gemini", "parallel_tool_calls", Forward},

		// Unknown provider / unknown param default to Forward.
		{"unknown provider defaults forward", "does-not-exist", "seed", Forward},
		{"unknown param defaults forward", "anthropic", "made_up_param", Forward},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SupportOf(tc.provider, tc.param); got != tc.want {
				t.Errorf("SupportOf(%q, %q) = %v, want %v", tc.provider, tc.param, got, tc.want)
			}
		})
	}
}

// TestProfileOf_CoversAllParams verifies a materialised profile has exactly one
// entry per canonical parameter, each carrying a defined Support value.
func TestProfileOf_CoversAllParams(t *testing.T) {
	profile := ProfileOf("gemini")
	if len(profile) != len(AllParams) {
		t.Fatalf("ProfileOf returned %d params, want %d", len(profile), len(AllParams))
	}
	for _, param := range AllParams {
		support, ok := profile[param]
		if !ok {
			t.Errorf("ProfileOf missing param %q", param)
			continue
		}
		switch support {
		case Forward, Translate, Unsupported:
		default:
			t.Errorf("ProfileOf[%q] = %v, not a defined Support", param, support)
		}
	}
	// Overrides must be reflected in the materialised profile.
	if profile["response_format"] != Translate {
		t.Errorf("ProfileOf(gemini)[response_format] = %v, want Translate", profile["response_format"])
	}
	if profile["temperature"] != Forward {
		t.Errorf("ProfileOf(gemini)[temperature] = %v, want Forward", profile["temperature"])
	}
}

// TestProfileOf_UnknownProviderAllForward verifies a provider with no matrix
// entry yields an all-Forward profile.
func TestProfileOf_UnknownProviderAllForward(t *testing.T) {
	profile := ProfileOf("fireworks")
	for _, param := range AllParams {
		if profile[param] != Forward {
			t.Errorf("ProfileOf(fireworks)[%q] = %v, want Forward", param, profile[param])
		}
	}
}
