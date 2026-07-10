package anthropicwire

import "testing"

// FuzzStreamDecoderEvent feeds arbitrary bytes to the Anthropic stream event
// decoder. Each event frame is untrusted upstream input; decoding any single
// frame must yield chunks or an error without panicking, regardless of the
// decoder's accumulated state.
func FuzzStreamDecoderEvent(f *testing.F) {
	f.Add([]byte(`{"type":"message_start","message":{"id":"m","model":"claude","usage":{"input_tokens":5}}}`))
	f.Add([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"fn"}}`))
	f.Add([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`))
	f.Add([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`))
	f.Add([]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`))
	f.Add([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"boom"}}`))
	f.Add([]byte(``))

	f.Fuzz(func(_ *testing.T, data []byte) {
		d := NewStreamDecoder("fuzz", "")
		_, _ = d.Event(data)
	})
}
