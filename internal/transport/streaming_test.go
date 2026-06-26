package transport

import (
	"testing"
)

func TestIsStreamingRequest(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"stream true", `{"model":"gpt-4o","stream":true}`, true},
		{"stream true spaces", `{"model":"gpt-4o","stream": true}`, true},
		{"stream true newline", "{\"stream\":\n  true}", true},
		{"stream false", `{"model":"gpt-4o","stream":false}`, false},
		{"no stream field", `{"model":"gpt-4o"}`, false},
		{"empty body", `{}`, false},
		{"stream in value", `{"model":"stream-model"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsStreamingRequest([]byte(tc.body))
			if got != tc.want {
				t.Errorf("IsStreamingRequest(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func BenchmarkIsStreamingRequest(b *testing.B) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":100}`)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = IsStreamingRequest(body)
		}
	})
}
