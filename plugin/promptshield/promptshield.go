// Package promptshield provides a prompt-shield guardrail plugin that rejects
// prompt-injection and jailbreak attempts, matched by category over the common
// written forms. Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/promptshield"
package promptshield

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
)

func init() {
	plugin.RegisterFactory("prompt-shield", func() plugin.Plugin {
		return &PromptShield{}
	})
}

type category struct {
	name string
	re   *regexp.Regexp
}

// categories are the injection shapes recognised without configuration.
//
// These are heuristics over phrasing, not a classifier: they catch the common
// written forms and will miss an attacker who paraphrases. That is the honest
// ceiling of pattern matching, and it is why this plugin is one layer rather
// than the whole defence — pair it with an external provider for adversarial
// traffic.
var categories = []category{
	{"system_override", regexp.MustCompile(`(?i)(ignore\s+(previous|all)\s+instructions|disregard\s+your\s+instructions|forget\s+your\s+instructions|override\s+system\s+prompt)`)},
	{"role_manipulation", regexp.MustCompile(`(?i)(you\s+are\s+now|act\s+as\s+if\s+you\s+are|pretend\s+you\s+are|roleplay\s+as|assume\s+the\s+role)`)},
	{"instruction_leak", regexp.MustCompile(`(?i)(show\s+me\s+your\s+system\s+prompt|reveal\s+your\s+instructions|what\s+are\s+your\s+instructions|print\s+your\s+system\s+message|output\s+your\s+prompt)`)},
	{"delimiter_attack", regexp.MustCompile("(?i)(" + regexp.QuoteMeta("```system") + "|" + regexp.QuoteMeta("###SYSTEM") + "|" + regexp.QuoteMeta("[SYSTEM]") + "|" + regexp.QuoteMeta("<|system|>") + ")")},
}

// PromptShield rejects requests carrying prompt-injection attempts.
//
// It screens the request only. An injection attempt is something a caller
// sends; by after_request the model has already acted on it, and on a
// streamed response the tokens have already been delivered.
type PromptShield struct {
	enabled []category
	action  string
}

// Name returns the plugin identifier.
func (s *PromptShield) Name() string { return "prompt-shield" }

// Type returns the plugin lifecycle hook type.
func (s *PromptShield) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init selects the enabled categories and the action.
func (s *PromptShield) Init(config map[string]any) error {
	rawAction, _ := config["action"].(string)
	action, err := plugin.NormalizeAction(rawAction, plugin.ActionBlock, plugin.ActionBlock, plugin.ActionWarn, plugin.ActionLog)
	if err != nil {
		return fmt.Errorf("prompt-shield: action: %w", err)
	}
	s.action = action

	list, ok := config["categories"].([]any)
	if !ok {
		s.enabled = make([]category, len(categories))
		copy(s.enabled, categories)
		return nil
	}

	wanted := make(map[string]bool, len(list))
	for _, v := range list {
		if str, ok := v.(string); ok {
			wanted[strings.ToLower(strings.TrimSpace(str))] = true
		}
	}
	for _, c := range categories {
		if wanted[c.name] {
			s.enabled = append(s.enabled, c)
		}
	}
	return nil
}

// Execute screens the request. It returns early for any stage other than
// before_request: this plugin has nothing to do at after_request or on_error.
func (s *PromptShield) Execute(ctx context.Context, pctx *plugin.Context) error {
	if pctx.Stage != plugin.StageBeforeRequest || len(s.enabled) == 0 {
		return nil
	}
	if plugin.RejectUninspectable(pctx) {
		return nil
	}

	for text := range plugin.RequestText(pctx.Request) {
		for _, c := range s.enabled {
			if !c.re.MatchString(text) {
				continue
			}
			logger.Ctx(ctx).Warn("prompt-shield: injection attempt detected", "category", c.name)
			if s.action != plugin.ActionBlock {
				continue
			}
			pctx.Reject = true
			// The category, not the matched phrase: naming the category tells
			// a legitimate caller what to rephrase, while quoting the match
			// would let an attacker binary-search the pattern.
			pctx.Reason = "request blocked by content policy: " + c.name + " detected"
			return nil
		}
	}
	return nil
}

// Close releases resources owned by the plugin.
func (s *PromptShield) Close() error { return nil }
