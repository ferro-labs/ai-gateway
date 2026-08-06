package providers

import (
	"encoding/json"
	"testing"
)

func BenchmarkMessageUnmarshalStringContent(b *testing.B) {
	payload := []byte(`{
		"role":"user",
		"content":"Summarize the latest deployment status.",
		"name":"alice",
		"metadata":{"tenant":"bench","request_id":"req-123"}
	}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var msg Message
		if err := json.Unmarshal(payload, &msg); err != nil {
			b.Fatal(err)
		}
	}
}
