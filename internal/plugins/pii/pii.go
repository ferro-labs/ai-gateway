// Package pii provides a guardrail plugin for PII detection and redaction.
package pii

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ferro-labs/ai-gateway/internal/plugins/guardrailutil"
	"github.com/ferro-labs/ai-gateway/plugin"
)

func init() {
	plugin.RegisterFactory("pii-redact", func() plugin.Plugin {
		return &Plugin{}
	})
}

type entityDef struct {
	name     string
	pattern  string
	validate func(string) bool
}

var builtinEntities = []entityDef{
	{
		name:    "email",
		pattern: `[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`,
	},
	{
		name:    "phone",
		pattern: `(\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}`,
	},
	{
		name:     "credit_card",
		pattern:  `\b(?:4[0-9]{12}(?:[0-9]{3})?|5[1-5][0-9]{14}|3[47][0-9]{13}|3(?:0[0-5]|[68][0-9])[0-9]{11}|6(?:011|5[0-9]{2})[0-9]{12})\b`,
		validate: luhnCheck,
	},
	{
		name:     "ssn",
		pattern:  `\b\d{3}-\d{2}-\d{4}\b`,
		validate: ssnCheck,
	},
	{
		name:    "ip_address",
		pattern: `\b(?:(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\.){3}(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\b`,
	},
	{
		name:    "aws_access_key",
		pattern: `\bAKIA[0-9A-Z]{16}\b`,
	},
	{
		name:    "iban",
		pattern: `\b[A-Z]{2}\d{2}[A-Z0-9]{4}\d{7}(?:[A-Z0-9]?){0,16}\b`,
	},
	{
		name:    "passport",
		pattern: `\b[A-Z][0-9]{8}\b`,
	},
}

type redactMode string

const (
	redactMask        redactMode = "mask"
	redactReplaceType redactMode = "replace_type"
	redactHash        redactMode = "hash"
	redactSynthetic   redactMode = "synthetic"
)

type compiledEntity struct {
	name     string
	compiled *regexp.Regexp
	validate func(string) bool
	action   string
}

// Plugin detects and blocks or redacts PII in requests/responses.
type Plugin struct {
	action     string
	redactMode redactMode
	applyTo    string
	entities   []compiledEntity
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "pii-redact" }

// Type returns the plugin lifecycle type.
func (p *Plugin) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init configures the plugin.
func (p *Plugin) Init(config map[string]interface{}) error {
	p.action = guardrailutil.ParseAction(config, "action", "redact")
	p.applyTo = guardrailutil.ParseApplyTo(config)
	p.redactMode = parseRedactMode(config)

	enabledSet, err := parseEnabledEntitySet(config)
	if err != nil {
		return err
	}

	overrides, err := parseEntityOverrides(config, p.action)
	if err != nil {
		return err
	}

	p.entities = nil
	for _, definition := range builtinEntities {
		if len(enabledSet) > 0 {
			if _, ok := enabledSet[definition.name]; !ok {
				continue
			}
		}

		compiled, err := regexp.Compile(definition.pattern)
		if err != nil {
			return fmt.Errorf("compile entity pattern %s: %w", definition.name, err)
		}

		action := p.action
		if overrideAction, ok := overrides[definition.name]; ok {
			action = overrideAction
		}

		p.entities = append(p.entities, compiledEntity{
			name:     definition.name,
			compiled: compiled,
			validate: definition.validate,
			action:   action,
		})
	}

	return nil
}

