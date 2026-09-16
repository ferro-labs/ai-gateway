// Package schemaguard provides a schema-guard guardrail plugin that validates a
// model's response against a JSON Schema subset. Register it with a blank
// import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/schemaguard"
package schemaguard

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
)

func init() {
	plugin.RegisterFactory("schema-guard", func() plugin.Plugin {
		return &SchemaGuard{}
	})
}

// SchemaGuard validates the model's response against a JSON Schema subset:
// "type", "required" and "properties".
//
// A subset rather than a dependency: this module carries no JSON Schema
// library, and adding one to validate the three keywords that answer "did the
// model return the object shape my code is about to unmarshal" would be a large
// dependency for a small question. An unsupported keyword is ignored rather
// than rejected, so a schema copied from elsewhere still validates what this
// plugin understands.
//
// It runs at after_request only. On a streamed response the tokens are
// already delivered to the caller, so this can report a violation but cannot
// unsend it — a caller that must withhold malformed output must not stream.
type SchemaGuard struct {
	schema map[string]any
	action string
}

// Name returns the plugin identifier.
func (g *SchemaGuard) Name() string { return "schema-guard" }

// Type returns the plugin lifecycle hook type.
func (g *SchemaGuard) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init stores the schema and the action.
func (g *SchemaGuard) Init(config map[string]any) error {
	rawAction, _ := config["action"].(string)
	action, err := plugin.NormalizeAction(rawAction, plugin.ActionBlock, plugin.ActionBlock, plugin.ActionWarn, plugin.ActionLog)
	if err != nil {
		return fmt.Errorf("schema-guard: action: %w", err)
	}
	g.action = action

	schema, ok := config["schema"].(map[string]any)
	if !ok {
		return fmt.Errorf("schema-guard: schema is required and must be an object")
	}
	g.schema = schema
	return nil
}

// Execute validates each choice's content against the schema. It returns
// early for any stage other than after_request: this plugin validates
// responses only, and there is nothing to validate before the provider has
// answered.
func (g *SchemaGuard) Execute(ctx context.Context, pctx *plugin.Context) error {
	if g.schema == nil || pctx.Stage != plugin.StageAfterRequest {
		return nil
	}

	for text := range plugin.ResponseText(pctx.Response) {
		if strings.TrimSpace(text) == "" {
			continue
		}
		violation := g.validate(text)
		if violation == "" {
			continue
		}
		logger.Ctx(ctx).Warn("schema-guard: response violates schema", "violation", violation)
		if g.action != plugin.ActionBlock {
			continue
		}
		pctx.Reject = true
		// A schema violation is not adversarial the way a prompt injection or a
		// leaked secret is, so naming the offending field is the whole
		// diagnostic value here rather than a hint an attacker could exploit.
		pctx.Reason = "response blocked by content policy: " + violation
		return nil
	}
	return nil
}

// Close releases resources owned by the plugin.
func (g *SchemaGuard) Close() error { return nil }

// validate returns a human-readable violation, or "" when the document
// conforms.
func (g *SchemaGuard) validate(text string) string {
	var doc any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return "response is not valid JSON"
	}
	return validateAgainst(doc, g.schema, "response")
}

// validateAgainst checks doc against schema's "type", "required" and
// "properties" keywords only. Any other keyword — "minimum", "pattern",
// "enum", and the rest of JSON Schema — is silently ignored: this is a
// structural subset, not a validator, chosen so a schema pasted in from
// elsewhere still validates the part this plugin understands rather than
// erroring on the part it does not.
func validateAgainst(doc any, schema map[string]any, path string) string {
	if want, ok := schema["type"].(string); ok {
		if got := jsonType(doc); got != want {
			return fmt.Sprintf("%s: expected %s, got %s", path, want, got)
		}
	}

	obj, isObject := doc.(map[string]any)
	if !isObject {
		return ""
	}

	if required, ok := schema["required"].([]any); ok {
		for _, r := range required {
			name, ok := r.(string)
			if !ok {
				continue
			}
			if _, present := obj[name]; !present {
				return fmt.Sprintf("%s: missing required field %q", path, name)
			}
		}
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return ""
	}
	for name, raw := range properties {
		sub, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		value, present := obj[name]
		if !present {
			continue
		}
		if violation := validateAgainst(value, sub, path+"."+name); violation != "" {
			return violation
		}
	}
	return ""
}

// jsonType names a decoded JSON value's type in JSON Schema's vocabulary.
// encoding/json decodes every number as float64, so "integer" is reported as
// "number" — distinguishing them would mean claiming a precision the decode
// already discarded.
func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}
