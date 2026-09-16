// Package promptshield provides a prompt-shield guardrail plugin that detects
// prompt-injection and jailbreak attempts, matched by category over the common
// written forms, and applies the configured action. Register it with a blank
// import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/promptshield"
//
// # What this plugin can and cannot do
//
// The categories are heuristics over phrasing, not a classifier. They catch the
// common written forms and will miss an attacker who paraphrases — that is the
// honest ceiling of pattern matching, and it does not move with tuning. Run
// this as one layer and pair it with an external provider for adversarial
// traffic; a deployment relying on it alone is relying on an attacker writing
// the attempt the usual way.
//
// It screens the request only. An injection attempt is something a caller
// sends: by after_request the model has already acted on it, and on a streamed
// response the tokens have already been delivered, so there would be nothing
// left to withhold.
//
// Only action "block" rejects. Under "warn" and "log" the detection is recorded
// by category and the prompt reaches the model anyway, which is what those
// actions are for — measuring a pattern's false-positive rate before enforcing
// on it.
package promptshield

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
	plugin.RegisterFactory("prompt-shield", func() plugin.Plugin {
		return &PromptShield{}
	})
}

type category struct {
	name string
	re   *regexp.Regexp
}

// categories are the injection shapes recognised without configuration. See
// the package doc for what pattern matching can and cannot catch.
//
// role_manipulation is deliberately narrower than "any sentence about roles":
// "you are now" is ordinary account-state phrasing ("you are now enrolled in
// the premium plan"), and "assume the role" is ordinary business English for
// a job or workflow assignment ("assume the role of team lead"). Neither
// alternative is safe to match on its own. "you are now" is dropped outright
// — a real persona injection ("you are now DAN", "you are now unrestricted")
// almost always also carries one of the surviving, adversarially-framed
// alternatives, and missing that weak a signal costs less than blocking
// routine support and HR text. "assume the role" is kept only when the target
// is a privilege persona (system, assistant, admin, administrator) and the
// persona ends there — "assume the role of system" still matches; "assume the
// role of team lead" and "assume the role of systems architect" do not. The
// trailing word boundary is load-bearing: without it the persona group matched
// inside a longer word and blocked real job titles.
//
// "developer" is deliberately absent from the group. "Assume the role of
// developer" is routine workflow prose, and a developer persona is a far
// weaker privilege claim than system or admin — by this plugin's own cost
// calculus, under-blocking a weak signal beats blocking ordinary business
// text.
var categories = []category{
	{"system_override", regexp.MustCompile(`(?i)(ignore\s+(previous|all)\s+instructions|disregard\s+your\s+instructions|forget\s+your\s+instructions|override\s+system\s+prompt)`)},
	{"role_manipulation", regexp.MustCompile(`(?i)(act\s+as\s+if\s+you\s+are|pretend\s+you\s+are|roleplay\s+as|assume\s+the\s+role\s+of\s+(?:the\s+)?(?:system|assistant|admin|administrator)\b)`)},
	{"instruction_leak", regexp.MustCompile(`(?i)(show\s+me\s+your\s+system\s+prompt|reveal\s+your\s+instructions|what\s+are\s+your\s+instructions|print\s+your\s+system\s+message|output\s+your\s+prompt)`)},
	{"delimiter_attack", regexp.MustCompile("(?i)(" + regexp.QuoteMeta("```system") + "|" + regexp.QuoteMeta("###SYSTEM") + "|" + regexp.QuoteMeta("[SYSTEM]") + "|" + regexp.QuoteMeta("<|system|>") + ")")},
}

// PromptShield detects prompt-injection attempts in a request and applies the
// configured action: block, warn or log. See the package doc for the action
// semantics and the limits of the detection.
type PromptShield struct {
	enabled []category
	action  string
}

// Name returns the plugin identifier.
func (s *PromptShield) Name() string { return "prompt-shield" }

// Type returns the plugin lifecycle hook type.
func (s *PromptShield) Type() plugin.PluginType { return plugin.TypeGuardrail }

// SupportedStages reports that this plugin screens the request only. An
// injection attempt is something a caller sends: by after_request the model has
// already acted on it, so an entry there would enforce nothing and is refused
// at load instead.
func (s *PromptShield) SupportedStages() []plugin.Stage {
	return []plugin.Stage{plugin.StageBeforeRequest}
}

// actions are the actions this plugin can honour, read by Init and by
// ValidateConfig so the two cannot disagree about the set.
var actions = []string{plugin.ActionBlock, plugin.ActionWarn, plugin.ActionLog}

// ValidateConfig checks the action without building anything, so `ferrogw
// validate` and `ferrogw doctor` reject a misspelled one rather than leaving it
// to the startup or the config reload that follows. See plugin.ConfigValidator,
// and plugin.ValidateAction for why a ${VAR} reference is passed. The
// categories list is checked at Init, where every value is resolved.
func (s *PromptShield) ValidateConfig(config map[string]any) error {
	if err := plugin.ValidateAction(config["action"], plugin.ActionBlock, actions...); err != nil {
		return fmt.Errorf("prompt-shield: %w", err)
	}
	return nil
}

// Init selects the enabled categories and the action.
func (s *PromptShield) Init(config map[string]any) error {
	rawAction, err := plugin.StringSetting(config["action"], "action")
	if err != nil {
		return fmt.Errorf("prompt-shield: %w", err)
	}
	action, err := plugin.NormalizeAction(rawAction, plugin.ActionBlock, actions...)
	if err != nil {
		return fmt.Errorf("prompt-shield: action: %w", err)
	}
	s.action = action

	// An ABSENT key selects every category. A key that is present says something
	// about the selection, so a value that cannot express one — a scalar, a
	// mapping — is a load error rather than a silent widening: an operator who
	// asked for one category and got four is enforcing patterns they never
	// opted into.
	raw, present := config["categories"]
	if !present {
		s.enabled = make([]category, len(categories))
		copy(s.enabled, categories)
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("prompt-shield: categories must be a list of category names")
	}
	if len(list) == 0 {
		return fmt.Errorf("prompt-shield: categories is empty: omit the key to select every category, or name at least one")
	}

	known := make([]string, len(categories))
	for i, c := range categories {
		known[i] = c.name
	}

	wanted := make(map[string]bool, len(list))
	for i, v := range list {
		str, ok := v.(string)
		if !ok {
			return fmt.Errorf("prompt-shield: categories[%d] must be a string", i)
		}
		name := strings.ToLower(strings.TrimSpace(str))
		// A name matching nothing enables nothing, which registers a plugin the
		// catalog reports as enabled and that lets every injection through.
		if !slices.Contains(known, name) {
			return fmt.Errorf("prompt-shield: unrecognized category %q: must be one of %q", str, known)
		}
		wanted[name] = true
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
		// The caller has gone: stop screening rather than walk the rest of a
		// prompt nobody is waiting for. Returning nil and not the context's
		// error is the whole point — an error from Execute means the plugin
		// broke, which the gateway answers 500 and the target's circuit breaker
		// counts as a fault. A caller hanging up is neither.
		if ctx.Err() != nil {
			return nil
		}
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