// Execute evaluates request/response content and applies the configured action.
func (p *Plugin) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx == nil {
		return nil
	}
	if pctx.Metadata == nil {
		pctx.Metadata = make(map[string]interface{})
	}

	isAfterRequest := pctx.Response != nil
	if isAfterRequest && p.applyTo == "input" {
		return nil
	}
	if !isAfterRequest && p.applyTo == "output" {
		return nil
	}

	typeActions := make(map[string]string)

	if !isAfterRequest {
		if pctx.Request == nil {
			return nil
		}
		for _, message := range pctx.Request.Messages {
			p.collectTypeActions(message.Content, typeActions)
		}
		if len(typeActions) == 0 {
			return nil
		}

		blockedTypes := blockedTypes(typeActions)
		if len(blockedTypes) > 0 {
			pctx.Reject = true
			pctx.Reason = "PII detected: " + strings.Join(blockedTypes, ", ")
			return nil
		}

		if hasAction(typeActions, "redact") {
			redacted := false
			for i := range pctx.Request.Messages {
				updated, changed := p.redactContent(pctx.Request.Messages[i].Content, pctx)
				if changed {
					pctx.Request.Messages[i].Content = updated
					redacted = true
				}
			}
			if redacted {
				pctx.Metadata["pii_redacted"] = true
			}
		}

		mergePIITypesMetadata(pctx, mapKeys(typeActions))
		return nil
	}

	for _, choice := range pctx.Response.Choices {
		p.collectTypeActions(choice.Message.Content, typeActions)
	}
	if len(typeActions) == 0 {
		return nil
	}

	blockedTypes := blockedTypes(typeActions)
	if len(blockedTypes) > 0 {
		pctx.Reject = true
		pctx.Reason = "PII detected: " + strings.Join(blockedTypes, ", ")
		return nil
	}

	if hasAction(typeActions, "redact") {
		redacted := false
		for i := range pctx.Response.Choices {
			updated, changed := p.redactContent(pctx.Response.Choices[i].Message.Content, pctx)
			if changed {
				pctx.Response.Choices[i].Message.Content = updated
				redacted = true
			}
		}
		if redacted {
			pctx.Metadata["pii_redacted"] = true
		}
	}

	mergePIITypesMetadata(pctx, mapKeys(typeActions))
	return nil
}

func (p *Plugin) collectTypeActions(content string, out map[string]string) {
	for _, entity := range p.entities {
		if entityMatches(entity, content) {
			out[entity.name] = entity.action
		}
	}
}

func (p *Plugin) redactContent(content string, pctx *plugin.Context) (string, bool) {
	updated := content
	changed := false

	for _, entity := range p.entities {
		if entity.action != "redact" {
			continue
		}
		updated = entity.compiled.ReplaceAllStringFunc(updated, func(match string) string {
			if entity.validate != nil && !entity.validate(match) {
				return match
			}
			changed = true
			return p.replacement(entity.name, match, pctx)
		})
	}

	return updated, changed
}

func (p *Plugin) replacement(entityName, match string, pctx *plugin.Context) string {
	upperType := strings.ToUpper(entityName)

	switch p.redactMode {
	case redactMask:
		return "***"
	case redactHash:
		sum := sha256.Sum256([]byte(match))
		return fmt.Sprintf("[%s:%s]", upperType, hex.EncodeToString(sum[:3]))
	case redactSynthetic:
		ordinal := nextSyntheticOrdinal(pctx)
		return syntheticReplacement(entityName, ordinal)
	case redactReplaceType:
		fallthrough
	default:
		return fmt.Sprintf("[%s]", upperType)
	}
}

func parseEnabledEntitySet(config map[string]interface{}) (map[string]struct{}, error) {
	entities := guardrailutil.ParseStringList(config, "entities")
	if len(entities) == 0 {
		return nil, nil
	}

	known := builtinEntitySet()
	set := make(map[string]struct{}, len(entities))
	for _, entity := range entities {
		name := strings.TrimSpace(entity)
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("unknown PII entity: %s", name)
		}
		set[name] = struct{}{}
	}

	return set, nil
}

func parseEntityOverrides(config map[string]interface{}, defaultAction string) (map[string]string, error) {
	raw, ok := config["entity_overrides"]
	if !ok {
		return nil, nil
	}

	overridesMap, ok := raw.(map[string]interface{})
	if !ok {
		return nil, nil
	}

	known := builtinEntitySet()
	overrides := make(map[string]string, len(overridesMap))
	for name, value := range overridesMap {
		if _, exists := known[name]; !exists {
			return nil, fmt.Errorf("unknown PII entity override: %s", name)
		}
		entry, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		overrides[name] = guardrailutil.ParseAction(entry, "action", defaultAction)
	}

	return overrides, nil
}

func parseRedactMode(config map[string]interface{}) redactMode {
	if config == nil {
		return redactReplaceType
	}
	raw, ok := config["redact_mode"]
	if !ok {
		return redactReplaceType
	}
	text, ok := raw.(string)
	if !ok {
		return redactReplaceType
	}

	switch redactMode(strings.ToLower(strings.TrimSpace(text))) {
	case redactMask, redactReplaceType, redactHash, redactSynthetic:
		return redactMode(strings.ToLower(strings.TrimSpace(text)))
	default:
		return redactReplaceType
	}
}

