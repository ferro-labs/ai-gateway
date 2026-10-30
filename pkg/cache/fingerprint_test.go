package cache_test

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/pkg/cache"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestFingerprintVectors(t *testing.T) {
	for _, tt := range []struct {
		req             providers.Request
		namespace, want string
	}{
		{providers.Request{}, "", "v1:592d2fa4df0856ead0bb2b74a2d488fb5e0c7302ed30ba66615b3ec9429ee6f9"},
		{providers.Request{Model: "example-model", Messages: []providers.Message{{Role: "user", Content: "Hello"}}}, "application-a", "v1:b33810242bf6a17c724be254de4a375faba5c4ff51786e4e065ee64b66a7ed9e"},
	} {
		got, err := cache.Fingerprint(&tt.req, tt.namespace)
		if err != nil || got != tt.want {
			t.Fatalf("Fingerprint = %q, %v; want %q", got, err, tt.want)
		}
	}
}

func TestFingerprintCompletionFields(t *testing.T) {
	base := func() *providers.Request {
		return &providers.Request{Model: "example-model", Messages: []providers.Message{{Role: "system", Content: "Answer briefly."}, {Role: "user", Content: "Hello"}}}
	}
	zeroInt, zeroFloat, zeroInt64, no := 0, 0.0, int64(0), false
	changes := map[string]func(*providers.Request){
		"model":                 func(r *providers.Request) { r.Model = "other-model" },
		"system message":        func(r *providers.Request) { r.Messages[0].Content = "Answer in detail." },
		"role":                  func(r *providers.Request) { r.Messages[0].Role = "developer" },
		"name":                  func(r *providers.Request) { r.Messages[1].Name = "example" },
		"content":               func(r *providers.Request) { r.Messages[1].Content = "Goodbye" },
		"order":                 func(r *providers.Request) { r.Messages[0], r.Messages[1] = r.Messages[1], r.Messages[0] },
		"temperature":           func(r *providers.Request) { r.Temperature = &zeroFloat },
		"top_p":                 func(r *providers.Request) { r.TopP = &zeroFloat },
		"n":                     func(r *providers.Request) { r.N = &zeroInt },
		"seed":                  func(r *providers.Request) { r.Seed = &zeroInt64 },
		"max_tokens":            func(r *providers.Request) { r.MaxTokens = &zeroInt },
		"max_completion_tokens": func(r *providers.Request) { r.MaxCompletionTokens = &zeroInt },
		"presence_penalty":      func(r *providers.Request) { r.PresencePenalty = &zeroFloat },
		"frequency_penalty":     func(r *providers.Request) { r.FrequencyPenalty = &zeroFloat },
		"stop":                  func(r *providers.Request) { r.Stop = []string{"END"} },
		"tools": func(r *providers.Request) {
			r.Tools = []providers.Tool{{Type: "function", Function: providers.Function{Name: "example", Parameters: json.RawMessage(`{"type":"object"}`)}}}
		},
		"tool_choice":         func(r *providers.Request) { r.ToolChoice = "none" },
		"parallel_tool_calls": func(r *providers.Request) { r.ParallelToolCalls = &no },
		"response_format":     func(r *providers.Request) { r.ResponseFormat = &providers.ResponseFormat{Type: "json_object"} },
		"logprobs":            func(r *providers.Request) { r.LogProbs = true },
		"top_logprobs":        func(r *providers.Request) { r.TopLogProbs = &zeroInt },
		"stream":              func(r *providers.Request) { r.Stream = true },
		"user":                func(r *providers.Request) { r.User = "example" },
		"logit_bias":          func(r *providers.Request) { r.LogitBias = map[string]float64{"1": 1} },
	}
	key, err := cache.Fingerprint(base(), "")
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			req := base()
			change(req)
			got, err := cache.Fingerprint(req, "")
			if err != nil || got == key {
				t.Fatalf("changed request retained key or failed: %v", err)
			}
		})
	}
}

func TestFingerprintErrors(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	for name, req := range map[string]*providers.Request{
		"nan temperature":            {Temperature: &nan},
		"infinite top_p":             {TopP: &inf},
		"nan presence_penalty":       {PresencePenalty: &nan},
		"infinite frequency_penalty": {FrequencyPenalty: &inf},
		"nan logit_bias":             {LogitBias: map[string]float64{"1": nan}},
		"nil":                        nil,
		"tool choice":                {ToolChoice: make(chan int)},
		"tool schema":                {Tools: []providers.Tool{{Function: providers.Function{Parameters: json.RawMessage(`invalid`)}}}},
		"response schema":            {ResponseFormat: &providers.ResponseFormat{JSONSchema: json.RawMessage(`invalid`)}},
	} {
		t.Run(name, func(t *testing.T) {
			key, err := cache.Fingerprint(req, "opaque-example")
			if err == nil || key != "" {
				t.Fatalf("Fingerprint = %q, %v; want empty key and error", key, err)
			}
			if strings.Contains(err.Error(), "opaque-example") || strings.Contains(err.Error(), "invalid") {
				t.Fatal("error exposes input")
			}
		})
	}
}

func TestFingerprintNamespaceAndMapOrder(t *testing.T) {
	req := &providers.Request{
		Model:      "example-model",
		Messages:   []providers.Message{{Role: "system", Content: "Answer briefly."}, {Role: "user", Content: "Hello"}},
		ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "example"}},
		LogitBias:  map[string]float64{"2": 1, "1": -1},
	}
	before, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	keys := make(map[string]bool)
	for _, namespace := range []string{"", "a", "b", "a\x00b"} {
		key, err := cache.Fingerprint(req, namespace)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(key, "v1:") || len(key) != 67 || keys[key] {
			t.Fatalf("invalid or duplicate key: %q", key)
		}
		keys[key] = true
		copyReq := *req
		copyReq.LogitBias = map[string]float64{"1": -1, "2": 1}
		copyReq.ToolChoice = map[string]any{"function": map[string]any{"name": "example"}, "type": "function"}
		again, err := cache.Fingerprint(&copyReq, namespace)
		if err != nil || again != key {
			t.Fatalf("map order changed key: %q, %v", again, err)
		}
	}
	after, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("request mutated")
	}
}

func ExampleFingerprint() {
	req := &providers.Request{Model: "example-model", Messages: []providers.Message{{Role: "user", Content: "Hello"}}}
	key, err := cache.Fingerprint(req, "application-a")
	if err != nil {
		panic(err)
	}
	again, err := cache.Fingerprint(req, "application-a")
	if err != nil {
		panic(err)
	}
	fmt.Println(key == again)
	// Output: true
}
