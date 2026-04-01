package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// PluginsCmd lists plugins registered in a running gateway instance.
var PluginsCmd = &cobra.Command{
	Use:   "plugins",
	Short: "List plugins registered in a running gateway instance",
	RunE:  runPlugins,
}

func runPlugins(cmd *cobra.Command, _ []string) error {
	flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
	flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
	c := NewAdminClient(flagURL, flagKey)

	var plugins []struct {
		Name    string `json:"name" yaml:"name"`
		Type    string `json:"type" yaml:"type"`
		Enabled bool   `json:"enabled" yaml:"enabled"`
	}
	if err := c.Get("/admin/plugins", &plugins); err != nil {
		return err
	}

	format, _ := cmd.Root().PersistentFlags().GetString("format")
	pr := NewPrinter(format)
	if pr.Format != FormatTable {
		return pr.Print(plugins)
	}

	if len(plugins) == 0 {
		fmt.Println("No plugins registered.")
		return nil
	}

	fmt.Printf("%-24s %-16s %s\n", "NAME", "TYPE", "ENABLED")
	fmt.Printf("%-24s %-16s %s\n", "----", "----", "-------")
	for _, p := range plugins {
		enabled := "no"
		if p.Enabled {
			enabled = "yes"
		}
		fmt.Printf("%-24s %-16s %s\n", p.Name, p.Type, enabled)
	}
	return nil
}
