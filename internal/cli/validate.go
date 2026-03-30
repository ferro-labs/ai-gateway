package cli

import (
	"fmt"
	"strings"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/spf13/cobra"
)

// ValidateCmd validates a gateway configuration file.
var ValidateCmd = &cobra.Command{
	Use:   "validate <config-file>",
	Short: "Validate a gateway configuration file (JSON or YAML)",
	Args:  cobra.ExactArgs(1),
	RunE:  runValidate,
}

func runValidate(cmd *cobra.Command, args []string) error {
	path := args[0]

	cfg, err := aigateway.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := aigateway.ValidateConfig(*cfg); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	format, _ := cmd.Root().PersistentFlags().GetString("format")
	pr := NewPrinter(format)

	if pr.Format != FormatTable {
		return pr.Print(cfg)
	}

	fmt.Println("✓ Config is valid")
	fmt.Printf("  Strategy:  %s\n", cfg.Strategy.Mode)
	fmt.Printf("  Targets:   %d\n", len(cfg.Targets))

	var targetNames []string
	for _, t := range cfg.Targets {
		targetNames = append(targetNames, t.VirtualKey)
	}
	if len(targetNames) > 0 {
		fmt.Printf("  Providers: %s\n", strings.Join(targetNames, ", "))
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
		fmt.Printf("  Plugins:   %s\n", strings.Join(pluginNames, ", "))
	}

	if len(cfg.Aliases) > 0 {
		fmt.Printf("  Aliases:   %d\n", len(cfg.Aliases))
	}
	return nil
}
