package openaicompat

import "testing"

// FuzzDecodeStreamChunk feeds arbitrary bytes to the OpenAI-compatible stream
// chunk decoder. Each SSE "data:" payload is untrusted; the decoder must return
// a value or an error for any input without panicking.
func FuzzDecodeStreamChunk(f *testing.F) {
	f.Add([]byte(`{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"}}]}`))
	f.Add([]byte(`{"choices":[{"index":0,"finish_reason":"stop"}]}`))
	f.Add([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = DecodeStreamChunk(data)
	})
}
