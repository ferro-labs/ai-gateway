package bootstrap

import (
	"reflect"
	"slices"
	"strings"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/internal/admin/handlers"
	"github.com/ferro-labs/ai-gateway/pkg/logger"
)

// The gateway resolves its config from three sources, highest precedence first.
// A source only ever supplies the whole config, never individual keys: these
// are alternatives, not layers.
//
//	store     a config written through PUT /admin/config and kept in
//	          CONFIG_STORE_BACKEND. It wins because it is the newest operator
//	          intent and the thing a rollback restores; a file edit that has to
//	          beat it is applied by resetting the stored config, not by
//	          restarting.
//	file      the path named by GATEWAY_CONFIG.
//	defaults  fallback strategy over one target per credential the environment
//	          supplied.
const (
	sourceStore    = "store"
	sourceFile     = "file"
	sourceDefaults = "defaults"
)

// storedConfigReporter is implemented by a config manager that can say whether
// the config now running came out of its persistent store.
type storedConfigReporter interface {
	AdoptedStoredConfig() bool
}

// ResolveActiveConfig returns the config the gateway is actually running and
// logs, in one line, which source produced it.
//
// It exists because the startup log could otherwise describe a config that
// lost. The file is read and logged early, plugins are built from it and
// logged, and only afterwards does the config store — if one holds a config —
// replace all of it. A store-persisted config with no plugins section
// therefore used to leave a boot log reading "plugin registered
// name=word-filter" and "plugins loaded count=10" while the running gateway had
// none and the guardrail those lines named was not running.
//
// Naming the winner is the fix; naming it once is what makes the line worth
// grepping. Production systems that get this right report the source per
// setting — PostgreSQL's pg_settings exposes source, sourcefile and sourceline,
// and Envoy's /config_dump separates static_listeners from
// dynamic_active_listeners and leaves version_info empty for bootstrap. This
// gateway swaps whole configs rather than merging keys, so one line covers it.
//
// fileCfg is what GATEWAY_CONFIG produced, or nil when it was unset.
func ResolveActiveConfig(
	gw *aigateway.Gateway,
	cfgManager handlers.ConfigManager,
	fileCfg *config.Config,
	configStoreBackend string,
) config.Config {
	active := gw.GetConfig()

	fromStore := false
	if reporter, ok := cfgManager.(storedConfigReporter); ok {
		fromStore = reporter.AdoptedStoredConfig()
	}

	source := sourceDefaults
	switch {
	case fromStore:
		source = sourceStore
	case fileCfg != nil:
		source = sourceFile
	}

	attrs := []any{
		"source", source,
		"strategy", string(active.Strategy.Mode),
		"targets", len(active.Targets),
		"plugins", enabledPluginCount(active.Plugins),
		"max_request_bytes", effectiveMaxRequestBytes(active),
	}
	if path := configFilePath(); path != "" {
		attrs = append(attrs, "file", path)
	}
	if fromStore {
		attrs = append(attrs, "config_store", configStoreBackend)
	}
	logger.Default().Info("active config resolved", attrs...)

	if fromStore && fileCfg != nil {
		warnConfigSourceDivergence(*fileCfg, active, configStoreBackend)
	}

	return active
}

// warnConfigSourceDivergence reports a config file that was read, validated and
// then discarded because the store held a different config.
//
// It is a warning rather than an informational line because the two sources
// disagreeing is the operator's problem, not the gateway's: the person who
// edited the file believes it is in force, and the gap can silently remove a
// guardrail. Systems that manage layered config treat disagreement as
// reportable state for the same reason — PostgreSQL's pg_file_settings marks a
// file entry applied=false when a later source overrode it, and Envoy keeps a
// rejected update's version and reason next to the live one instead of dropping
// it.
//
// Identical configs are not a divergence and stay silent.
func warnConfigSourceDivergence(fileCfg, active config.Config, configStoreBackend string) {
	if reflect.DeepEqual(fileCfg, active) {
		return
	}

	attrs := []any{
		"file", configFilePath(),
		"config_store", configStoreBackend,
		"file_strategy", string(fileCfg.Strategy.Mode),
		"active_strategy", string(active.Strategy.Mode),
		"file_targets", len(fileCfg.Targets),
		"active_targets", len(active.Targets),
		"file_plugins", enabledPluginCount(fileCfg.Plugins),
		"active_plugins", enabledPluginCount(active.Plugins),
	}
	// The dropped plugins are called out by name because that is the shape this
	// costs most: a guardrail present in the file and absent from the store
	// stops filtering while every log line about it still reads as success.
	if dropped := droppedPlugins(fileCfg.Plugins, active.Plugins); len(dropped) > 0 {
		attrs = append(attrs, "plugins_dropped", strings.Join(dropped, ","))
	}

	logger.Default().Warn(
		"config file superseded by the persisted config; the file is not applied while the config store holds one "+
			"(DELETE /admin/config discards the stored config so the file applies again)",
		attrs...,
	)
}

// droppedPlugins names the enabled plugins the file declared that the active
// config does not run.
func droppedPlugins(filePlugins, activePlugins []config.PluginConfig) []string {
	live := make(map[string]bool, len(activePlugins))
	for _, p := range activePlugins {
		if p.Enabled {
			live[p.Name] = true
		}
	}
	var dropped []string
	for _, p := range filePlugins {
		if p.Enabled && !live[p.Name] && !slices.Contains(dropped, p.Name) {
			dropped = append(dropped, p.Name)
		}
	}
	return dropped
}

// enabledPluginCount counts the plugins that actually run. A disabled entry is
// config, not a plugin, and counting it is how "plugins loaded count=10" came
// to describe a gateway running none.
func enabledPluginCount(plugins []config.PluginConfig) int {
	n := 0
	for _, p := range plugins {
		if p.Enabled {
			n++
		}
	}
	return n
}

// effectiveMaxRequestBytes reports the cap the router enforces, resolving the
// omitted-means-default case so the logged number is the enforced number.
func effectiveMaxRequestBytes(cfg config.Config) int64 {
	if cfg.MaxRequestBytes > 0 {
		return cfg.MaxRequestBytes
	}
	return config.DefaultMaxRequestBytes
}
