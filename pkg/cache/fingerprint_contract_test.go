package cache_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ferro-labs/ai-gateway/pkg/cache"
	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// populatedFingerprintRequest exercises every Request field, including fields
// deliberately excluded from completion identity. Its values freeze the v1
// encoding of optional scalars, nested messages/tools, schemas and sorted maps.
func populatedFingerprintRequest() *providers.Request {
	temperature, topP, presence, frequency := 0.375, 0.875, -0.25, 0.5
	n, maxTokens, maxCompletion, topLogProbs, index := 2, 64, 96, 3, 1
	seed, parallel := int64(-42), true
	return &providers.Request{
		Model: "example-model",
		Messages: []providers.Message{{
			Role: "assistant", Name: "example-agent", Content: "answer|雪",
			ContentParts: []providers.ContentPart{
				{Type: "text", Text: "prompt|雪"},
				{Type: "image_url", ImageURL: &providers.ImageURLPart{URL: "https://example.com/image.png", Detail: "high"}},
			},
			ToolCalls:  []providers.ToolCall{{Index: &index, ID: "call-example", Type: "function", Function: providers.FunctionCall{Name: "lookup", Arguments: `{"city":"Paris"}`}}},
			ToolCallID: "call-parent", ReasoningContent: "example reasoning",
		}},
		Temperature: &temperature, TopP: &topP, N: &n, Seed: &seed,
		MaxTokens: &maxTokens, MaxCompletionTokens: &maxCompletion,
		PresencePenalty: &presence, FrequencyPenalty: &frequency,
		Stop: []string{"END", "雪|stop"},
		Tools: []providers.Tool{{Type: "function", Function: providers.Function{
			Name: "lookup", Description: "Look up a city",
			Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`), Strict: true,
		}}},
		ToolChoice:     map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}},
		ResponseFormat: &providers.ResponseFormat{Type: "json_schema", JSONSchema: json.RawMessage(`{"name":"answer","schema":{"type":"object"}}`)},
		LogProbs:       true, TopLogProbs: &topLogProbs, Stream: true,
		StreamOptions:       &core.StreamOptions{IncludeUsage: true},
		ClientStreamOptions: &core.StreamOptions{IncludeUsage: true},
		ParallelToolCalls:   &parallel, User: "example-user",
		LogitBias:       map[string]float64{"2": -1.5, "10": 0.25},
		RoutingMetadata: map[string]string{"route": "example-route"},
	}
}

func TestFingerprintPopulatedVector(t *testing.T) {
	const want = "v1:1c1fde3ff5da52085dc75ec85a17df86fc4cdbee3f7b5bf19453c3db11aec051"
	got, err := cache.Fingerprint(populatedFingerprintRequest(), "vector-scope")
	if err != nil || got != want {
		t.Fatalf("Fingerprint = %q, %v; want %q", got, err, want)
	}
}

func TestFingerprintRequestFieldCoverage(t *testing.T) {
	// An empty reason means hashed; a nonempty reason explicitly excludes it.
	// A new Request field must be classified here and populated above. For
	// hashed fields, removing its value must actually change the fingerprint.
	policy := map[string]string{
		"Model": "", "Messages": "", "Temperature": "", "TopP": "", "N": "", "Seed": "",
		"MaxTokens": "", "MaxCompletionTokens": "", "PresencePenalty": "", "FrequencyPenalty": "",
		"Stop": "", "Tools": "", "ToolChoice": "", "ResponseFormat": "", "LogProbs": "", "TopLogProbs": "",
		"Stream": "", "ParallelToolCalls": "", "User": "", "LogitBias": "",
		"StreamOptions":       "Provider usage-chunk framing is handled separately from completion content.",
		"ClientStreamOptions": "Client-facing usage-chunk suppression is handled separately from completion content.",
		"RoutingMetadata":     "Routing configuration must be distinguished by the caller's namespace.",
	}
	typ := reflect.TypeFor[providers.Request]()
	for name := range policy {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("policy names nonexistent Request field %s", name)
		}
	}
	base, err := cache.Fingerprint(populatedFingerprintRequest(), "vector-scope")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		t.Run(name, func(t *testing.T) {
			reason, classified := policy[name]
			if !classified {
				t.Fatalf("Request.%s must be hashed or explicitly excluded with a reason", name)
			}
			req := populatedFingerprintRequest()
			field := reflect.ValueOf(req).Elem().Field(i)
			if field.IsZero() {
				t.Fatalf("fixture must populate Request.%s", name)
			}
			field.SetZero()
			got, err := cache.Fingerprint(req, "vector-scope")
			if err != nil {
				t.Fatal(err)
			}
			if reason == "" && got == base {
				t.Errorf("Request.%s is classified as hashed but does not affect the key", name)
			}
			if reason != "" && got != base {
				t.Errorf("Request.%s affects the key despite exclusion: %s", name, reason)
			}
		})
	}
}
