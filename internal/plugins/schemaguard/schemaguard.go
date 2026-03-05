// Package schemaguard provides a guardrail plugin for JSON schema validation.
package schemaguard

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

const (
	schemaGuardApplyToInput  = "input"
	schemaGuardApplyToOutput = "output"
	schemaGuardApplyToBoth   = "both"
	schemaGuardActionBlock   = "block"
	schemaGuardActionWarn    = "warn"
	schemaGuardActionLog     = "log"
)

func init() {
	plugin.RegisterFactory("schema-guard", func() plugin.Plugin {
		return &SchemaGuard{}
	})
}

// SchemaGuard validates request/response JSON payloads against a JSON schema.
type SchemaGuard struct {
	applyTo     string
	action      string
	extractJSON bool
	schema      *jsonschema.Schema
}

// Name returns the plugin identifier.
func (s *SchemaGuard) Name() string { return "schema-guard" }

// Type returns the plugin lifecycle type.
func (s *SchemaGuard) Type() plugin.PluginType { return plugin.TypeGuardrail }

// Init configures the plugin.
func (s *SchemaGuard) Init(config map[string]interface{}) error {
	s.applyTo = parseApplyTo(config)
	s.action = parseAction(config)
	s.extractJSON = true
	if raw, ok := config["extract_json"].(bool); ok {
		s.extractJSON = raw
	}

	rawSchema, ok := config["schema"]
	if !ok {
		return fmt.Errorf("schema is required")
	}

	schemaBytes, err := json.Marshal(rawSchema)
	if err != nil {
		return fmt.Errorf("marshal schema: %w", err)
	}

	compiled, err := jsonschema.CompileString("inline.json", string(schemaBytes))
	if err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}

	s.schema = compiled
	return nil
}

// Execute validates request/response message content according to apply_to.
func (s *SchemaGuard) Execute(_ context.Context, pctx *plugin.Context) error {
	if pctx == nil || s.schema == nil {
		return nil
	}
	if pctx.Metadata == nil {
		pctx.Metadata = make(map[string]interface{})
	}

	isAfterRequest := pctx.Response != nil
	if isAfterRequest {
		if s.applyTo == schemaGuardApplyToInput {
			return nil
		}
		for _, choice := range pctx.Response.Choices {
			content := choice.Message.Content
			if s.extractJSON {
				content = extractJSON(content)
			}
			if err := s.validateJSON(content); err != nil {
				message := "output failed schema validation: " + err.Error()
				s.handleViolation(pctx, message)
				if s.action == schemaGuardActionBlock {
					return nil
				}
			}
		}
		return nil
	}

	if pctx.Request == nil || s.applyTo == schemaGuardApplyToOutput {
		return nil
	}

	for _, message := range pctx.Request.Messages {
		content := message.Content
		if s.extractJSON {
			content = extractJSON(content)
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}

		if err := s.validateJSON(content); err != nil {
			message := "input failed schema validation: " + err.Error()
			s.handleViolation(pctx, message)
			if s.action == schemaGuardActionBlock {
				return nil
			}
		}
	}

	return nil
}

func (s *SchemaGuard) validateJSON(content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("empty content")
	}

	var document interface{}
	if err := json.Unmarshal([]byte(content), &document); err != nil {
		return err
	}
	return s.schema.Validate(document)
}

func (s *SchemaGuard) handleViolation(pctx *plugin.Context, message string) {
	pctx.Metadata["schema_violation"] = message
	if s.action == schemaGuardActionBlock {
		pctx.Reject = true
		pctx.Reason = message
	}
}

func parseApplyTo(config map[string]interface{}) string {
	if config == nil {
		return schemaGuardApplyToOutput
	}
	raw, ok := config["apply_to"].(string)
	if !ok {
		return schemaGuardApplyToOutput
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case schemaGuardApplyToInput, schemaGuardApplyToOutput, schemaGuardApplyToBoth:
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return schemaGuardApplyToOutput
	}
}

func parseAction(config map[string]interface{}) string {
	if config == nil {
		return schemaGuardActionBlock
	}
	raw, ok := config["action"].(string)
	if !ok {
		return schemaGuardActionBlock
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case schemaGuardActionBlock, schemaGuardActionWarn, schemaGuardActionLog:
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return schemaGuardActionBlock
	}
}

var codeFenceRE = regexp.MustCompile("(?s)```(?:json)?\\n?(.*?)\\n?```")

// extractJSON strips markdown code fences and returns raw JSON content.
func extractJSON(s string) string {
	m := codeFenceRE.FindStringSubmatch(s)
	if len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return s
}
