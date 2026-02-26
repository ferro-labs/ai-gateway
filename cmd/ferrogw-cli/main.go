// Package main provides the ferrogw-cli command-line tool for managing the FerroGateway.
package main

import (
	"fmt"
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

const usage = `ferrogw-cli — FerroGateway command line tool

Usage:
  ferrogw-cli <command> [arguments]

Commands:
  validate <config-file>    Validate a gateway configuration file (JSON/YAML)
  plugins                   List all registered plugins
  version                   Print version info
  help                      Show this help
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(0)
	}

	switch os.Args[1] {
	case "validate":
		cmdValidate()
	case "plugins":
		cmdPlugins()
	case "version":
		cmdVersion()
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		fmt.Print(usage)
		os.Exit(1)
	}
}

func cmdValidate() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: ferrogw-cli validate <config-file>")
		os.Exit(1)
	}
	path := os.Args[2]

	cfg, err := aigateway.LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	if err := aigateway.ValidateConfig(*cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Validation error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Config is valid\n")
	fmt.Printf("  Strategy:  %s\n", cfg.Strategy.Mode)
	fmt.Printf("  Targets:   %d\n", len(cfg.Targets))

	var targetNames []string
	for _, t := range cfg.Targets {
		targetNames = append(targetNames, t.VirtualKey)
	}
	fmt.Printf("  Providers: %s\n", strings.Join(targetNames, ", "))

	if len(cfg.Plugins) > 0 {
		var pluginNames []string
		for _, p := range cfg.Plugins {
			status := "disabled"
			if p.Enabled {
				status = "enabled"
			}
			pluginNames = append(pluginNames, fmt.Sprintf("%s (%s)", p.Name, status))
		}
		fmt.Printf("  Plugins:   %s\n", strings.Join(pluginNames, ", "))
	}
}

func cmdPlugins() {
	names := plugin.RegisteredPlugins()
	if len(names) == 0 {
		fmt.Println("No plugins registered.")
		return
	}
	fmt.Println("Registered plugins:")
	for _, name := range names {
		factory, _ := plugin.GetFactory(name)
		p := factory()
		fmt.Printf("  %-20s type=%s\n", name, p.Type())
	}
}

func cmdVersion() {
	fmt.Printf("ferrogw-cli %s\n", version.String())
}
