package vertexai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// imagenInstance is a single Imagen prediction input.
type imagenInstance struct {
	Prompt string `json:"prompt"`
}

// imagenParameters carries the optional Imagen generation knobs.
type imagenParameters struct {
	SampleCount *int   `json:"sampleCount,omitempty"`
	AspectRatio string `json:"aspectRatio,omitempty"`
}

// imagenRequest is the Imagen :predict request envelope.
type imagenRequest struct {
	Instances  []imagenInstance  `json:"instances"`
	Parameters *imagenParameters `json:"parameters,omitempty"`
}

// imagenPrediction is a single Imagen :predict result.
type imagenPrediction struct {
	BytesBase64Encoded string `json:"bytesBase64Encoded"`
	MimeType           string `json:"mimeType"`
	RAIFilteredReason  string `json:"raiFilteredReason"`
}

// imagenResponse is the Imagen :predict response envelope.
type imagenResponse struct {
	Predictions []imagenPrediction `json:"predictions"`
}

// isVertexAIUltraImageModel reports whether the model is an Imagen "ultra"
// variant, which supports only a single generated image per request.
func isVertexAIUltraImageModel(model string) bool {
	model = vertexAIModelID(model)
	return strings.HasPrefix(model, "imagen-") && strings.Contains(model, "-ultra")
}

// buildImagenRequest maps a gateway ImageRequest onto the Imagen :predict shape.
// A recognized req.Size ("WxH") is mapped to the nearest Imagen aspectRatio;
// req.ResponseFormat is ignored (Imagen returns base64 only). For ultra models,
// sampleCount is clamped to 1.
func buildImagenRequest(req core.ImageRequest) imagenRequest {
	out := imagenRequest{
		Instances: []imagenInstance{{Prompt: req.Prompt}},
	}
	params := imagenParameters{AspectRatio: imagenAspectRatio(req.Size)}
	if req.N != nil {
		count := *req.N
		if isVertexAIUltraImageModel(req.Model) && count > 1 {
			count = 1
		}
		params.SampleCount = &count
	} else if isVertexAIUltraImageModel(req.Model) {
		one := 1
		params.SampleCount = &one
	}
	if params.SampleCount != nil || params.AspectRatio != "" {
		out.Parameters = &params
	}
	return out
}

// imagenAspectRatio maps a common OpenAI "WxH" size to the nearest Imagen
// aspectRatio. An unmapped or empty size returns "" (Imagen defaults to 1:1).
func imagenAspectRatio(size string) string {
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

// mapImagenPredictions converts Imagen predictions to gateway images. It returns
// an error when every prediction was rai-filtered or empty.
func mapImagenPredictions(model string, predictions []imagenPrediction) ([]core.GeneratedImage, error) {
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
			return nil, fmt.Errorf("vertex ai image generation for %q returned no images: %s", model, filterReason)
		}
		return nil, fmt.Errorf("vertex ai image generation for %q returned no images (all predictions were filtered or empty)", model)
	}
	return images, nil
}

// GenerateImage sends an image generation request to Vertex AI's Imagen
// publisher :predict endpoint.
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	imagenReq := buildImagenRequest(req)

	respBody, err := p.doPredict(ctx, req.Model, imagenReq, "image")
	if err != nil {
		return nil, err
	}

	var imagenResp imagenResponse
	if err := json.Unmarshal(respBody, &imagenResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image response: %w", err)
	}

	images, err := mapImagenPredictions(req.Model, imagenResp.Predictions)
	if err != nil {
		return nil, err
	}

	return &core.ImageResponse{
		Created: time.Now().Unix(),
		Data:    images,
	}, nil
}
