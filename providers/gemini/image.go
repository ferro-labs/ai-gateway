package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/providers/core/imagenwire"
)

// GenerateImage routes to the correct Gemini image surface by model family.
// Imagen models generate through the :predict endpoint; the gemini image models
// (gemini-2.5-flash-image and newer) generate through :generateContent and
// return the image as base64 inlineData inside a candidate part. Both paths are
// required because Google no longer grants Imagen :predict to new API keys — a
// new account can only generate images through the generateContent models.
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	if err := core.EnforceImageResponseFormat(Name, req); err != nil {
		return nil, err
	}
	model := strings.TrimPrefix(req.Model, "models/")
	if strings.HasPrefix(model, "imagen") {
		return p.generateImagePredict(ctx, req, model)
	}
	return p.generateImageContent(ctx, req, model)
}

// generateImagePredict calls Imagen's :predict endpoint (Imagen 3/4 family).
func (p *Provider) generateImagePredict(ctx context.Context, req core.ImageRequest, model string) (*core.ImageResponse, error) {
	imagenReq := imagenwire.BuildRequest(req, model)

	reqURL := fmt.Sprintf("%s/models/%s:predict", p.baseURL, url.PathEscape(model))
	httpResp, release, err := p.doJSONRequest(ctx, reqURL, "image ", imagenReq)
	if err != nil {
		return nil, err
	}
	defer release()
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read image response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIErrorFromResponse("gemini image", httpResp, respBody)
	}

	var imagenResp imagenwire.Response
	if err := json.Unmarshal(respBody, &imagenResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image response: %w", err)
	}

	images, err := imagenwire.MapPredictions(Name, req.Model, imagenResp.Predictions)
	if err != nil {
		return nil, err
	}

	return &core.ImageResponse{
		Created: time.Now().Unix(),
		Data:    images,
	}, nil
}

// generateImageContent calls the generateContent endpoint for the gemini image
// models, whose generated image comes back as base64 inlineData in a candidate
// part — the same shape the chat path already decodes.
func (p *Provider) generateImageContent(ctx context.Context, req core.ImageRequest, model string) (*core.ImageResponse, error) {
	gReq := geminiRequest{
		Contents: []geminiContent{{Parts: []geminiPart{{Text: req.Prompt}}}},
		// responseModalities is documented for these models and makes the older
		// *-image-generation preview models emit an image too; flash-image would
		// default to it. OpenAI's pixel Size does not map to Gemini's
		// aspectRatio/imageSize, so size/quality hints are dropped here.
		GenerationConfig: &geminiGenerationConfig{ResponseModalities: []string{"TEXT", "IMAGE"}},
	}

	reqURL := fmt.Sprintf("%s/models/%s:generateContent", p.baseURL, url.PathEscape(model))
	httpResp, release, err := p.doJSONRequest(ctx, reqURL, "image ", gReq)
	if err != nil {
		return nil, err
	}
	defer release()
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := core.ReadResponseBody(httpResp.Body, core.MaxProviderResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read image response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, core.APIErrorFromResponse("gemini image", httpResp, respBody)
	}

	var decoded geminiResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("failed to unmarshal image response: %w", err)
	}

	var images []core.GeneratedImage
	for _, c := range decoded.Candidates {
		for _, part := range c.Content.Parts {
			if part.InlineData != nil && part.InlineData.Data != "" {
				images = append(images, core.GeneratedImage{B64JSON: part.InlineData.Data})
			}
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("gemini image: response carried no image data")
	}

	return &core.ImageResponse{
		Created: time.Now().Unix(),
		Data:    images,
	}, nil
}
