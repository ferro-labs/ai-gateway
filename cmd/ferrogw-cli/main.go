// Package main provides the ferrogw-cli command-line tool for managing the Ferro Labs AI Gateway.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/version"
	"github.com/ferro-labs/ai-gateway/plugin"

	// Register built-in plugins so they appear in the plugin list.
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/cache"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/logger"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/maxtoken"
	_ "github.com/ferro-labs/ai-gateway/internal/plugins/wordfilter"
)

const usage = `ferrogw-cli — Ferro Labs AI Gateway command line tool

Usage:
  ferrogw-cli <command> [arguments]

Commands:
  validate <config-file>    Validate a gateway configuration file (JSON/YAML)
  plugins                   List all registered plugins
  version                   Print version info
  help                      Show this help
`

func main() {
	os.Exit(execute(os.Args, os.Stdout, os.Stderr))
}

func execute(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprint(stdout, usage)
		return 0
	}

	switch args[1] {
	case "validate":
		return cmdValidate(args, stdout, stderr)
	case "plugins":
		return cmdPlugins(stdout, stderr)
	case "version":
		return cmdVersion(stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "Unknown command: %s\n\n", args[1])
		fmt.Fprint(stdout, usage)
		return 1
	}
}

func cmdValidate(args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		fmt.Fprintln(stderr, "Usage: ferrogw-cli validate <config-file>")
		return 1
	}
	path := args[2]

	cfg, err := aigateway.LoadConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "Error loading config: %v\n", err)
		return 1
	}

	if err := aigateway.ValidateConfig(*cfg); err != nil {
		fmt.Fprintf(stderr, "Validation error: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "✓ Config is valid\n")
	fmt.Fprintf(stdout, "  Strategy:  %s\n", cfg.Strategy.Mode)
	fmt.Fprintf(stdout, "  Targets:   %d\n", len(cfg.Targets))

	var targetNames []string
	for _, t := range cfg.Targets {
		targetNames = append(targetNames, t.VirtualKey)
	}
	fmt.Fprintf(stdout, "  Providers: %s\n", strings.Join(targetNames, ", "))

	if len(cfg.Plugins) > 0 {
		var pluginNames []string
		for _, p := range cfg.Plugins {
			status := "disabled"
			if p.Enabled {
				status = "enabled"
			}
			pluginNames = append(pluginNames, fmt.Sprintf("%s (%s)", p.Name, status))
		}
		fmt.Fprintf(stdout, "  Plugins:   %s\n", strings.Join(pluginNames, ", "))
	}
	return 0
}

func cmdPlugins(stdout, stderr io.Writer) int {
	names := plugin.RegisteredPlugins()
	if len(names) == 0 {
		fmt.Fprintln(stdout, "No plugins registered.")
		return 0
	}
	fmt.Fprintln(stdout, "Registered plugins:")
	for _, name := range names {
		factory, _ := plugin.GetFactory(name)
		p := factory()
		fmt.Fprintf(stdout, "  %-20s type=%s\n", name, p.Type())
	}
	return 0
}

func cmdVersion(stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "ferrogw-cli %s\n", version.String())
	return 0
}
