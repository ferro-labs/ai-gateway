package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// buildImagenRequest maps a gateway ImageRequest onto the Imagen :predict shape.
// req.Size ("WxH") is not directly mappable to Imagen and is ignored;
// req.ResponseFormat is ignored (Imagen returns base64 only).
func buildImagenRequest(req core.ImageRequest) imagenRequest {
	out := imagenRequest{
		Instances: []imagenInstance{{Prompt: req.Prompt}},
	}
	if req.N != nil {
		params := imagenParameters{SampleCount: req.N}
		out.Parameters = &params
	}
	return out
}

// mapImagenPredictions converts Imagen predictions to gateway images. It returns
// an error when every prediction was rai-filtered or empty.
func mapImagenPredictions(model string, predictions []imagenPrediction) ([]core.GeneratedImage, error) {
	images := make([]core.GeneratedImage, 0, len(predictions))
	for _, pred := range predictions {
		if pred.BytesBase64Encoded == "" {
			continue
		}
		images = append(images, core.GeneratedImage{B64JSON: pred.BytesBase64Encoded})
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("gemini image generation for %q returned no images (all predictions were filtered or empty)", model)
	}
	return images, nil
}

// GenerateImage sends an image generation request to Gemini's Imagen :predict endpoint.
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	imagenReq := buildImagenRequest(req)

	bodyReader, _, release, err := core.JSONBodyReader(imagenReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal image request: %w", err)
	}
	defer release()

	model := strings.TrimPrefix(req.Model, "models/")
	url := fmt.Sprintf("%s/v1beta/models/%s:predict?key=%s", p.baseURL, url.PathEscape(model), p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create image request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("image request failed: %w", sanitizeRequestErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read image response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIError("gemini image", httpResp.StatusCode, respBody)
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
