package bedrock

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/ferro-labs/ai-gateway/internal/logging"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// bedrockResponseID synthesizes a response ID for the Bedrock model families
// whose InvokeModel response carries none (Nova, Titan, Llama), so gateway
// responses always expose an ID as the OpenAI contract expects.
func bedrockResponseID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "bedrock"
	}
	return "bedrock-" + hex.EncodeToString(b[:])
}

// warnDroppedImageParts logs a warning when a request carries image content that
// a text-only Bedrock model family (Nova/Titan/Llama on the InvokeModel path)
// cannot forward, so the drop is observable instead of silent. Preserving images
// on these families requires the Converse API (tracked as a roadmap item).
func warnDroppedImageParts(ctx context.Context, provider, model string, messages []core.Message) {
	for _, msg := range messages {
		for _, part := range msg.ContentParts {
			if part.Type == "image_url" {
				logging.FromContext(ctx).Warn(
					"model family does not support image content on the InvokeModel path; dropping image parts",
					"provider", provider,
					"model", model,
				)
				return
			}
		}
	}
}
