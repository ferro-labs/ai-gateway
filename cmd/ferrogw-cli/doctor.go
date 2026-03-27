package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run offline environment and connectivity checks",
	RunE: func(_ *cobra.Command, _ []string) error {
		fmt.Println(clr(colorBold+colorWhite, "Environment Checks"))
		fmt.Println(clr(colorDim, "─────────────────────────────────────"))
		fmt.Println()

		// 1. Detect configured providers via env vars.
		fmt.Println(clr(colorBold, "Provider API Keys"))
		configured := 0
		for _, entry := range providers.AllProviders() {
			if cfg := providers.ProviderConfigFromEnv(entry); cfg != nil {
				fmt.Printf("  %s  %s\n", clr(colorGreen, "✓"), entry.ID)
				configured++
			}
		}
		// Also check Bedrock separately.
		if os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_ACCESS_KEY_ID") != "" {
			fmt.Printf("  %s  %s\n", clr(colorGreen, "✓"), "bedrock")
			configured++
		}
		if configured == 0 {
			fmt.Printf("  %s  No provider API keys detected\n", clr(colorYellow, "!"))
		} else {
			fmt.Printf("\n  %s provider(s) configured\n", clr(colorCyan, fmt.Sprintf("%d", configured)))
		}
		fmt.Println()

		// 2. Config file validation.
		fmt.Println(clr(colorBold, "Configuration"))
		cfgPath := os.Getenv("GATEWAY_CONFIG")
		if cfgPath == "" {
			fmt.Printf("  %s  GATEWAY_CONFIG not set (will use default fallback config)\n", clr(colorDim, "–"))
		} else {
			cfg, err := aigateway.LoadConfig(cfgPath)
			if err != nil {
				fmt.Printf("  %s  %s: %v\n", clr(colorRed, "✗"), cfgPath, err)
			} else if err := aigateway.ValidateConfig(*cfg); err != nil {
				fmt.Printf("  %s  %s: validation failed: %v\n", clr(colorRed, "✗"), cfgPath, err)
			} else {
				fmt.Printf("  %s  %s (strategy: %s, %d targets, %d plugins)\n",
					clr(colorGreen, "✓"), cfgPath,
					cfg.Strategy.Mode, len(cfg.Targets), len(cfg.Plugins))
			}
		}
		fmt.Println()

		// 3. Gateway connectivity.
		fmt.Println(clr(colorBold, "Gateway Connectivity"))
		gwURL := os.Getenv("FERROGW_URL")
		if gwURL == "" {
			gwURL = "http://localhost:8080"
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(gwURL + "/health")
		if err != nil {
			fmt.Printf("  %s  %s is unreachable\n", clr(colorRed, "✗"), gwURL)
		} else {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				fmt.Printf("  %s  %s is healthy\n", clr(colorGreen, "✓"), gwURL)
			} else {
				fmt.Printf("  %s  %s returned HTTP %d\n", clr(colorYellow, "!"), gwURL, resp.StatusCode)
			}
		}
		fmt.Println()
		return nil
	},
}
