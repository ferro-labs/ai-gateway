package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check the health of a running gateway instance",
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := clientFromCmd(cmd)

		// Health check.
		start := time.Now()
		var health map[string]interface{}
		if err := c.get("/health", &health); err != nil {
			fmt.Printf("%s  Gateway at %s is unreachable\n",
				clr(colorRed, "✗"), c.baseURL)
			return fmt.Errorf("health check failed: %w", err)
		}
		latency := time.Since(start)

		fmt.Printf("%s  Gateway is %s\n",
			clr(colorGreen, "✓"),
			clr(colorGreen+colorBold, "healthy"))
		fmt.Printf("   URL:      %s\n", c.baseURL)
		fmt.Printf("   Latency:  %s\n", latency.Round(time.Millisecond))

		if v, ok := health["version"]; ok {
			fmt.Printf("   Version:  %s\n", clr(colorYellow, fmt.Sprintf("%v", v)))
		}

		// Try to fetch providers.
		var providers []map[string]interface{}
		if err := c.get("/admin/providers", &providers); err == nil && len(providers) > 0 {
			totalModels := 0
			for _, p := range providers {
				if mc, ok := p["model_count"].(float64); ok {
					totalModels += int(mc)
				}
			}
			fmt.Printf("   Providers: %s (%d models)\n",
				clr(colorCyan, fmt.Sprintf("%d", len(providers))),
				totalModels)
		}

		fmt.Println()
		return nil
	},
}
