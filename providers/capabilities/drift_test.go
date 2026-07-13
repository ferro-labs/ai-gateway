package capabilities_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/capabilities"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// The matrix defaults to Forward for anything it does not model: an unknown
// provider forwards every parameter, an unknown parameter is forwarded. That
// tolerance is what keeps the matrix from breaking on inputs it never saw — and
// it is also what makes a typo invisible. A misspelled provider ID or parameter
// name does not fail; it quietly declares that everything is supported.
//
// Each guard below fails on one specific drift that the defaults would otherwise
// swallow.

// A matrix key that is not a real provider ID declares exceptions for a provider
// that does not exist. The provider it was meant to describe keeps the Forward
// default and silently forwards every parameter, including ones it cannot express.
func TestMatrixKeysAreRealProviderIDs(t *testing.T) {
	known := make(map[string]bool)
	for _, entry := range providers.AllProviders() {
		known[entry.ID] = true
	}

	for id := range capabilities.Matrix {
		if !known[id] {
			t.Errorf("matrix declares provider %q, which is not a built-in provider ID; "+
				"its exceptions apply to nothing, and the provider it was meant to describe forwards everything", id)
		}
	}
}

// A parameter name the gateway does not model can never be enforced or reported,
// so an Unsupported entry for it is inert: the parameter it was meant to catch
// still reaches the provider.
func TestMatrixParamsAreKnownParams(t *testing.T) {
	known := make(map[string]bool, len(capabilities.AllParams))
	for _, param := range capabilities.AllParams {
		known[param] = true
	}

	for id, profile := range capabilities.Matrix {
		for param := range profile {
			if !known[param] {
				t.Errorf("provider %q declares support for %q, which is not in AllParams; "+
					"the entry is inert and that parameter is enforced for nobody", id, param)
			}
		}
	}
}

// AllParams is documented as the chat parameters of core.Request. If a parameter is
// added to the request struct and not to AllParams, it is missing from
// /v1/capabilities and no provider can declare it Unsupported — it reaches every
// provider unchecked. That is the drift adding a parameter causes, and the one
// least likely to be noticed by hand.
func TestAllParamsMatchesRequestParameters(t *testing.T) {
	// model and messages are the request itself, not tunable parameters: every
	// provider must express them, so they sit outside the compatibility matrix.
	notParameters := map[string]bool{"model": true, "messages": true}

	requestParams := make(map[string]bool)
	rt := reflect.TypeFor[core.Request]()
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" || notParameters[name] {
			continue
		}
		requestParams[name] = true
	}

	declared := make(map[string]bool, len(capabilities.AllParams))
	for _, param := range capabilities.AllParams {
		declared[param] = true
	}

	for name := range requestParams {
		if !declared[name] {
			t.Errorf("core.Request accepts %q but capabilities.AllParams does not list it: "+
				"it is absent from /v1/capabilities, no provider can declare it unsupported, "+
				"and it is forwarded to every provider unchecked", name)
		}
	}

	// The reverse drift: a parameter listed in AllParams that no longer exists on
	// core.Request can never arrive, so its matrix entries are dead weight.
	for param := range declared {
		if !requestParams[param] {
			t.Errorf("capabilities.AllParams lists %q, which is not a parameter on core.Request; "+
				"the gateway can never receive it and its matrix entries are dead", param)
		}
	}
}
