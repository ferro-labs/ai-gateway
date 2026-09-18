package aigateway

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/observability"
	"github.com/ferro-labs/ai-gateway/providers"

	_ "github.com/ferro-labs/ai-gateway/plugin/regexguard"
)

// TestGateway_Route_EmitsGuardrailMatchEvent proves the log-only visibility the
// feature exists for: a regex-guard configured action: log matches the request,
// the request is served, and exactly one gateway.guardrail.match event reaches
// the observability seam — attributed to the instance, carrying the resolved
// action, and carrying no matched text.
func TestGateway_Route_EmitsGuardrailMatchEvent(t *testing.T) {
	const canary = "canary-CONTENT-9f3" // the matched text; must never appear in the event

	gw, err := newTestGateway(t, config.Config{
		Strategy: config.StrategyConfig{Mode: config.ModeSingle},
		Targets:  []config.Target{{VirtualKey: "mock"}},
		Plugins: []config.PluginConfig{{
			Name:    "regex-guard",
			ID:      "audit-hits",
			Type:    "guardrail",
			Stage:   "before_request",
			Enabled: true,
			Config: map[string]any{
				"action": "log",
				"rules": []any{
					map[string]any{"name": "r1", "pattern": canary, "apply_to": "input"},
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ep := &eventCapturingProvider{recordingActive: true}
	gw.SetObservability(ep)
	if err := gw.LoadPlugins(); err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}
	gw.RegisterProvider(&mockProvider{
		name:   "mock",
		models: []string{testModel},
		resp:   &providers.Response{ID: "r1", Provider: "mock", Model: testModel},
	})

	_, err = gw.Route(context.Background(), providers.Request{
		Model:    testModel,
		Messages: []providers.Message{{Role: "user", Content: "please handle " + canary + " now"}},
	})
	if err != nil {
		t.Fatalf("a log action must not block the request, got %v", err)
	}

	matches := eventsWithSubject(ep.capturedEvents(), observability.SubjectGuardrailMatch)
	if len(matches) != 1 {
		t.Fatalf("want exactly one %s event, got %d", observability.SubjectGuardrailMatch, len(matches))
	}
	evt := matches[0]

	want := map[string]any{
		observability.AttrFerroGuardrailPlugin:   "regex-guard",
		observability.AttrFerroGuardrailInstance: "audit-hits",
		observability.AttrFerroGuardrailAction:   "log",
		observability.AttrFerroGuardrailStage:    "before_request",
		observability.AttrFerroGuardrailAllowed:  true,
	}
	for k, v := range want {
		if evt.Attributes[k] != v {
			t.Errorf("attribute %q = %v, want %v", k, evt.Attributes[k], v)
		}
	}

	// The gateway's no-leak posture: the event names the decision, never the
	// matched text or the pattern.
	for _, s := range eventStrings(evt) {
		if strings.Contains(s, canary) {
			t.Fatalf("guardrail-match event leaked the matched text %q in %q", canary, s)
		}
	}
}

// eventStrings returns every string-valued field and attribute of an event, so a
// test can assert none of them carries request content.
func eventStrings(evt observability.Event) []string {
	out := make([]string, 0, 7+2*len(evt.Attributes)+2*len(evt.Metadata))
	out = append(out, evt.Subject, evt.TraceID, evt.User, evt.SessionID, evt.Error, evt.Provider, evt.Model)
	for k, v := range evt.Attributes {
		out = append(out, k, fmt.Sprintf("%v", v))
	}
	for k, v := range evt.Metadata {
		out = append(out, k, v)
	}
	return out
}
