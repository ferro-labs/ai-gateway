package core

import (
	"slices"
	"strings"
)

// ImageResponseFormatURL and ImageResponseFormatB64JSON are the two
// response_format values the OpenAI image API defines.
const (
	ImageResponseFormatURL     = "url"
	ImageResponseFormatB64JSON = "b64_json"
)

// imageResponseFormats records, per provider, the response_format values that
// provider's image surface can actually produce. A provider absent from this
// map is unconstrained: it either honours both or forwards the field and lets
// its upstream answer.
//
// This is deliberately NOT an entry in providers/capabilities: that matrix
// describes chat parameters, where response_format means JSON mode, and its
// enforcement seam (EnforceUnsupportedParams) is reached only from Complete and
// CompleteStream — never from GenerateImage. The image surface had no
// capability path at all, which is why a format nobody could produce was
// answered with a wrong-shaped 200.
//
// Verified against each provider's GenerateImage:
//   - bedrock returns base64 from every family (bedrockImageResponseFromBase64)
//   - gemini and vertex-ai read Imagen predictions' bytesBase64Encoded
//   - hugging-face base64-encodes the raw image bytes the task API returns
//   - replicate's prediction output is a URL (or a list of them)
var imageResponseFormats = map[string][]string{
	"bedrock":      {ImageResponseFormatB64JSON},
	"gemini":       {ImageResponseFormatB64JSON},
	"vertex-ai":    {ImageResponseFormatB64JSON},
	"hugging-face": {ImageResponseFormatB64JSON},
	"replicate":    {ImageResponseFormatURL},
}

// ImageResponseFormatsFor returns the response_format values provider's image
// surface can produce, or nil when it is unconstrained. It is what
// GET /v1/capabilities publishes, so a client can discover the restriction
// rather than meet it as an error.
func ImageResponseFormatsFor(provider string) []string {
	return slices.Clone(imageResponseFormats[provider])
}

// EnforceImageResponseFormat rejects an explicitly requested response_format
// the provider cannot produce, with the same HTTP 400 an unsupported chat
// parameter gets. It is a no-op when the caller set nothing: OpenAI defaults
// response_format to "url", so enforcing the default would refuse requests this
// gateway has always served.
//
// There is deliberately no conversion. Fetching a URL the gateway did not mint
// to base64-encode it is an SSRF primitive and puts unbounded egress on the
// image path; uploading bytes to mint a URL needs storage the gateway does not
// have. No comparable gateway converts either.
//
// produced overrides the provider-level map for a provider whose answer is
// per-model — OpenAI serves DALL·E, which honours both formats, and gpt-image-*,
// which always returns base64 and rejects the field outright.
func EnforceImageResponseFormat(provider string, req ImageRequest, produced ...string) error {
	if req.ResponseFormat == "" {
		return nil
	}
	if len(produced) == 0 {
		produced = imageResponseFormats[provider]
		if len(produced) == 0 {
			return nil
		}
	}
	if slices.Contains(produced, req.ResponseFormat) {
		return nil
	}
	return NewUnsupportedParamError(provider, []string{
		"response_format=" + req.ResponseFormat + " (produces: " + strings.Join(produced, ", ") + ")",
	})
}

// ImageRequest mirrors the OpenAI /v1/images/generations request schema.
type ImageRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              *int   `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"` // "url" | "b64_json"
	Quality        string `json:"quality,omitempty"`
	Style          string `json:"style,omitempty"`
	User           string `json:"user,omitempty"`
}

// ImageResponse mirrors the OpenAI /v1/images/generations response schema.
type ImageResponse struct {
	Created int64            `json:"created"`
	Data    []GeneratedImage `json:"data"`
	// Usage carries the token counts the provider reported for the generation.
	// It exists because per-image pricing is not the only image billing shape:
	// the gpt-image family carries no per-tile price in the model catalog and is
	// billed per token instead, so a discarded usage is a real generation costed
	// at nothing.
	//
	// A zero value means the provider reported none — which is every image
	// provider except OpenAI and Azure OpenAI — and such a request is priced as
	// unpriced rather than as $0.00. See models.Calculate's ModeImage arm.
	//
	// It is deliberately not serialized. The OpenAI image schema spells this
	// object input_tokens/output_tokens, while Usage marshals the chat spelling
	// (prompt_tokens/completion_tokens), so emitting it would answer an
	// OpenAI-compatible client with keys it does not read.
	Usage Usage `json:"-"`
}

// GeneratedImage holds the result of a single image generation.
type GeneratedImage struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}
