package anthropicwire

import (
	"encoding/json"
	"testing"
)

func TestParseDataURI(t *testing.T) {
	cases := []struct {
		name      string
		uri       string
		wantOK    bool
		wantMedia string
		wantData  string
	}{
		{"base64", "data:image/png;base64,QUJD", true, "image/png", "QUJD"},
		{"base64 after charset param", "data:image/png;charset=utf-8;base64,QUJD", true, "image/png", "QUJD"},
		{"non-base64 data URI", "data:image/png,rawtext", false, "", ""},
		{"remote url", "https://example.com/x.png", false, "", ""},
		{"no comma", "data:image/png;base64", false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mt, data, ok := ParseDataURI(tc.uri)
			if ok != tc.wantOK || mt != tc.wantMedia || data != tc.wantData {
				t.Fatalf("ParseDataURI(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.uri, mt, data, ok, tc.wantMedia, tc.wantData, tc.wantOK)
			}
		})
	}
}

// ---- The cache-read fold -------------------------------------------------

// Anthropic reports input_tokens EXCLUSIVE of the cache-read subset; the rest
// of the gateway carries the OpenAI convention, in which prompt_tokens is
// INCLUSIVE of it. Decoding must fold, or the same workload reports a different
// prompt-token count — and is billed differently — depending on which provider
// served it.
func TestUsageUnmarshalFoldsCacheReadIntoInputTokens(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantInput     int
		wantCacheRead int
		wantCacheWrit int
	}{
		{
			name:          "cache read folded in",
			body:          `{"input_tokens":2000,"output_tokens":50,"cache_read_input_tokens":8000}`,
			wantInput:     10000,
			wantCacheRead: 8000,
		},
		{
			name:      "no cache hit is unchanged",
			body:      `{"input_tokens":2000,"output_tokens":50}`,
			wantInput: 2000,
		},
		{
			// A cache WRITE bills at its own rate on top of the prompt and has
			// no OpenAI equivalent, so it is deliberately not folded.
			name:          "cache write is not folded",
			body:          `{"input_tokens":2000,"output_tokens":50,"cache_creation_input_tokens":600}`,
			wantInput:     2000,
			wantCacheWrit: 600,
		},
		{
			name:          "both cache fields",
			body:          `{"input_tokens":2000,"output_tokens":50,"cache_read_input_tokens":8000,"cache_creation_input_tokens":600}`,
			wantInput:     10000,
			wantCacheRead: 8000,
			wantCacheWrit: 600,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var u Usage
			if err := json.Unmarshal([]byte(tc.body), &u); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if u.InputTokens != tc.wantInput {
				t.Errorf("InputTokens = %d, want %d", u.InputTokens, tc.wantInput)
			}
			if u.CacheReadInputTokens != tc.wantCacheRead {
				t.Errorf("CacheReadInputTokens = %d, want %d", u.CacheReadInputTokens, tc.wantCacheRead)
			}
			if u.CacheCreationInputTokens != tc.wantCacheWrit {
				t.Errorf("CacheCreationInputTokens = %d, want %d", u.CacheCreationInputTokens, tc.wantCacheWrit)
			}
		})
	}
}

// The non-streaming Messages response is the other half of the boundary: the
// fold has to reach the providers that read Response.Usage (anthropic and
// anthropic-on-bedrock), not just the stream decoder.
func TestResponseUsageIsCacheInclusive(t *testing.T) {
	const body = `{"id":"msg_1","model":"claude-3-5-sonnet","role":"assistant",` +
		`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":2000,"output_tokens":50,"cache_read_input_tokens":8000,"cache_creation_input_tokens":600}}`

	var r Response
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Usage.InputTokens != 10000 {
		t.Errorf("InputTokens = %d, want 10000 (2000 fresh + 8000 cache read)", r.Usage.InputTokens)
	}
	if r.Usage.CacheReadInputTokens != 8000 || r.Usage.CacheCreationInputTokens != 600 {
		t.Errorf("cache tokens = %d/%d, want 8000/600",
			r.Usage.CacheReadInputTokens, r.Usage.CacheCreationInputTokens)
	}
}
