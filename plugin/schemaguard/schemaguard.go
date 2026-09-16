// Package schemaguard provides a schema-guard guardrail plugin that validates a
// model's response against a JSON Schema subset — "type", "required" and
// "properties". Register it with a blank import:
//
//	_ "github.com/ferro-labs/ai-gateway/plugin/schemaguard"
//
// # Scope
//
// A subset rather than a dependency: this module carries no JSON Schema
// library, and adding one to validate the three keywords that answer "did the
// model return the object shape my code is about to unmarshal" would be a large
// dependency for a small question. A keyword outside the subset is ignored, so
// a schema pasted in from elsewhere still validates the part this plugin
// understands; a keyword INSIDE it that is written wrong fails the load.
//
// It validates responses only. On a streamed response the tokens have already
// been delivered chunk by chunk, so a violation can be reported but not
// unsent — a caller that must withhold malformed output must not stream.
//
// Each choice is assembled into one document and validated once, and a choice
// carrying NEITHER content NOR a tool call is a violation. A choice that
// carries only a tool call passes without validation: a tool call is a
// different kind of answer, not a malformed one, so a model that chose to call
// a tool did not return a document this schema describes.
package schemaguard

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// typeInteger is the one JSON Schema type name no decoded value ever reports,
// because encoding/json has no integer type to report.
const typeInteger = "integer"

// schemaTypes are JSON Schema's seven fundamental types, which is the closed
// set a "type" keyword may name.
var schemaTypes = []string{"null", "boolean", "object", "array", "number", "string", typeInteger}

func init() {
	plugin.RegisterFactory("schema-guard", func() plugin.Plugin {
		return &SchemaGuard{}
	})
}

// SchemaGuard validates the model's response against a JSON Schema subset:
// "type", "required" and "properties". A keyword outside that subset is
// ignored, which is a contract rather than an omission — see the package doc
// before narrowing it.
type SchemaGuard struct {
	schema map[string]any
	action string
}

// Name returns the plugin identifier.
func (g *SchemaGuard) Name() string { return "schema-guard" }

// Type returns the plugin lifecycle hook type.
func (g *SchemaGuard) Type() plugin.PluginType { return plugin.TypeGuardrail }

// SupportedStages reports that this plugin validates responses only. There is
// nothing to validate before the provider has answered, so an entry at another
// stage would enforce nothing and is refused at load instead.
func (g *SchemaGuard) SupportedStages() []plugin.Stage {
	return []plugin.Stage{plugin.StageAfterRequest}
}

// Init stores the schema and the action.
func (g *SchemaGuard) Init(config map[string]any) error {
	rawAction, err := plugin.StringSetting(config["action"], "action")
	if err != nil {
		return fmt.Errorf("schema-guard: %w", err)
	}
	action, err := plugin.NormalizeAction(rawAction, plugin.ActionBlock, plugin.ActionBlock, plugin.ActionWarn, plugin.ActionLog)
	if err != nil {
		return fmt.Errorf("schema-guard: action: %w", err)
	}
	g.action = action

	schema, ok := config["schema"].(map[string]any)
	if !ok {
		return fmt.Errorf("schema-guard: schema is required and must be an object")
	}
	if err := validateSchema(schema, "schema"); err != nil {
		return fmt.Errorf("schema-guard: %w", err)
	}
	g.schema = schema
	return nil
}

// validateSchema checks the keywords this plugin enforces, recursively.
//
// A malformed SUPPORTED keyword is a load error. Ignoring it — which is what a
// validator that only type-asserts at match time does — leaves the keyword
// unenforced: `schema: {required: "name"}` is one keystroke from the list that
// was meant, reads identically in the config, and produced a plugin that
// started, reported itself enabled and approved every response.
//
// An UNSUPPORTED keyword is still ignored in silence. That is the documented
// contract and the reason this subset is usable at all: a schema pasted in from
// elsewhere carries "minimum", "pattern" and the rest, and rejecting those
// would refuse the configs this plugin exists to serve.
func validateSchema(schema map[string]any, path string) error {
	if raw, present := schema["type"]; present {
		name, ok := raw.(string)
		if !ok {
			return fmt.Errorf("%s: type must be a single type name, got %T", path, raw)
		}
		// A misspelled name is not an unsupported keyword: it is this keyword,
		// written so that no value can ever satisfy it, which under the default
		// action rejects every response.
		if !slices.Contains(schemaTypes, name) {
			return fmt.Errorf("%s: unrecognized type %q: must be one of %q", path, name, schemaTypes)
		}
	}

	if raw, present := schema["required"]; present {
		list, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s: required must be a list of field names, got %T", path, raw)
		}
		for i, entry := range list {
			if _, ok := entry.(string); !ok {
				return fmt.Errorf("%s: required[%d] must be a field name, got %T", path, i, entry)
			}
		}
	}

	raw, present := schema["properties"]
	if !present {
		return nil
	}
	properties, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: properties must map field names to schemas, got %T", path, raw)
	}
	for name, sub := range properties {
		mapped, ok := sub.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.%s must be a schema object, got %T", path, name, sub)
		}
		if err := validateSchema(mapped, path+"."+name); err != nil {
			return err
		}
	}
	return nil
}

