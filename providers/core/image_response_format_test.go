package core

import (
	"errors"
	"strings"
	"testing"
)

// A provider that cannot produce the requested representation used to answer
// 200 with the other one. The caller asked for a URL, read data[0].url, found
// it empty, and had no error to act on. These tests pin the refusal and,
// equally, the two cases that must NOT be refused.

func TestEnforceImageResponseFormat(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		format   string
		produced []string
		wantErr  bool
	}{
		// b64-only providers.
		{name: "bedrock url", provider: "bedrock", format: "url", wantErr: true},
		{name: "bedrock b64", provider: "bedrock", format: "b64_json"},
		{name: "gemini url", provider: "gemini", format: "url", wantErr: true},
		{name: "vertex-ai url", provider: "vertex-ai", format: "url", wantErr: true},
		{name: "hugging-face url", provider: "hugging-face", format: "url", wantErr: true},

		// url-only provider.
		{name: "replicate b64", provider: "replicate", format: "b64_json", wantErr: true},
		{name: "replicate url", provider: "replicate", format: "url"},

		// Unset is never enforced: OpenAI defaults response_format to "url", so
		// enforcing the default would refuse every caller that omits the field.
		{name: "bedrock unset", provider: "bedrock", format: ""},
		{name: "replicate unset", provider: "replicate", format: ""},

		// Providers that honour both are absent from the map and unconstrained.
		{name: "openai url", provider: "openai", format: "url"},
		{name: "azure-openai b64", provider: "azure-openai", format: "b64_json"},
		{name: "xai url", provider: "xai", format: "url"},
		{name: "unknown provider", provider: "not-a-provider", format: "url"},

		// An explicit produced set overrides the provider-level map, for a
		// provider whose answer is per-model.
		{name: "per-model b64 only rejects url", provider: "openai", format: "url",
			produced: []string{"b64_json"}, wantErr: true},
		{name: "per-model b64 only accepts b64", provider: "openai", format: "b64_json",
			produced: []string{"b64_json"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := EnforceImageResponseFormat(tc.provider,
				ImageRequest{Model: "m", Prompt: "p", ResponseFormat: tc.format}, tc.produced...)
			if tc.wantErr && err == nil {
				t.Fatalf("EnforceImageResponseFormat(%q, %q) = nil, want a refusal",
					tc.provider, tc.format)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("EnforceImageResponseFormat(%q, %q) = %v, want nil",
					tc.provider, tc.format, err)
			}
		})
	}
}

// The refusal must be a 400 the caller can act on, naming both what they asked
// for and what is available — not a generic failure and not a 5xx.
func TestEnforceImageResponseFormat_IsActionable400(t *testing.T) {
	err := EnforceImageResponseFormat("replicate",
		ImageRequest{Model: "m", Prompt: "p", ResponseFormat: "b64_json"})

	var unsupported *UnsupportedParamError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v (%T), want *UnsupportedParamError", err, err)
	}
	if got := ParseStatusCode(err); got != 400 {
		t.Errorf("ParseStatusCode = %d, want 400", got)
	}
	msg := err.Error()
	for _, want := range []string{"response_format=b64_json", "url"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

// ImageResponseFormatsFor is what GET /v1/capabilities publishes; an entry that
// stops matching what the provider produces would advertise a format callers
// then get a 400 for.
func TestImageResponseFormatsFor(t *testing.T) {
	want := map[string][]string{
		"bedrock":      {"b64_json"},
		"gemini":       {"b64_json"},
		"vertex-ai":    {"b64_json"},
		"hugging-face": {"b64_json"},
		"replicate":    {"url"},
	}
	for provider, formats := range want {
		got := ImageResponseFormatsFor(provider)
		if len(got) != len(formats) || got[0] != formats[0] {
			t.Errorf("ImageResponseFormatsFor(%q) = %v, want %v", provider, got, formats)
		}
	}
	// An unrestricted provider must publish nothing rather than an empty claim.
	for _, provider := range []string{"openai", "azure-openai", "xai"} {
		if got := ImageResponseFormatsFor(provider); got != nil {
			t.Errorf("ImageResponseFormatsFor(%q) = %v, want nil (unconstrained)", provider, got)
		}
	}
}

// ImageResponseFormatsFor feeds GET /v1/capabilities, so a caller mutating the
// slice it returns must not reach the shared map behind it.
func TestImageResponseFormatsFor_ReturnsACopy(t *testing.T) {
	got := ImageResponseFormatsFor("bedrock")
	if len(got) == 0 {
		t.Fatal("ImageResponseFormatsFor(bedrock) = empty, want at least one format")
	}
	got[0] = "mutated"
	if again := ImageResponseFormatsFor("bedrock"); again[0] == "mutated" {
		t.Errorf("mutating the returned slice corrupted the shared map: %v", again)
	}
}
