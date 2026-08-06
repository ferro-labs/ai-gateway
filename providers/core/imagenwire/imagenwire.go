// Package imagenwire holds the Imagen :predict wire format shared by the Gemini
// API and Vertex AI, which serve the same model family through two endpoints.
// Keeping one translation here is what stops the two providers from answering
// the same request differently.
package imagenwire

import (
	"fmt"
	"strings"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// Instance is a single Imagen prediction input.
type Instance struct {
	Prompt string `json:"prompt"`
}

// Parameters carries the optional Imagen generation knobs.
type Parameters struct {
	SampleCount *int   `json:"sampleCount,omitempty"`
	AspectRatio string `json:"aspectRatio,omitempty"`
}

// Request is the Imagen :predict request envelope.
type Request struct {
	Instances  []Instance  `json:"instances"`
	Parameters *Parameters `json:"parameters,omitempty"`
}

// Prediction is a single Imagen :predict result.
type Prediction struct {
	BytesBase64Encoded string `json:"bytesBase64Encoded"`
	MimeType           string `json:"mimeType"`
	RAIFilteredReason  string `json:"raiFilteredReason"`
}

// Response is the Imagen :predict response envelope.
type Response struct {
	Predictions []Prediction `json:"predictions"`
}

// IsUltraModel reports whether modelID is an Imagen "ultra" variant, which
// generates only a single image per request. modelID must already be normalized
// to the bare model name by the caller.
func IsUltraModel(modelID string) bool {
	return strings.HasPrefix(modelID, "imagen-") && strings.Contains(modelID, "-ultra")
}

// BuildRequest maps a gateway ImageRequest onto the Imagen :predict shape, with
// modelID the caller's normalized model name. A recognized req.Size ("WxH") is
// mapped to the nearest Imagen aspectRatio. For ultra models, sampleCount is
// clamped to 1.
//
// req.ResponseFormat has no Imagen equivalent — predictions carry base64 only.
// It is not silently ignored here: both callers refuse an explicitly requested
// format they cannot produce up front, via core.EnforceImageResponseFormat, so
// by the time this runs the only reachable values are absent or b64_json.
func BuildRequest(req core.ImageRequest, modelID string) Request {
	out := Request{
		Instances: []Instance{{Prompt: req.Prompt}},
	}
	params := Parameters{AspectRatio: AspectRatio(req.Size)}
	if req.N != nil {
		count := *req.N
		if IsUltraModel(modelID) && count > 1 {
			count = 1
		}
		params.SampleCount = &count
	} else if IsUltraModel(modelID) {
		one := 1
		params.SampleCount = &one
	}
	if params.SampleCount != nil || params.AspectRatio != "" {
		out.Parameters = &params
	}
	return out
}

// AspectRatio maps a common OpenAI "WxH" size to the nearest Imagen
// aspectRatio. An unmapped or empty size returns "" (Imagen defaults to 1:1).
func AspectRatio(size string) string {
	switch size {
	case "1024x1024", "512x512", "256x256":
		return "1:1"
	case "1792x1024", "1536x1024":
		return "16:9"
	case "1024x1792", "1024x1536":
		return "9:16"
	case "1408x1024":
		return "4:3"
	case "1024x1408":
		return "3:4"
	default:
		return ""
	}
}

// MapPredictions converts Imagen predictions to gateway images, labelling any
// error with the calling provider. It returns an error when every prediction
// was rai-filtered or empty.
func MapPredictions(provider, model string, predictions []Prediction) ([]core.GeneratedImage, error) {
	images := make([]core.GeneratedImage, 0, len(predictions))
	var filterReason string
	for _, pred := range predictions {
		if pred.BytesBase64Encoded == "" {
			if filterReason == "" {
				filterReason = pred.RAIFilteredReason
			}
			continue
		}
		images = append(images, core.GeneratedImage{B64JSON: pred.BytesBase64Encoded})
	}
	if len(images) == 0 {
		if filterReason != "" {
			return nil, fmt.Errorf("%s image generation for %q returned no images: %s", provider, model, filterReason)
		}
		return nil, fmt.Errorf("%s image generation for %q returned no images (all predictions were filtered or empty)", provider, model)
	}
	return images, nil
}
