// Package promptshield provides a guardrail plugin for prompt-injection signals.
package promptshield

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ferro-labs/ai-gateway/plugin"
)

const (
	promptShieldActionBlock         = "block"
	promptShieldActionWarn          = "warn"
	promptShieldActionLog           = "log"
	promptShieldApplyToUserMessages = "user_messages"
	promptShieldApplyToAll          = "all"
)

func init() {
	plugin.RegisterFactory("prompt-shield", func() plugin.Plugin {
		return &PromptShield{}
	})
}

type injectionSignal struct {
	name     string
	pattern  string
	score    float64
	category string
}

var defaultSignals = []injectionSignal{
	{name: "ignore_instructions", score: 0.95, category: "direct_override", pattern: `(?i)ignore\s+(all\s+)?(previous|prior|your|the)\s+(instructions?|context|prompt|rules?|constraints?)`},
	{name: "disregard_prompt", score: 0.92, category: "direct_override", pattern: `(?i)(disregard|forget|bypass|override)\s+(everything|all\s+previous|your\s+(system|initial|base))`},
	{name: "new_instructions", score: 0.88, category: "direct_override", pattern: `(?i)(your\s+new\s+instructions?|from\s+now\s+on)\s*[,:]?\s*(you\s+(are|will|must|should))`},
	{name: "pretend_no_limits", score: 0.90, category: "persona_injection", pattern: `(?i)pretend\s+(you\s+)?(have\s+no|don.?t\s+have|without\s+(any\s+)?)\s+(restrictions?|limits?|rules?|guidelines?)`},
	{name: "act_as_unrestricted", score: 0.82, category: "persona_injection", pattern: `(?i)act\s+as\s+(if\s+)?(you\s+(are|were|have)|a\s+[a-z]+\s+(without|that|who))`},
	{name: "you_are_now", score: 0.78, category: "persona_injection", pattern: `(?i)you\s+are\s+now\s+(a|an|the)\s+[a-z\s]+(without|that\s+can|who\s+can)`},
	{name: "dan_jailbreak", score: 0.95, category: "known_template", pattern: `(?i)\bDAN\b.{0,50}(jailbreak|mode|prompt|do\s+anything|now)`},
	{name: "developer_mode", score: 0.90, category: "known_template", pattern: `(?i)developer\s+mode\s+(enabled|activated|prompt|jailbreak)`},
	{name: "opposite_mode", score: 0.85, category: "known_template", pattern: `(?i)(opposite\s+mode|anti[-\s]?gpt|evil\s+(mode|ai|version))`},
	{name: "reveal_system_prompt", score: 0.85, category: "extraction", pattern: `(?i)(print|show|repeat|output|reveal|tell\s+me|what\s+is)\s+(your|the)\s+(system\s+prompt|instructions?|initial\s+prompt)`},
	{name: "repeat_above", score: 0.80, category: "extraction", pattern: `(?i)repeat\s+(everything|all\s+(the\s+)?(text|words?|content)\s+(above|before|prior))`},
	{name: "encoded_instruction", score: 0.88, category: "obfuscation", pattern: `(?i)(decode|base64|rot13)\s+(this|the\s+following)\s+and\s+(follow|execute|run|do)`},
	{name: "hidden_html_comment", score: 0.88, category: "indirect", pattern: `(?i)<!--.*?(ignore|instruction|system|override).*?-->`},
	{name: "fake_system_tag", score: 0.85, category: "indirect", pattern: `(?i)<\s*(SYSTEM|INST|INSTRUCTION)\s*>`},
}

type compiledSignal struct {
	name     string
	score    float64
	category string
	compiled *regexp.Regexp
}

// PromptShield scores prompt injection indicators and blocks/warns/logs.
type PromptShield struct {
	action       string
	threshold    float64
	applyTo      string
	includeScore bool
	signals      []compiledSignal
}

// Name returns the plugin identifier.
func (p *PromptShield) Name() string { return "prompt-shield" }