func entityMatches(entity compiledEntity, content string) bool {
	if entity.validate == nil {
		return entity.compiled.MatchString(content)
	}
	matches := entity.compiled.FindAllString(content, -1)
	for _, match := range matches {
		if entity.validate(match) {
			return true
		}
	}
	return false
}

func blockedTypes(typeActions map[string]string) []string {
	blocked := make([]string, 0, len(typeActions))
	for typ, action := range typeActions {
		if action == "block" {
			blocked = append(blocked, typ)
		}
	}
	sort.Strings(blocked)
	return blocked
}

func hasAction(typeActions map[string]string, action string) bool {
	for _, configured := range typeActions {
		if configured == action {
			return true
		}
	}
	return false
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mergePIITypesMetadata(pctx *plugin.Context, newTypes []string) {
	existingSet := make(map[string]struct{}, len(newTypes))
	for _, typ := range newTypes {
		existingSet[typ] = struct{}{}
	}

	if raw, ok := pctx.Metadata["pii_types"]; ok {
		switch values := raw.(type) {
		case []string:
			for _, typ := range values {
				existingSet[typ] = struct{}{}
			}
		case []interface{}:
			for _, typ := range values {
				text, ok := typ.(string)
				if ok {
					existingSet[text] = struct{}{}
				}
			}
		}
	}

	merged := make([]string, 0, len(existingSet))
	for typ := range existingSet {
		merged = append(merged, typ)
	}
	sort.Strings(merged)
	pctx.Metadata["pii_types"] = merged
}

func nextSyntheticOrdinal(pctx *plugin.Context) int {
	current := 0
	switch value := pctx.Metadata["pii_synthetic_counter"].(type) {
	case int:
		current = value
	case float64:
		current = int(value)
	}
	current++
	pctx.Metadata["pii_synthetic_counter"] = current
	return current
}

func syntheticReplacement(entityName string, n int) string {
	switch entityName {
	case "email":
		return fmt.Sprintf("user%d@example.com", n)
	case "phone":
		return fmt.Sprintf("(555) 000-%04d", n%10000)
	case "ssn":
		return fmt.Sprintf("000-00-%04d", n%10000)
	case "credit_card":
		return syntheticCreditCard(n)
	case "ip_address":
		octet := (n % 254) + 1
		return fmt.Sprintf("192.0.2.%d", octet)
	default:
		return fmt.Sprintf("[REDACTED-%s]", strings.ToUpper(entityName))
	}
}

func syntheticCreditCard(n int) string {
	candidate := fmt.Sprintf("4111111111111%03d", n%1000)
	if luhnCheck(candidate) {
		return candidate
	}
	prefix := candidate[:15]
	checkDigit := luhnCheckDigit(prefix)
	return prefix + strconv.Itoa(checkDigit)
}

func luhnCheck(value string) bool {
	digits := normalizeDigits(value)
	if len(digits) < 12 {
		return false
	}

	sum := 0
	shouldDouble := false
	for i := len(digits) - 1; i >= 0; i-- {
		digit := int(digits[i] - '0')
		if shouldDouble {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		shouldDouble = !shouldDouble
	}

	return sum%10 == 0
}

func luhnCheckDigit(prefix string) int {
	digits := normalizeDigits(prefix)
	if len(digits) == 0 {
		return 0
	}

	sum := 0
	shouldDouble := true
	for i := len(digits) - 1; i >= 0; i-- {
		digit := int(digits[i] - '0')
		if shouldDouble {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		shouldDouble = !shouldDouble
	}

	return (10 - (sum % 10)) % 10
}

func ssnCheck(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) != 3 {
		return false
	}

	area := parts[0]
	group := parts[1]
	serial := parts[2]

	if len(area) != 3 || len(group) != 2 || len(serial) != 4 {
		return false
	}
	if area == "000" || area == "666" || area[0] == '9' {
		return false
	}
	if group == "00" || serial == "0000" {
		return false
	}

	return true
}

func normalizeDigits(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
			continue
		}
		if r == ' ' || r == '-' {
			continue
		}
		return ""
	}
	return builder.String()
}

func builtinEntitySet() map[string]struct{} {
	set := make(map[string]struct{}, len(builtinEntities))
	for _, entity := range builtinEntities {
		set[entity.name] = struct{}{}
	}
	return set
}
