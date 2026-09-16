// Package secretscan provides a secret-scan guardrail plugin that detects
// content carrying credentials and applies the configured action. Register it
// with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/secretscan"
//
// One plugins[] entry registers one stage, so a before_request-only entry
// screens the request alone: the model's response is screened only when this
// plugin is ALSO listed at after_request.
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
	// Two shapes under one kind: the classic prefixes, and the github_pat_
	// prefix a fine-grained personal access token carries — the format GitHub
	// now issues by default, whose body contains underscores the classic
	// character class excludes.
	{"github_token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{36,})\b`)},
	// The bot, user and legacy prefixes, plus the app-level (xapp-) and
	// rotation (xoxe-) prefixes, which carry the same access as the rest and
	// were passing screening while the kind reported itself selected.
	{"slack_token", regexp.MustCompile(`\b(?:xox[abeprs]|xapp)-[0-9A-Za-z-]{10,}\b`)},
	{"openai_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
	{"google_api_key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	// The API keys, plus the webhook signing secret, which authenticates
	// callbacks and is a credential in the same sense.
	{"stripe_key", regexp.MustCompile(`\b(?:[rs]k_(?:live|test)|whsec)_[0-9A-Za-z]{24,}\b`)},
	{"private_key", regexp.MustCompile(`-----BEGIN (?:RSA |DSA |EC |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY-----`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
}

// SecretScan detects content carrying credentials, in either direction, and
// applies the configured action. Only "block" rejects; under "warn" and "log"
// the detection is recorded and the content is forwarded.
//
// It can screen the response as well as the request — a model asked to "show me
// the config" will happily read a credential back out of its context, and a
// guardrail that only watched the prompt would miss it — but that direction
// needs its own after_request plugins[] entry, since one entry is one stage.
type SecretScan struct {
	secrets []secret
	action  string
}

// Name returns the plugin identifier.
func (s *SecretScan) Name() string { return "secret-scan" }

// Type returns the plugin lifecycle hook type.
func (s *SecretScan) Type() plugin.PluginType { return plugin.TypeGuardrail }

// actions are the actions this plugin can honour, read by Init and by
// ValidateConfig so the two cannot disagree about the set.
var actions = []string{plugin.ActionBlock, plugin.ActionWarn, plugin.ActionLog}

// ValidateConfig checks the action without compiling anything, so `ferrogw
// validate` and `ferrogw doctor` reject a misspelled one rather than leaving it
// to the startup or the config reload that follows. See plugin.ConfigValidator,
// and plugin.ValidateAction for why a ${VAR} reference is passed. The kinds and
// patterns lists are checked at Init, where every value is resolved.
func (s *SecretScan) ValidateConfig(config map[string]any) error {
	if err := plugin.ValidateAction(config["action"], plugin.ActionBlock, actions...); err != nil {
		return fmt.Errorf("secret-scan: %w", err)
	}
	return nil
}

// Init selects the curated kinds and compiles any custom patterns.
func (s *SecretScan) Init(config map[string]any) error {
	rawAction, err := plugin.StringSetting(config["action"], "action")
	if err != nil {
		return fmt.Errorf("secret-scan: %w", err)
	}
	action, err := plugin.NormalizeAction(rawAction, plugin.ActionBlock, actions...)
	if err != nil {
		return fmt.Errorf("secret-scan: action: %w", err)
	}
	s.action = action

	kinds, present := config["kinds"]
	selected, err := selectCurated(kinds, present)
	if err != nil {
		return err
	}
	s.secrets = selected

	custom, err := plugin.ListSetting(config["patterns"], "patterns")
	if err != nil {
		return fmt.Errorf("secret-scan: %w", err)
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

	// Checked after the custom patterns are appended, because an empty kinds
	// list alongside patterns is a real policy — scan for mine and none of the
	// curated ones. Only a plugin left with no pattern at all is the defect:
	// enabled in the catalog, scanning for nothing. Unreachable with the key
	// absent, which selects every curated kind.
	if len(s.secrets) == 0 {
		return fmt.Errorf("secret-scan: kinds is empty: omit the key to select every kind, or name at least one")
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
			if ctx.Err() != nil {
				return nil
			}
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
		// The caller has gone: stop scanning rather than walk the rest of a body
		// nobody is waiting for. Returning nil and not the context's error is
		// the whole point — an error from Execute means the plugin broke, which
		// the gateway answers 500 and the target's circuit breaker counts as a
		// fault. A caller hanging up is neither.
		if ctx.Err() != nil {
			return nil
		}
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

// selectCurated resolves the kinds selector. An ABSENT key selects every
// curated kind. A key that is present says something about the selection, so a
// value that cannot express one — a scalar, a mapping — is a load error rather
// than a silent widening: an operator who asked for one kind and got eight is
// scanning for patterns they never opted into.
func selectCurated(raw any, present bool) ([]secret, error) {
	if !present {
		out := make([]secret, len(curated))
		copy(out, curated)
		return out, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("secret-scan: kinds must be a list of kind names")
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
