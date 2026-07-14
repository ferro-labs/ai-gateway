package bedrock

import (
	"strings"
	"testing"
)

// TestBedrockAnthropicImageBlock verifies that Bedrock's Anthropic image
// content builder accepts base64 data URIs and rejects remote image URLs,
// since Bedrock's Anthropic models only accept base64-encoded images.
func TestBedrockAnthropicImageBlock(t *testing.T) {
	t.Run("base64 data URI is accepted", func(t *testing.T) {
		block, err := bedrockAnthropicImageBlock("data:image/png;base64,AAAA")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if block.Type != "image" || block.Source == nil {
			t.Fatalf("unexpected block: %+v", block)
		}
		if block.Source.Type != "base64" || block.Source.MediaType != "image/png" || block.Source.Data != "AAAA" {
			t.Fatalf("unexpected source: %+v", block.Source)
		}
	})

	for _, url := range []string{
		"https://example.com/cat.png",
		"http://example.com/cat.png",
		"ftp://example.com/cat.png",
	} {
		t.Run("remote URL is rejected: "+url, func(t *testing.T) {
			_, err := bedrockAnthropicImageBlock(url)
			if err == nil {
				t.Fatalf("expected an error for remote image URL %q, got nil", url)
			}
			if !strings.Contains(err.Error(), "base64 data URIs") {
				t.Fatalf("error = %q, want it to explain the base64 requirement", err.Error())
			}
		})
	}
}
