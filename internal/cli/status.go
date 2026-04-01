package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// StatusCmd checks the health of a running gateway.
var StatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check health of a running gateway instance",
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, _ []string) error {
	flagURL, _ := cmd.Root().PersistentFlags().GetString("gateway-url")
	flagKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
	c := NewAdminClient(flagURL, flagKey)

	start := time.Now()
	var health map[string]any
	if err := c.Get("/health", &health); err != nil {
		fmt.Printf("  %s Gateway unreachable: %v\n", Clr(ColorRed, SymFAIL), err)
		return nil
	}
	latency := time.Since(start)

	fmt.Printf("  %s %s -- %s (%s)\n",
		Clr(ColorGreen, SymOK),
		c.BaseURL,
		Clr(ColorBold+ColorGreen, "healthy"),
		latency.Round(time.Millisecond),
	)

	if v, ok := health["version"]; ok {
		fmt.Printf("  Version: %s\n", Clr(ColorYellow, fmt.Sprint(v)))
	}

	// Try to get provider count.
	var provResp []map[string]any
	if err := c.Get("/admin/providers", &provResp); err == nil && len(provResp) > 0 {
		models := 0
		for _, p := range provResp {
			if m, ok := p["models"].([]any); ok {
				models += len(m)
			}
		}
		fmt.Printf("  Providers: %s (%d models)\n",
			Clr(ColorCyan, fmt.Sprintf("%d", len(provResp))), models)
	}

	return nil
}
