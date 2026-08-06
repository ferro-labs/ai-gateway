package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/spf13/cobra"

	// Register the built-in plugins so validate resolves plugin names against
	// the same registry serve will use. Importing them here rather than relying
	// on whoever wires ValidateCmd keeps the check correct for any binary that
	// mounts the command, and keeps it exercisable without one.
	_ "github.com/ferro-labs/ai-gateway/plugin/budget"
	_ "github.com/ferro-labs/ai-gateway/plugin/cache"
	_ "github.com/ferro-labs/ai-gateway/plugin/logger"
	_ "github.com/ferro-labs/ai-gateway/plugin/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/plugin/ratelimit"
	_ "github.com/ferro-labs/ai-gateway/plugin/wordfilter"
)

// ValidateCmd validates a gateway configuration file.
//
// What it promises: everything `serve` can reject about a config without a
// runtime environment. Two kinds of answer make that up, and they live in two
// places for a reason.
//
//   - Properties of the CONFIG alone — strategy modes, target weights, alias
//     chains, a plugin listed at several stages under entries that disagree —
//     are checked by config.ValidateConfig. serve reaches the same function
//     through aigateway.New, so neither path can drift from the other, and a
//     rule added there is inherited here for free.
//   - Properties of the BINARY — which provider IDs and plugin names exist —
//     are checked by validateReferences below, because config cannot ask a
//     build it does not see (an embedder registers its own via
//     Gateway.RegisterProvider and plugin.RegisterFactory).
//
// What it deliberately does not promise: anything that needs the deployment.
// It does not construct plugins, which means it does not resolve ${VAR}
// references or run a plugin's own Init. That is not an omission to fix later
// — envref resolves at construction on purpose, so a plugin whose config reads
// ${SOME_TOKEN} must validate on a build machine that has no such token, and a
// check that only passed where the secrets live would not be a pre-flight
// check. It likewise makes no claim about credentials, network reachability, or
// whether a provider will accept a given model: a target naming a real provider
// whose API key only exists in production is a valid config and is reported as
// one.
//
// That is the same line nginx -t, haproxy -c and `deck file validate` draw —
// parse and resolve fully, contact nothing. Envoy's --mode validate goes
// further and instantiates its objects, which is why it needs the referenced
// key material on disk; this command is deliberately on the other side of that
// split.
var ValidateCmd = &cobra.Command{
	Use:   "validate <config-file>",
	Short: "Validate a gateway configuration file (JSON or YAML)",
	Args:  cobra.ExactArgs(1),
	RunE:  runValidate,
}

func runValidate(cmd *cobra.Command, args []string) error {
	path := args[0]

	cfg, err := config.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := config.ValidateConfig(*cfg); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	if err := validateReferences(*cfg); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	pr := printerFromCmd(cmd)
	if pr.Format != FormatTable {
		return pr.Print(cfg)
	}

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "%s Config is valid\n", SymOK)
	_, _ = fmt.Fprintf(out, "  Strategy:  %s\n", cfg.Strategy.Mode)
	_, _ = fmt.Fprintf(out, "  Targets:   %d\n", len(cfg.Targets))

	var targetNames []string
	for _, t := range cfg.Targets {
		targetNames = append(targetNames, t.VirtualKey)
	}
	if len(targetNames) > 0 {
		_, _ = fmt.Fprintf(out, "  Providers: %s\n", strings.Join(targetNames, ", "))
	}

	if len(cfg.Plugins) > 0 {
		var pluginNames []string
		for _, p := range cfg.Plugins {
			status := "disabled"
			if p.Enabled {
				status = "enabled"
			}
			pluginNames = append(pluginNames, fmt.Sprintf("%s (%s)", p.Name, status))
		}
		_, _ = fmt.Fprintf(out, "  Plugins:   %s\n", strings.Join(pluginNames, ", "))
	}

	if len(cfg.Aliases) > 0 {
		_, _ = fmt.Fprintf(out, "  Aliases:   %d\n", len(cfg.Aliases))
	}
	return nil
}

// validateReferences resolves the config's cross-references against what this
// binary knows at build time. It lives here rather than in config.ValidateConfig
// because the answers are properties of the *binary*, not of the config: an
// embedder registers its own providers with Gateway.RegisterProvider and its own
// plugins with plugin.RegisterFactory, and ValidateConfig runs inside
// aigateway.New before either has had a chance to. For the ferrogw binary the
// two sets are fixed at compile time, so a name that does not resolve here can
// never resolve at all.
func validateReferences(cfg config.Config) error {
	for _, t := range cfg.Targets {
		if _, ok := providers.GetProviderEntry(t.VirtualKey); !ok {
			return fmt.Errorf(
				"target %q names no known provider; valid provider ids: %s",
				t.VirtualKey, strings.Join(knownProviderIDs(), ", "))
		}
	}

	for _, p := range cfg.Plugins {
		// Disabled entries are skipped when the gateway builds its plugin
		// manager, so a disabled plugin with an unknown name still starts and
		// serves. validate must not be stricter than serve, or it fails configs
		// that work.
		if !p.Enabled {
			continue
		}
		if _, ok := plugin.GetFactory(p.Name); !ok {
			available := plugin.RegisteredPlugins()
			sort.Strings(available)
			return fmt.Errorf("unknown plugin %q; available plugins: %s",
				p.Name, strings.Join(available, ", "))
		}
		switch plugin.Stage(p.Stage) {
		case plugin.StageBeforeRequest, plugin.StageAfterRequest, plugin.StageOnError:
		default:
			return fmt.Errorf("plugin %q: unknown stage %q; valid stages: %s, %s, %s",
				p.Name, p.Stage,
				plugin.StageBeforeRequest, plugin.StageAfterRequest, plugin.StageOnError)
		}
		// Rules the plugin publishes about its own config block. Only the
		// deployment-independent ones: plugin.ConfigValidator is contracted to
		// resolve no ${VAR} and touch nothing, so this keeps the promise above
		// that validate runs anywhere. A plugin's remaining checks stay in Init
		// and are still startup errors.
		if err := plugin.ValidateConfigFor(p.Name, p.Config); err != nil {
			return err
		}
	}
	return nil
}

// knownProviderIDs lists every provider this binary can build, sorted, for the
// error message. A rejected virtual_key is nearly always a typo, and the correct
// spelling is the only thing the operator actually needs.
func knownProviderIDs() []string {
	entries := providers.AllProviders()
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}
