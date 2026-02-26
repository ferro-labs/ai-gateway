package aigateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadConfig reads and parses a config file from the given path.
// Supported formats: JSON (.json), YAML (.yaml, .yml).
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing YAML config: %w", err)
		}
	case ".json":
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing JSON config: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported config file extension %q: use .json, .yaml, or .yml", ext)
	}

	return &cfg, nil
}

// ValidateConfig validates a Config for correctness.
func ValidateConfig(cfg Config) error {
	// Default to single strategy when mode is omitted to match runtime behavior.
	mode := cfg.Strategy.Mode
	if mode == "" {
		mode = ModeSingle
	}

	switch mode {
	case ModeSingle, ModeFallback, ModeLoadBalance, ModeConditional:
	default:
		return fmt.Errorf("unknown strategy mode: %q", cfg.Strategy.Mode)
	}

	if len(cfg.Targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}

	if mode == ModeConditional && len(cfg.Strategy.Conditions) == 0 {
		return fmt.Errorf("conditional strategy requires at least one condition")
	}

	if mode == ModeLoadBalance {
		var sum float64
		for _, t := range cfg.Targets {
			if t.Weight < 0 {
				return fmt.Errorf("target %q has negative weight", t.VirtualKey)
			}
			sum += t.Weight
		}
		if sum <= 0 {
			return fmt.Errorf("loadbalance strategy requires total weight > 0")
		}
	}

	return nil
}
