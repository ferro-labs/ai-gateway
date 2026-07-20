package aigateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/tracingpolicy"
	pubmcp "github.com/ferro-labs/ai-gateway/mcp"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"gopkg.in/yaml.v3"
)

// LoadConfig reads and parses a config file from the given path.
// Supported formats: JSON (.json), YAML (.yaml, .yml).
//
// Decoding is strict: an unknown or misspelled key against the typed schema is
// rejected rather than silently ignored, so a typo cannot quietly disable the
// setting it was meant to change. Free-form config blocks (plugin and exporter
// "config" maps) accept arbitrary keys and are preserved verbatim. Exactly one
// document is permitted: trailing JSON data or a second YAML document (after
// "---") is rejected rather than silently dropped. The returned Config is
// Normalize-d so it carries its effective defaults.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: config path is an operator-supplied startup argument, not request input
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		// An empty document decodes to io.EOF; treat it as an empty config so
		// validation (not decoding) reports the missing required fields.
		if err := dec.Decode(&cfg); err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("parsing YAML config: %w", err)
			}
		} else if err := dec.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
			// A valid first document must be the only one. Reject a trailing
			// second document rather than silently ignoring it, mirroring the
			// JSON path's trailing-data rejection.
			return nil, fmt.Errorf("parsing YAML config: unexpected additional document; only one is allowed")
		}
	case ".json":
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("parsing JSON config: %w", err)
		}
		// Reject trailing data after the top-level object, matching the
		// strictness of a whole-document json.Unmarshal.
		if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parsing JSON config: unexpected data after top-level object")
		}
	default:
		return nil, fmt.Errorf("unsupported config file extension %q: use .json, .yaml, or .yml", ext)
	}

	cfg.Normalize()
	if cfg.APIVersion != CurrentAPIVersion {
		slog.Warn("config: unrecognized apiVersion; proceeding for forward compatibility",
			"apiVersion", cfg.APIVersion, "expected", CurrentAPIVersion)
	}

	return &cfg, nil
}

