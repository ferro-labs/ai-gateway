// Package guardrailutil provides shared helpers for guardrail plugins.
package guardrailutil

import (
	"strings"

	"github.com/ferro-labs/ai-gateway/providers"
)

// ExtractMessageContents returns all message content strings from messages,
// optionally filtered to specific roles.
func ExtractMessageContents(messages []providers.Message, roles ...string) []string {
	if len(messages) == 0 {
		return nil
	}

	roleSet := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		trimmed := strings.TrimSpace(role)
		if trimmed == "" {
			continue
		}
		roleSet[strings.ToLower(trimmed)] = struct{}{}
	}

	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		if len(roleSet) > 0 {
			if _, ok := roleSet[strings.ToLower(message.Role)]; !ok {
				continue
			}
		}
		contents = append(contents, message.Content)
	}

	return contents
}

// FlattenMessages returns all message content joined by newlines.
func FlattenMessages(messages []providers.Message, roles ...string) string {
	return strings.Join(ExtractMessageContents(messages, roles...), "\n")
}

// ParseAction parses an action value from config with a default fallback.
// Valid actions: block, redact, warn, log.
func ParseAction(config map[string]interface{}, key, defaultVal string) string {
	action := normalizeAction(defaultVal)
	if action == "" {
		action = "block"
	}
	if config == nil {
		return action
	}

	raw, ok := config[key]
	if !ok {
		return action
	}
	text, ok := raw.(string)
	if !ok {
		return action
	}
	parsed := normalizeAction(text)
	if parsed == "" {
		return action
	}
	return parsed
}

// ParseApplyTo parses an apply_to value from config.
// Valid values: input, output, both.
func ParseApplyTo(config map[string]interface{}) string {
	applyTo := "input"
	if config == nil {
		return applyTo
	}

	raw, ok := config["apply_to"]
	if !ok {
		return applyTo
	}
	text, ok := raw.(string)
	if !ok {
		return applyTo
	}
	parsed := strings.ToLower(strings.TrimSpace(text))
	switch parsed {
	case "input", "output", "both":
		return parsed
	default:
		return applyTo
	}
}

// ParseStringList parses a config key as []string, handling both []interface{}
// and []string input forms.
func ParseStringList(config map[string]interface{}, key string) []string {
	if config == nil {
		return nil
	}
	raw, ok := config[key]
	if !ok {
		return nil
	}

	var result []string
	switch list := raw.(type) {
	case []string:
		for _, item := range list {
			trimmed := strings.TrimSpace(item)
			if trimmed != "" {
				result = append(result, trimmed)
			}
		}
	case []interface{}:
		for _, item := range list {
			text, ok := item.(string)
			if !ok {
				continue
			}
			trimmed := strings.TrimSpace(text)
			if trimmed != "" {
				result = append(result, trimmed)
			}
		}
	}

	return result
}

// IntFromConfig reads an int config value handling float64 (JSON) and int (YAML).
func IntFromConfig(config map[string]interface{}, key string, defaultVal int) int {
	if config == nil {
		return defaultVal
	}
	raw, ok := config[key]
	if !ok {
		return defaultVal
	}

	switch value := raw.(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return defaultVal
	}
}

func normalizeAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "block", "redact", "warn", "log":
		return strings.ToLower(strings.TrimSpace(action))
	default:
		return ""
	}
}
