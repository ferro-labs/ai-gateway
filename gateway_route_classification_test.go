package aigateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// A target whose provider never registered — the shape a missing credential
// takes by the time routing sees it — must not turn a request for a model
// nobody serves into a server fault. It is a 404 condition, and the classifier
// only reaches 404 through core.ErrNoCapableProvider.
//
// The operator's half of the same event is asserted alongside it: the target's
// name is what makes this diagnosable, so it has to be in the log even though it
// is deliberately kept out of the response.
func TestRoute_UnregisteredTargetIsUnroutableAndLogged(t *testing.T) {
	logs := captureLogs(t)

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeFallback},
		Targets:  []config.Target{{VirtualKey: mockProviderName}, {VirtualKey: "ghost"}},
	})
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	gw.RegisterProvider(&mockProvider{name: mockProviderName, models: []string{testModel}})

	_, routeErr := gw.Route(context.Background(), providers.Request{
		Model:    "a-model-no-target-serves",
		Messages: []providers.Message{{Role: "user", Content: "hi"}},
	})
	if routeErr == nil {
		t.Fatal("expected the route to fail")
	}
	if !errors.Is(routeErr, core.ErrNoCapableProvider) {
		t.Fatalf("Route = %v, want an error matching core.ErrNoCapableProvider", routeErr)
	}
	if !strings.Contains(logs.String(), "ghost") {
		t.Errorf("the unresolvable target is missing from the log, leaving nothing to diagnose: %s", logs.String())
	}
}
