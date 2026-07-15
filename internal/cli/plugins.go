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
	c := adminClientFromCmd(cmd)

	var plugins []struct {
		Name    string `json:"name" yaml:"name"`
		Type    string `json:"type" yaml:"type"`
		Enabled bool   `json:"enabled" yaml:"enabled"`
	}
	if err := c.Get(cmd.Context(), "/admin/plugins", &plugins); err != nil {
		return err
	}

	pr := printerFromCmd(cmd)
	if pr.Format != FormatTable {
		return pr.Print(plugins)
	}

	out := cmd.OutOrStdout()
	if len(plugins) == 0 {
		_, _ = fmt.Fprintln(out, "No plugins registered.")
		return nil
	}

	_, _ = fmt.Fprintf(out, "%-24s %-16s %s\n", "NAME", "TYPE", "ENABLED")
	_, _ = fmt.Fprintf(out, "%-24s %-16s %s\n", "----", "----", "-------")
	for _, p := range plugins {
		enabled := boolNo
		if p.Enabled {
			enabled = boolYes
		}
		_, _ = fmt.Fprintf(out, "%-24s %-16s %s\n", p.Name, p.Type, enabled)
	}
	return nil
}
