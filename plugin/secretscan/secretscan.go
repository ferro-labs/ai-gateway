// Package secretscan provides a secret-scan guardrail plugin that rejects
// content carrying credentials. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/secretscan"
package secretscan

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
)

func init() {
	plugin.RegisterFactory("secret-scan", func() plugin.Plugin {
		return &SecretScan{}
	})
}

type secret struct {
	name string
	re   *regexp.Regexp
}

// curated is the default credential pattern set, selectable by kind.
//
// Each entry matches a credential's STRUCTURE — a fixed prefix and a length —
// rather than guessing from surrounding words. High-entropy-string heuristics
// belong to a scanner with a corpus to tune against; here a false positive
// costs a developer their request, so the patterns stay literal.
var curated = []secret{
	{"aws_access_key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"github_token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`)},
	{"slack_token", regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}\b`)},
	{"openai_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
	{"google_api_key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"stripe_key", regexp.MustCompile(`\b[rs]k_(?:live|test)_[0-9A-Za-z]{24,}\b`)},
	{"private_key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
}

// SecretScan rejects content carrying credentials, in either direction.
//
// It screens the response as well as the request: a model asked to "show me the
// config" will happily read a credential back out of its context, and a
// guardrail that only watched the prompt would miss it.
type SecretScan struct {
	secrets []secret
	action  string
}

// Name returns the plugin identifier.
func (s *SecretScan) Name() string { return "secret-scan" }

// Type returns the plugin lifecycle hook type.
func (s *SecretScan) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init selects the curated kinds and compiles any custom patterns.
func (s *SecretScan) Init(config map[string]any) error {
	rawAction, _ := config["action"].(string)
	action, err := plugin.NormalizeAction(rawAction, plugin.ActionBlock, plugin.ActionBlock, plugin.ActionWarn, plugin.ActionLog)
	if err != nil {
		return fmt.Errorf("secret-scan: action: %w", err)
	}
	s.action = action

	selected, err := selectCurated(config["kinds"])
	if err != nil {
		return err
	}
	s.secrets = selected

	custom, ok := config["patterns"].([]any)
	if !ok {
		return nil
	}
	for i, v := range custom {
		str, ok := v.(string)
		if !ok {
			return fmt.Errorf("secret-scan: patterns[%d] must be a string", i)
		}
		re, err := regexp.Compile(str)
		if err != nil {
			return fmt.Errorf("secret-scan: patterns[%d]: %w", i, err)
		}
		s.secrets = append(s.secrets, secret{name: fmt.Sprintf("custom_%d", i+1), re: re})
	}
	return nil
}

// Execute screens the request at before_request and the response at
// after_request.
func (s *SecretScan) Execute(ctx context.Context, pctx *plugin.Context) error {
	if len(s.secrets) == 0 {
		return nil
	}

	if pctx.Stage == plugin.StageAfterRequest {
		for text := range plugin.ResponseText(pctx.Response) {
			if s.screen(ctx, pctx, text, "response") {
				return nil
			}
		}
		return nil
	}

	if plugin.RejectUninspectable(pctx) {
		return nil
	}
	for text := range plugin.RequestText(pctx.Request) {
		if s.screen(ctx, pctx, text, "request") {
			return nil
		}
	}
	return nil
}

// Close releases resources owned by the plugin.
func (s *SecretScan) Close() error { return nil }

// screen reports whether the content was blocked. The credential itself is
// never logged and never reaches the reason — a secret in a log line is still a
// leaked secret.
func (s *SecretScan) screen(ctx context.Context, pctx *plugin.Context, content, subject string) bool {
	for _, sec := range s.secrets {
		if !sec.re.MatchString(content) {
			continue
		}
		logger.Ctx(ctx).Warn("secret-scan: credential detected in "+subject, "kind", sec.name)
		if s.action != plugin.ActionBlock {
			continue
		}
		pctx.Reject = true
		pctx.Reason = subject + " blocked by content policy: " + sec.name + " detected"
		return true
	}
	return false
}

func selectCurated(raw any) ([]secret, error) {
	list, ok := raw.([]any)
	if !ok {
		out := make([]secret, len(curated))
		copy(out, curated)
		return out, nil
	}

	known := make([]string, len(curated))
	for i, sec := range curated {
		known[i] = sec.name
	}

	wanted := make(map[string]bool, len(list))
	for i, v := range list {
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("secret-scan: kinds[%d] must be a string", i)
		}
		name := strings.ToLower(strings.TrimSpace(str))
		// A name matching nothing selects nothing, which registers a plugin the
		// catalog reports as enabled and that scans for no credential at all.
		if !slices.Contains(known, name) {
			return nil, fmt.Errorf("secret-scan: unrecognized kind %q: must be one of %q", str, known)
		}
		wanted[name] = true
	}

	out := make([]secret, 0, len(curated))
	for _, sec := range curated {
		if wanted[sec.name] {
			out = append(out, sec)
		}
	}
	return out, nil
}
