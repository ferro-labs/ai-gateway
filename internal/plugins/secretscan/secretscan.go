// Package secretscan provides a guardrail plugin for secret detection.
package secretscan

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/plugins/guardrailutil"
	"github.com/ferro-labs/ai-gateway/plugin"
)

const (
	secretScanActionBlock = "block"
	secretScanActionWarn  = "warn"
	secretScanActionLog   = "log"
)

func init() {
	plugin.RegisterFactory("secret-scan", func() plugin.Plugin {
		return &SecretScan{}
	})
}

type secretPattern struct {
	name          string
	pattern       string
	minEntropy    float64
	excludePrefix string
}

var builtinSecretPatterns = []secretPattern{
	{name: "aws_access_key", pattern: `\bAKIA[0-9A-Z]{16}\b`},
	{name: "aws_secret_key", pattern: `[0-9a-zA-Z/+]{40}`, minEntropy: 4.5},
	{name: "gcp_private_key", pattern: `-----BEGIN (RSA )?PRIVATE KEY-----`},
	{name: "openai_key", pattern: `\bsk-[a-zA-Z0-9]{48}\b`, excludePrefix: "sk-ferro-"},
	{name: "anthropic_key", pattern: `\bsk-ant-[a-zA-Z0-9\-_]{95}\b`},
	{name: "huggingface_token", pattern: `\bhf_[a-zA-Z]{34}\b`},
	{name: "github_pat_classic", pattern: `\bghp_[a-zA-Z0-9]{36}\b`},
	{name: "github_pat_fine", pattern: `\bgithub_pat_[a-zA-Z0-9_]{82}\b`},
	{name: "gitlab_pat", pattern: `\bglpat-[a-zA-Z0-9\-_]{20}\b`},
	{name: "stripe_secret", pattern: `\bsk_(live|test)_[a-zA-Z0-9]{24}\b`},
	{name: "stripe_restricted", pattern: `\brk_(live|test)_[a-zA-Z0-9]{24}\b`},
	{name: "postgres_dsn", pattern: `postgres(ql)?://[^:]+:[^@]+@`},
	{name: "mongodb_dsn", pattern: `mongodb(\+srv)?://[^:]+:[^@]+@`},
	{name: "high_entropy_hex", pattern: `[0-9a-f]{32,64}`, minEntropy: 3.8},
	{name: "high_entropy_b64", pattern: `[A-Za-z0-9+/]{40,}={0,2}`, minEntropy: 4.2},
}

type compiledSecretPattern struct {
	name          string
	compiled      *regexp.Regexp
	minEntropy    float64
	excludePrefix string
}

// SecretScan detects hardcoded credentials and secrets in request content.
type SecretScan struct {
	action       string
	entropyCheck bool
	patterns     []compiledSecretPattern
}

// Name returns the plugin identifier.
func (s *SecretScan) Name() string { return "secret-scan" }

// Type returns the plugin lifecycle type.
func (s *SecretScan) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init configures the plugin.
func (s *SecretScan) Init(config map[string]interface{}) error {
	s.action = parseAction(config)
	s.entropyCheck = true
	if raw, ok := config["entropy_check"].(bool); ok {
		s.entropyCheck = raw
	}

	selectedSet, err := parsePatternSet(config)
	if err != nil {
		return err
	}

	s.patterns = nil
	for _, pattern := range builtinSecretPatterns {
		if len(selectedSet) > 0 {
			if _, ok := selectedSet[pattern.name]; !ok {
				continue
			}
		}

		compiled, err := regexp.Compile(pattern.pattern)
		if err != nil {
			return fmt.Errorf("compile secret pattern %s: %w", pattern.name, err)
		}

		s.patterns = append(s.patterns, compiledSecretPattern{
			name:          pattern.name,
			compiled:      compiled,
			minEntropy:    pattern.minEntropy,
			excludePrefix: pattern.excludePrefix,
		})
	}

	return nil
}

// Execute scans request messages and blocks/warns/logs when secrets are found.
func (s *SecretScan) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx == nil || pctx.Response != nil || pctx.Request == nil {
		return nil
	}
	if pctx.Metadata == nil {
		pctx.Metadata = make(map[string]interface{})
	}

	detected := make(map[string]struct{})
	for _, message := range pctx.Request.Messages {
		for _, pattern := range s.patterns {
			if patternMatches(pattern, message.Content, s.entropyCheck) {
				detected[pattern.name] = struct{}{}
			}
		}
	}

	if len(detected) == 0 {
		return nil
	}

	names := make([]string, 0, len(detected))
	for name := range detected {
		names = append(names, name)
	}
	sort.Strings(names)

	switch s.action {
	case secretScanActionBlock:
		pctx.Reject = true
		pctx.Reason = "secret detected: " + strings.Join(names, ", ")
	case secretScanActionWarn, secretScanActionLog:
		pctx.Metadata["secrets_detected"] = names
	}

	return nil
}

func parseAction(config map[string]interface{}) string {
	if config == nil {
		return secretScanActionBlock
	}
	raw, ok := config["action"].(string)
	if !ok {
		return secretScanActionBlock
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case secretScanActionBlock, secretScanActionWarn, secretScanActionLog:
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return secretScanActionBlock
	}
}

func parsePatternSet(config map[string]interface{}) (map[string]struct{}, error) {
	names := guardrailutil.ParseStringList(config, "patterns")
	if len(names) == 0 {
		return nil, nil
	}

	known := builtInPatternSet()
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("unknown secret pattern: %s", name)
		}
		set[name] = struct{}{}
	}

	return set, nil
}

func patternMatches(pattern compiledSecretPattern, content string, entropyCheck bool) bool {
	matches := pattern.compiled.FindAllString(content, -1)
	for _, match := range matches {
		if pattern.excludePrefix != "" && strings.HasPrefix(match, pattern.excludePrefix) {
			continue
		}
		if entropyCheck && pattern.minEntropy > 0 && shannonEntropy(match) < pattern.minEntropy {
			continue
		}
		return true
	}
	return false
}

func builtInPatternSet() map[string]struct{} {
	set := make(map[string]struct{}, len(builtinSecretPatterns))
	for _, pattern := range builtinSecretPatterns {
		set[pattern.name] = struct{}{}
	}
	return set
}

// shannonEntropy computes bits-per-byte entropy for a string.
func shannonEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	freq := make(map[rune]float64)
	for _, r := range s {
		freq[r]++
	}
	n := float64(len(s))
	var h float64
	for _, c := range freq {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}
