package vertexai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/imagenwire"
)

// GenerateImage sends an image generation request to Vertex AI's Imagen
// publisher :predict endpoint.
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	if err := core.EnforceImageResponseFormat(Name, req); err != nil {
		return nil, err
	}
	imagenReq := imagenwire.BuildRequest(req, vertexAIModelID(req.Model))

	respBody, err := p.doPredict(ctx, req.Model, imagenReq, "image")
	if err != nil {
		return nil, err
	}

	var imagenResp imagenwire.Response
	if err := json.Unmarshal(respBody, &imagenResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image response: %w", err)
	}

	images, err := imagenwire.MapPredictions("vertex ai", req.Model, imagenResp.Predictions)
	if err != nil {
		return nil, err
	}

	return &core.ImageResponse{
		Created: time.Now().Unix(),
		Data:    images,
	}, nil
}