// Type returns the plugin lifecycle type.
func (p *PromptShield) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init configures the plugin.
func (p *PromptShield) Init(config map[string]interface{}) error {
	p.action = parseAction(config)
	p.threshold = parseThreshold(config)
	p.applyTo = parseApplyTo(config)
	p.includeScore = false
	if raw, ok := config["include_score"].(bool); ok {
		p.includeScore = raw
	}

	signals := make([]injectionSignal, 0, len(defaultSignals)+4)
	signals = append(signals, defaultSignals...)
	customSignals, err := parseCustomSignals(config)
	if err != nil {
		return err
	}
	signals = append(signals, customSignals...)

	p.signals = nil
	for _, signal := range signals {
		compiled, err := regexp.Compile(signal.pattern)
		if err != nil {
			return fmt.Errorf("compile injection signal %s: %w", signal.name, err)
		}
		p.signals = append(p.signals, compiledSignal{
			name:     signal.name,
			score:    signal.score,
			category: signal.category,
			compiled: compiled,
		})
	}

	return nil
}

// Execute scans request messages and applies configured policy on threshold hit.
func (p *PromptShield) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx == nil || pctx.Response != nil || pctx.Request == nil {
		return nil
	}
	if pctx.Metadata == nil {
		pctx.Metadata = make(map[string]interface{})
	}

	maxScore := 0.0
	signalName := ""

	for _, message := range pctx.Request.Messages {
		if p.applyTo == promptShieldApplyToUserMessages && strings.ToLower(message.Role) != "user" {
			continue
		}
		for _, signal := range p.signals {
			if signal.compiled.MatchString(message.Content) && signal.score > maxScore {
				maxScore = signal.score
				signalName = signal.name
			}
		}
	}

	pctx.Metadata["injection_score"] = maxScore
	if signalName != "" {
		pctx.Metadata["injection_signal"] = signalName
	}

	if signalName != "" && maxScore >= p.threshold {
		switch p.action {
		case promptShieldActionBlock:
			pctx.Reject = true
			if p.includeScore {
				pctx.Reason = fmt.Sprintf("prompt injection detected (score: %.2f)", maxScore)
			} else {
				pctx.Reason = "prompt injection detected"
			}
		case promptShieldActionWarn, promptShieldActionLog:
			// Metadata already set.
		}
	}

	return nil
}

func parseAction(config map[string]interface{}) string {
	if config == nil {
		return promptShieldActionBlock
	}
	raw, ok := config["action"].(string)
	if !ok {
		return promptShieldActionBlock
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case promptShieldActionBlock, promptShieldActionWarn, promptShieldActionLog:
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return promptShieldActionBlock
	}
}

func parseThreshold(config map[string]interface{}) float64 {
	threshold := 0.90
	if config == nil {
		return threshold
	}
	raw, ok := config["threshold"]
	if !ok {
		return threshold
	}
	switch value := raw.(type) {
	case float64:
		threshold = value
	case int:
		threshold = float64(value)
	}
	if threshold < 0 {
		return 0
	}
	if threshold > 1 {
		return 1
	}
	return threshold
}

func parseApplyTo(config map[string]interface{}) string {
	if config == nil {
		return promptShieldApplyToUserMessages
	}
	raw, ok := config["apply_to"].(string)
	if !ok {
		return promptShieldApplyToUserMessages
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case promptShieldApplyToUserMessages, promptShieldApplyToAll:
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return promptShieldApplyToUserMessages
	}
}

func parseCustomSignals(config map[string]interface{}) ([]injectionSignal, error) {
	raw, ok := config["custom_signals"]
	if !ok {
		return nil, nil
	}

	list, ok := raw.([]interface{})
	if !ok {
		return nil, nil
	}

	custom := make([]injectionSignal, 0, len(list))
	for _, entry := range list {
		mapped, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := mapped["name"].(string)
		pattern, _ := mapped["pattern"].(string)
		if strings.TrimSpace(name) == "" || strings.TrimSpace(pattern) == "" {
			continue
		}

		score := 0.0
		switch rawScore := mapped["score"].(type) {
		case float64:
			score = rawScore
		case int:
			score = float64(rawScore)
		}
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}

		category, _ := mapped["category"].(string)
		custom = append(custom, injectionSignal{
			name:     strings.TrimSpace(name),
			pattern:  strings.TrimSpace(pattern),
			score:    score,
			category: strings.TrimSpace(category),
		})
	}

	return custom, nil
}
