package handler

import (
	"strings"
	"testing"
)

// TestDecodeChatCompletionRequest_StreamOptions verifies the three distinct
// client-visible behaviours for stream_options.include_usage: explicit
// false, explicit true, and omitted entirely. The decoded value lands on
// ClientStreamOptions, never on StreamOptions — see the field doc in
// providers/core/chat.go for why the two must stay separate.
func TestDecodeChatCompletionRequest_StreamOptions(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		wantNil        bool
		wantIncludeVal bool
	}{
		{
			name:    "omitted stream_options decodes to nil",
			body:    `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantNil: true,
		},
		{
			name:           "explicit include_usage:false decodes distinctly from omitted",
			body:           `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":false}}`,
			wantNil:        false,
			wantIncludeVal: false,
		},
		{
			name:           "explicit include_usage:true decodes",
			body:           `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`,
			wantNil:        false,
			wantIncludeVal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := DecodeChatCompletionRequest(strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			// The wire-forwarding field must never be populated from the
			// client's request — that is the field ~20 OpenAI-compatible
			// providers marshal verbatim upstream, and an explicit false
			// there would silently disable their usage reporting.
			if req.StreamOptions != nil {
				t.Errorf("StreamOptions = %+v, want nil (client intent must not reach the verbatim-forward field)", req.StreamOptions)
			}

			if tt.wantNil {
				if req.ClientStreamOptions != nil {
					t.Errorf("ClientStreamOptions = %+v, want nil", req.ClientStreamOptions)
				}
				return
			}
			if req.ClientStreamOptions == nil {
				t.Fatal("ClientStreamOptions is nil, want a decoded value")
			}
			if req.ClientStreamOptions.IncludeUsage != tt.wantIncludeVal {
				t.Errorf("IncludeUsage = %v, want %v", req.ClientStreamOptions.IncludeUsage, tt.wantIncludeVal)
			}
		})
	}
}

// TestDecodeChatCompletionRequest_StreamOptions_PoolReset verifies the
// sync.Pool-backed wire struct does not leak a previous request's
// stream_options into the next decode that omits it — the same
// cross-tenant-leak class of bug the reset() SECURITY comment already guards
// every other field against.
func TestDecodeChatCompletionRequest_StreamOptions_PoolReset(t *testing.T) {
	first, err := DecodeChatCompletionRequest(strings.NewReader(
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":false}}`))
	if err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if first.ClientStreamOptions == nil || first.ClientStreamOptions.IncludeUsage {
		t.Fatalf("first.ClientStreamOptions = %+v, want include_usage:false", first.ClientStreamOptions)
	}

	second, err := DecodeChatCompletionRequest(strings.NewReader(
		`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if second.ClientStreamOptions != nil {
		t.Fatalf("second.ClientStreamOptions = %+v, want nil (leaked from pooled first request)", second.ClientStreamOptions)
	}
}
