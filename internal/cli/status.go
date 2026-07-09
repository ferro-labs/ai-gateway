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
	c := adminClientFromCmd(cmd)

	start := time.Now()
	var health map[string]any
	if err := c.GetHealth(cmd.Context(), "/health", &health); err != nil {
		fmt.Printf("  %s Gateway unreachable: %v\n", Clr(ColorRed, SymFAIL), err)
		return nil
	}
	latency := time.Since(start)

	// /health answers 503 while degraded (e.g. no providers configured). The
	// gateway is up; say what is actually wrong instead of "unreachable".
	// A body with no usable status is reported as such rather than as healthy.
	status, symbol, color := "unknown", SymWARN, ColorYellow
	if s, ok := health["status"].(string); ok {
		if s == "ok" {
			status, symbol, color = "healthy", SymOK, ColorGreen
		} else {
			status = s
		}
	}
	fmt.Printf("  %s %s -- %s (%s)\n",
		Clr(color, symbol),
		c.BaseURL,
		Clr(ColorBold+color, status),
		latency.Round(time.Millisecond),
	)

	if v, ok := health["version"]; ok {
		fmt.Printf("  Version: %s\n", Clr(ColorYellow, fmt.Sprint(v)))
	}

	// Try to get provider count.
	var provResp []map[string]any
	if err := c.Get(cmd.Context(), "/admin/providers", &provResp); err == nil && len(provResp) > 0 {
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
