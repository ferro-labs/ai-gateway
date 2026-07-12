package capabilities_test

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/capabilities"
)

// TestProfileOf_AllProvidersValid is a drift guard mirroring
// providers/stability_test.go: every built-in provider must yield a full,
// well-formed capability profile over AllParams. It catches a provider added to
// AllProviders() whose ID the matrix or its consumers cannot resolve.
func TestProfileOf_AllProvidersValid(t *testing.T) {
	for _, entry := range providers.AllProviders() {
		profile := capabilities.ProfileOf(entry.ID)
		if len(profile) != len(capabilities.AllParams) {
			t.Errorf("provider %q: profile has %d params, want %d",
				entry.ID, len(profile), len(capabilities.AllParams))
		}
		for _, param := range capabilities.AllParams {
			support, ok := profile[param]
			if !ok {
				t.Errorf("provider %q: profile missing param %q", entry.ID, param)
				continue
			}
			switch support {
			case capabilities.Forward, capabilities.Translate, capabilities.Unsupported:
			default:
				t.Errorf("provider %q: param %q has undefined support %v", entry.ID, param, support)
			}
		}
	}
}