// Execute validates each choice's content against the schema. It returns
// early for any stage other than after_request: this plugin validates
// responses only, and there is nothing to validate before the provider has
// answered.
func (g *SchemaGuard) Execute(ctx context.Context, pctx *plugin.Context) error {
	if g.schema == nil || pctx.Stage != plugin.StageAfterRequest || pctx.Response == nil {
		return nil
	}

	for _, choice := range pctx.Response.Choices {
		// The caller has gone: stop validating rather than parse the remaining
		// choices of a response nobody is waiting for. Returning nil and not the
		// context's error is the whole point — an error from Execute means the
		// plugin broke, which the gateway answers 500 and the target's circuit
		// breaker counts as a fault. A caller hanging up is neither.
		if ctx.Err() != nil {
			return nil
		}
		violation := g.validate(choice.Message)
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

// choiceDocument assembles the one document a choice carries.
//
// A schema is a statement about a DOCUMENT, which is why this plugin does its
// own traversal rather than screening each string plugin.ResponseText yields.
// That iterator is shaped for pattern matching, where every fragment is worth
// testing on its own; here the fragments of one object are each invalid JSON
// alone, so a conforming answer split across content parts was denied, and a
// decoded multipart message — which carries both the collapsed Content and the
// parts it was collapsed from — had the same document validated twice.
//
// Content is the collapsed form: Message.UnmarshalJSON joins every text part
// into it, so whenever it is set it is the whole document and the parts are the
// same bytes again. A message built in Go rather than decoded carries an empty
// Content, and there its parts are the only source.
func choiceDocument(msg providers.Message) string {
	if msg.Content != "" {
		return msg.Content
	}
	var assembled strings.Builder
	for _, part := range msg.ContentParts {
		assembled.WriteString(part.Text)
	}
	return assembled.String()
}

// validate returns a human-readable violation, or "" when the choice conforms.
//
// A choice carrying nothing is a violation rather than a skip: a response with
// no content in it does not satisfy a schema requiring an object, and reading
// the absence as conformance let every empty answer through a guardrail
// configured to require one.
//
// A choice carrying a TOOL CALL and no text is not that case. A tool call is a
// different kind of answer, not a malformed one: a model that chose to call a
// tool did not return a document this schema describes, and denying it would
// make the plugin incompatible with tool calling rather than protective of it.
// A choice with nothing in it at all is still a violation, because that is the
// case the rule exists for.
func (g *SchemaGuard) validate(msg providers.Message) string {
	text := choiceDocument(msg)
	if strings.TrimSpace(text) == "" {
		if len(msg.ToolCalls) > 0 {
			return ""
		}
		return "response carries no content"
	}
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
	if want, ok := schema["type"].(string); ok && !satisfiesType(doc, want) {
		return fmt.Sprintf("%s: expected %s, got %s", path, want, jsonType(doc))
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

// satisfiesType reports whether a decoded JSON value matches a schema type
// name.
//
// "integer" is a constraint on a number's VALUE rather than a type a decoded
// value could report: JSON carries one number type and encoding/json decodes
// all of it as float64, so 42 and 42.5 arrive as the same Go type. Comparing
// type names alone would reject every response under a schema declaring
// "integer" — a count, an age, an id — because nothing ever reports one.
func satisfiesType(v any, want string) bool {
	if want != typeInteger {
		return jsonType(v) == want
	}
	f, ok := v.(float64)
	return ok && f == math.Trunc(f)
}

// jsonType names a decoded JSON value's type in JSON Schema's vocabulary. Every
// number is reported as "number"; "integer" is a constraint on a number's
// value, applied by validateAgainst through isWholeNumber.
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
