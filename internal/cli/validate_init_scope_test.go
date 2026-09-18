package cli

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
)

// initSpy stands in for a plugin built outside this repository whose Init has
// side effects — it opens a store, say. It declares no stage restriction, so its
// stage answer cannot depend on its config and validate has no reason to run it.
type initSpy struct{}

var initSpyCalls atomic.Int32

func (*initSpy) Name() string            { return "init-spy" }
func (*initSpy) Type() plugin.PluginType { return plugin.TypeLogging }
func (*initSpy) Init(map[string]any) error {
	initSpyCalls.Add(1)
	return nil
}
func (*initSpy) Execute(context.Context, *plugin.Context) error { return nil }
func (*initSpy) Close() error                                   { return nil }

func init() {
	plugin.RegisterFactory("init-spy", func() plugin.Plugin { return &initSpy{} })
}

// TestRunValidateDoesNotInitAPluginThatNeverAskedForIt keeps the pre-flight
// promise: validate compiles a stage-restricted plugin's config to read its real
// stages, and initialises nothing else.
func TestRunValidateDoesNotInitAPluginThatNeverAskedForIt(t *testing.T) {
	initSpyCalls.Store(0)
	path := writeConfig(t, "config.yaml",
		"strategy:\n  mode: single\ntargets:\n  - virtual_key: openai\n"+
			"plugins:\n  - name: init-spy\n    type: logging\n"+
			"    stage: before_request\n    enabled: true\n    config:\n      dsn: somewhere\n")
	cmd, _ := newHandlerCmd(t, "", "table")

	if err := runValidate(cmd, []string{path}); err != nil {
		t.Fatalf("runValidate: %v", err)
	}
	if n := initSpyCalls.Load(); n != 0 {
		t.Fatalf("validate ran Init %d time(s) on a plugin with no stage restriction", n)
	}
}
