package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func f64(f float64) *float64 { return &f }
func i64(i int64) *int64     { return &i }
func intp(i int) *int        { return &i }

// TestComplete_MapsSupportedParamsToGenerationConfig verifies #140 native wiring
// for Gemini: OpenAI sampling params land under generationConfig with Gemini
// field names (topP, candidateCount, seed, stopSequences, penalties), and
// response_format JSON mode maps to responseMimeType.
func TestComplete_MapsSupportedParamsToGenerationConfig(t *testing.T) {
	var captured struct {
		GenerationConfig map[string]json.RawMessage `json:"generationConfig"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _ = p.Complete(context.Background(), core.Request{
		Model:            "gemini-1.5-pro",
		Messages:         []core.Message{{Role: "user", Content: "hi"}},
		Temperature:      f64(0.7),
		TopP:             f64(0.9),
		N:                intp(2),
		Seed:             i64(42),
		MaxTokens:        intp(64),
		PresencePenalty:  f64(0.5),
		FrequencyPenalty: f64(0.25),
		Stop:             []string{"END"},
		ResponseFormat:   &core.ResponseFormat{Type: "json_object"},
		LogitBias:        map[string]float64{"1": -1}, // unsupported → dropped
	})

	gc := captured.GenerationConfig
	if gc == nil {
		t.Fatalf("generationConfig missing from request body")
	}
	for _, k := range []string{
		"temperature", "topP", "candidateCount", "seed", "maxOutputTokens",
		"presencePenalty", "frequencyPenalty", "stopSequences", "responseMimeType",
	} {
		if _, ok := gc[k]; !ok {
			t.Errorf("expected generationConfig.%s to be set", k)
		}
	}
	// json_object carries no schema, so responseSchema must stay off the wire.
	if raw, ok := gc["responseSchema"]; ok {
		t.Errorf("json_object set generationConfig.responseSchema = %s, want absent", raw)
	}
}

// TestComplete_ForwardsJSONSchemaAsResponseSchema pins that a json_schema
// response_format reaches Gemini as generationConfig.responseSchema. Forwarding
// only responseMimeType hands the caller free-form JSON that ignores the schema
// they asked to be enforced, with no warning and no rejection.
func TestComplete_ForwardsJSONSchemaAsResponseSchema(t *testing.T) {
	var captured struct {
		GenerationConfig map[string]json.RawMessage `json:"generationConfig"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"{}"}],"role":"model"},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()

	p, err := New("test-key", srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The OpenAI wire shape: the schema sits under json_schema.schema, beside
	// name/strict, and carries additionalProperties as strict mode requires.
	_, _ = p.Complete(context.Background(), core.Request{
		Model:    "gemini-1.5-pro",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		ResponseFormat: &core.ResponseFormat{
			Type: "json_schema",
			JSONSchema: json.RawMessage(`{
				"name": "weather",
				"strict": true,
				"schema": {
					"type": "object",
					"additionalProperties": false,
					"required": ["city"],
					"properties": {
						"city": {"type": "string"},
						"temp_c": {"type": "number"}
					}
				}
			}`),
		},
	})

	gc := captured.GenerationConfig
	if gc == nil {
		t.Fatalf("generationConfig missing from request body")
	}
	if got := string(gc["responseMimeType"]); got != `"application/json"` {
		t.Errorf("responseMimeType = %s, want \"application/json\"", got)
	}

	raw, ok := gc["responseSchema"]
	if !ok {
		t.Fatalf("generationConfig.responseSchema absent: the schema never reached the wire, "+
			"so Gemini enforces nothing. generationConfig = %v", gc)
	}

	// The schema itself must arrive, unwrapped (no name/strict) and sanitized
	// into Gemini's OpenAPI subset (no additionalProperties).
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("responseSchema is not a JSON object: %s", raw)
	}
	for _, k := range []string{"name", "strict", "additionalProperties"} {
		if _, present := got[k]; present {
			t.Errorf("responseSchema carries %q; want it stripped: %s", k, raw)
		}
	}
	if got["type"] != "object" {
		t.Errorf("responseSchema.type = %v, want object: %s", got["type"], raw)
	}
	props, _ := got["properties"].(map[string]any)
	city, _ := props["city"].(map[string]any)
	if city["type"] != "string" {
		t.Errorf("responseSchema.properties.city.type = %v, want string: %s", city["type"], raw)
	}
	req, _ := got["required"].([]any)
	if len(req) != 1 || req[0] != "city" {
		t.Errorf("responseSchema.required = %v, want [city]: %s", got["required"], raw)
	}
}

// TestBuildRequest_JSONSchemaWithoutUsableSchema keeps a malformed json_schema
// wrapper in plain JSON mode rather than sending Gemini a body it would reject.
func TestBuildRequest_JSONSchemaWithoutUsableSchema(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wrapper string
	}{
		{"absent", `{"name":"x","strict":true}`},
		{"null", `{"name":"x","schema":null}`},
		{"not an object", `"just a string"`},
		{"empty", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := buildRequest(core.Request{
				Messages: []core.Message{{Role: "user", Content: "hi"}},
				ResponseFormat: &core.ResponseFormat{
					Type:       "json_schema",
					JSONSchema: json.RawMessage(tc.wrapper),
				},
			})
			if r.GenerationConfig.ResponseMimeType != "application/json" {
				t.Errorf("responseMimeType = %q, want application/json",
					r.GenerationConfig.ResponseMimeType)
			}
			if got := r.GenerationConfig.ResponseSchema; len(got) > 0 {
				t.Errorf("responseSchema = %s, want nothing forwarded", got)
			}
		})
	}
}
