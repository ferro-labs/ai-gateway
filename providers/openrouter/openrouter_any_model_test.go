package openrouter

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestOpenRouterServesAnyModel pins the core.AnyModelProvider declaration.
//
// The routing index deliberately never consults SupportsModel — twelve
// providers answered it `return true`, so it buys no traffic. A provider whose
// upstream accepts ids nothing can enumerate says so with this marker instead,
// and OpenRouter is that case by construction: it aggregates 400+ upstream
// models whose roster changes continuously, so a catalog snapshot is stale the
// day it ships and live discovery is opt-in. Without the marker a real model
// this target serves was refused 404 and never reached OpenRouter at all.
//
// The assertion goes through core.Provider on purpose: that is the interface
// the routing index holds, and it is where the marker either exists or does not.
func TestOpenRouterServesAnyModel(t *testing.T) {
	built, err := New("test-key", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var p core.Provider = built

	if _, ok := p.(core.AnyModelProvider); !ok {
		t.Error("openrouter does not declare core.AnyModelProvider; the routing index will 404 every model absent from the catalog snapshot, however many OpenRouter serves")
	}
}
