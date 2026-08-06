package config_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
	"github.com/ferro-labs/ai-gateway/plugin"
)

// multiStagePlugins names every built-in plugin whose Execute implements a
// before_request AND an after_request branch, chosen by whether the response is
// already present. A config entry registers exactly one stage, so such a plugin
// needs one entry per stage or only half of it ever runs — and half a plugin
// starts cleanly and reports nothing, so the loss is silent: the cache never
// stores, the budget never accumulates spend, the request logger never writes
// its completion row.
//
// Adding a plugin with an after_request branch: add its name here and give it
// both entries, with identical config, in config.example.yaml AND
// config.example.json.
var multiStagePlugins = []string{"budget", "request-logger", "response-cache"}

// exampleConfigs are the shipped examples — the same configuration in two
// formats, which is why they are also checked against each other.
var exampleConfigs = []string{"config.example.yaml", "config.example.json"}

// loadExample loads a shipped example config. They live at the repo root, one
// level up from this package.
func loadExample(t *testing.T, name string) *config.Config {
	t.Helper()
	cfg, err := config.LoadConfig(filepath.Join("..", name))
	if err != nil {
		t.Fatalf("LoadConfig(%q): %v", name, err)
	}
	return cfg
}

// entriesByStage indexes the named plugin's example-config entries by stage.
func entriesByStage(plugins []config.PluginConfig, name string) map[string]config.PluginConfig {
	out := make(map[string]config.PluginConfig, 2)
	for _, p := range plugins {
		if p.Name == name {
			out[p.Stage] = p
		}
	}
	return out
}

// pluginRegistrations reduces plugin entries to the tuple that decides which
// stage each plugin is wired into, in file order. Config values are left out
// deliberately: a YAML number decodes to int and the same JSON number to
// float64, so only the registration tuple compares meaningfully across formats.
func pluginRegistrations(plugins []config.PluginConfig) []string {
	out := make([]string, len(plugins))
	for i, p := range plugins {
		out[i] = fmt.Sprintf("%s|%s|%s|%t", p.Name, p.Type, p.Stage, p.Enabled)
	}
	return out
}

func TestExampleConfigs_MultiStagePluginsRegisterEveryStage(t *testing.T) {
	for _, file := range exampleConfigs {
		t.Run(file, func(t *testing.T) {
			cfg := loadExample(t, file)
			for _, name := range multiStagePlugins {
				entries := entriesByStage(cfg.Plugins, name)
				for _, stage := range []plugin.Stage{plugin.StageBeforeRequest, plugin.StageAfterRequest} {
					if _, ok := entries[string(stage)]; ok {
						continue
					}
					t.Errorf("%s: plugin %q has no %s entry (configured stages: %v).\n"+
						"It implements both stages inside one Execute and one entry registers one stage, "+
						"so the missing entry silently disables half the plugin.\n"+
						"Fix: add a second %q entry with stage: %s and the same config as the entry that is there.\n"+
						"If %q no longer branches on both stages, drop it from multiStagePlugins in this file instead.",
						file, name, stage, stageNames(entries), name, stage, name)
				}
				before, hasBefore := entries[string(plugin.StageBeforeRequest)]
				after, hasAfter := entries[string(plugin.StageAfterRequest)]
				if hasBefore && hasAfter && !reflect.DeepEqual(before.Config, after.Config) {
					t.Errorf("%s: plugin %q is configured differently at before_request and after_request "+
						"(%v vs %v). The two stages share one set of counters only when their config matches — "+
						"budget's store_id is what pairs the spend check with the spend record.",
						file, name, before.Config, after.Config)
				}
			}
		})
	}
}

// stageNames lists the stages present for a plugin, for a failure message.
func stageNames(entries map[string]config.PluginConfig) []string {
	out := make([]string, 0, len(entries))
	for stage := range entries {
		out = append(out, stage)
	}
	return out
}

// TestExampleConfigs_YAMLAndJSONAgree keeps the two example files one
// configuration in two formats. They have drifted independently before, which
// is how a plugin gained its second stage in one file and not the other.
func TestExampleConfigs_YAMLAndJSONAgree(t *testing.T) {
	yamlCfg := loadExample(t, "config.example.yaml")
	jsonCfg := loadExample(t, "config.example.json")

	if !reflect.DeepEqual(yamlCfg.Targets, jsonCfg.Targets) {
		t.Errorf("targets differ between the example configs:\n  yaml: %+v\n  json: %+v",
			yamlCfg.Targets, jsonCfg.Targets)
	}
	yamlPlugins, jsonPlugins := pluginRegistrations(yamlCfg.Plugins), pluginRegistrations(jsonCfg.Plugins)
	if !reflect.DeepEqual(yamlPlugins, jsonPlugins) {
		t.Errorf("plugin registrations (name|type|stage|enabled) differ between the example configs:\n  yaml: %v\n  json: %v",
			yamlPlugins, jsonPlugins)
	}
	if !reflect.DeepEqual(yamlCfg.Aliases, jsonCfg.Aliases) {
		t.Errorf("aliases differ between the example configs:\n  yaml: %v\n  json: %v",
			yamlCfg.Aliases, jsonCfg.Aliases)
	}
}
