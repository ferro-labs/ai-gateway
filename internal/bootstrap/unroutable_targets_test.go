package bootstrap

import (
	"bytes"
	"context"
	"strings"
	"testing"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestWarnUnroutableTargets proves a misconfigured target is audible at startup,
// and that the total case is separated from the partial one: every target
// unroutable means the instance serves nothing and is logged at error level,
// while some targets unroutable is a degraded instance that still serves and is
// logged as a warning. Neither exits — a target whose credential has not been
// rolled out yet starts working the moment the secret lands.
func TestWarnUnroutableTargets(t *testing.T) {
	tests := []struct {
		name       string
		targets    []string
		registered []string
		wantLevel  string
		wantNames  []string
		wantSilent bool
	}{
		{
			name:       "every target resolves logs nothing",
			targets:    []string{"a", "b"},
			registered: []string{"a", "b"},
			wantSilent: true,
		},
		{
			name:       "one unroutable target warns",
			targets:    []string{"a", "typo-provider"},
			registered: []string{"a"},
			wantLevel:  "WARN",
			wantNames:  []string{"typo-provider"},
		},
		{
			name:       "no routable target at all is an error",
			targets:    []string{"typo-provider"},
			registered: []string{"a"},
			wantLevel:  "ERROR",
			wantNames:  []string{"typo-provider"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets := make([]config.Target, 0, len(tt.targets))
			for _, name := range tt.targets {
				targets = append(targets, config.Target{VirtualKey: name})
			}
			gw, err := aigateway.New(config.Config{
				Strategy: config.StrategyConfig{Mode: config.ModeFallback},
				Targets:  targets,
			})
			if err != nil {
				t.Fatalf("New gateway: %v", err)
			}
			t.Cleanup(func() {
				if closeErr := gw.Close(); closeErr != nil {
					t.Errorf("close gateway: %v", closeErr)
				}
			})
			for _, name := range tt.registered {
				gw.RegisterProvider(stubProvider{name: name})
			}

			var buf bytes.Buffer
			previous := logger.Default()
			logger.SetDefault(logger.New(logger.Options{Level: "debug", Output: &buf}))
			t.Cleanup(func() { logger.SetDefault(previous) })

			warnUnroutableTargets(gw)

			got := buf.String()
			if tt.wantSilent {
				if strings.Contains(got, "target") {
					t.Fatalf("expected no target diagnostic, got: %s", got)
				}
				return
			}
			if !strings.Contains(got, `"level":"`+tt.wantLevel+`"`) {
				t.Errorf("log level %q missing from: %s", tt.wantLevel, got)
			}
			for _, name := range tt.wantNames {
				if !strings.Contains(got, name) {
					t.Errorf("target %q missing from log: %s", name, got)
				}
			}
		})
	}
}

// stubProvider is the minimum core.Provider needed to occupy a name in the
// gateway's registry.
type stubProvider struct{ name string }

func (s stubProvider) Name() string              { return s.name }
func (s stubProvider) SupportsModel(string) bool { return true }
func (s stubProvider) Models() []providers.ModelInfo {
	return nil
}
func (s stubProvider) Complete(context.Context, providers.Request) (*providers.Response, error) {
	return nil, nil
}

var _ core.Provider = stubProvider{}