// ValidateConfig validates a Config for correctness.
func ValidateConfig(cfg Config) error {
	// Validate against normalized defaults (e.g. an omitted strategy mode means
	// single). cfg is passed by value, so normalizing this copy leaves the
	// caller's Config untouched while keeping validation consistent with the
	// Config LoadConfig returns.
	cfg.Normalize()

	switch cfg.Strategy.Mode {
	case ModeSingle, ModeFallback, ModeLoadBalance, ModeConditional, ModeLatency, ModeCostOptimized,
		ModeContentBased, ModeABTest:
	default:
		return fmt.Errorf("unknown strategy mode: %q", cfg.Strategy.Mode)
	}

	if len(cfg.Targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}

	for _, t := range cfg.Targets {
		if err := validateTargetConcurrency(t); err != nil {
			return err
		}
	}

	if cfg.RequestTimeout != "" {
		d, err := time.ParseDuration(cfg.RequestTimeout)
		if err != nil {
			return fmt.Errorf("invalid request_timeout %q: %w", cfg.RequestTimeout, err)
		}
		if d <= 0 {
			return fmt.Errorf("request_timeout must be positive, got %q", cfg.RequestTimeout)
		}
	}

	if cfg.Strategy.Mode == ModeConditional && len(cfg.Strategy.Conditions) == 0 {
		return fmt.Errorf("conditional strategy requires at least one condition")
	}

	if cfg.Strategy.Mode == ModeContentBased && len(cfg.Strategy.ContentConditions) == 0 {
		return fmt.Errorf("content-based strategy requires at least one content_condition")
	}

	if cfg.Strategy.Mode == ModeABTest && len(cfg.Strategy.ABVariants) == 0 {
		return fmt.Errorf("ab-test strategy requires at least one ab_variant")
	}

	if cfg.Strategy.Mode == ModeCostOptimized {
		switch cfg.Strategy.UnpricedStrategy {
		case "", unpricedStrategyFallback, unpricedStrategySkip, unpricedStrategyAllow:
		default:
			return fmt.Errorf("cost-optimized unpriced_strategy must be one of fallback, skip, allow")
		}
	}

	if cfg.Strategy.Mode == ModeLoadBalance {
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

	// Validate observability.tracing.privacy_level against the single source of
	// truth in the internal tracingpolicy package (shared with internal/otel).
	if err := tracingpolicy.ValidatePrivacyLevel(cfg.Observability.Tracing.PrivacyLevel); err != nil {
		return fmt.Errorf("observability.tracing: %w", err)
	}

	// Validate compatibility.on_unsupported_param: "" (⇒ warn), warn, drop, reject.
	if _, ok := core.ParseUnsupportedParamMode(cfg.Compatibility.OnUnsupportedParam); !ok {
		return fmt.Errorf("compatibility.on_unsupported_param must be one of warn, drop, reject")
	}

	// Validate aliases: no alias may point to another alias (no cycles/chains).
	for name, target := range cfg.Aliases {
		if name == "" {
			return fmt.Errorf("alias name must not be empty")
		}
		if target == "" {
			return fmt.Errorf("alias %q must not map to an empty string", name)
		}
		if name == target {
			return fmt.Errorf("alias %q must not point to itself", name)
		}
		if _, chainedAlias := cfg.Aliases[target]; chainedAlias {
			return fmt.Errorf("alias %q points to another alias %q; chained aliases are not supported", name, target)
		}
	}

	if err := validateMCPServers(cfg.MCPServers); err != nil {
		return err
	}

	return nil
}

// validateMCPServers checks each server's transport selection: exactly one of
// URL (Streamable HTTP) or Command (stdio) must be set. Leaving both empty fails
// later during async initialization with a confusing error; leaving both set
// silently prefers stdio and drops Headers, which is surprising.
func validateMCPServers(servers []pubmcp.ServerConfig) error {
	seen := make(map[string]struct{}, len(servers))
	for i, mcpCfg := range servers {
		// Name is the registry key. An empty one produces a server nothing can
		// refer to in logs or metrics; a duplicate silently replaces the earlier
		// server, taking its tool mappings with it.
		if strings.TrimSpace(mcpCfg.Name) == "" {
			return fmt.Errorf("mcp server at index %d: name is required", i)
		}
		if _, dup := seen[mcpCfg.Name]; dup {
			return fmt.Errorf("mcp server %q: duplicate name; each server needs a unique name", mcpCfg.Name)
		}
		seen[mcpCfg.Name] = struct{}{}

		hasURL := mcpCfg.URL != ""
		hasCommand := mcpCfg.Command != ""
		switch {
		case hasURL && hasCommand:
			return fmt.Errorf("mcp server %q: url and command are mutually exclusive; set exactly one to select transport", mcpCfg.Name)
		case !hasURL && !hasCommand:
			return fmt.Errorf("mcp server %q: either url (Streamable HTTP) or command (stdio) is required", mcpCfg.Name)
		}
	}
	return nil
}

// validateTargetConcurrency bounds a target's concurrency block. The ceiling is
// what keeps a mistyped max_concurrency from silently turning the limiter into a
// no-op: the value becomes the in-flight slot count, so an absurd one admits
// every request and the cap the operator asked for never applies.
func validateTargetConcurrency(t Target) error {
	if t.Concurrency == nil {
		return nil
	}
	if t.Concurrency.MaxConcurrency <= 0 {
		return fmt.Errorf("target %q: concurrency.max_concurrency must be positive (omit the concurrency block to leave the target unlimited)", t.VirtualKey)
	}
	if t.Concurrency.MaxConcurrency > MaxTargetConcurrency {
		return fmt.Errorf("target %q: concurrency.max_concurrency exceeds the limit of %d", t.VirtualKey, MaxTargetConcurrency)
	}
	if t.Concurrency.QueueSize < 0 {
		return fmt.Errorf("target %q: concurrency.queue_size cannot be negative", t.VirtualKey)
	}
	if t.Concurrency.QueueSize > MaxTargetConcurrency {
		return fmt.Errorf("target %q: concurrency.queue_size exceeds the limit of %d", t.VirtualKey, MaxTargetConcurrency)
	}
	return nil
}
