package bedrock

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// ── Amazon Nova Canvas + Titan Image (shared TEXT_IMAGE shape) ────────────────

type bedrockImageTitanRequest struct {
	TaskType              string                        `json:"taskType"`
	TextToImageParams     bedrockImageTextToImageParams `json:"textToImageParams"`
	ImageGenerationConfig bedrockImageGenerationConfig  `json:"imageGenerationConfig"`
}

type bedrockImageTextToImageParams struct {
	Text         string `json:"text"`
	NegativeText string `json:"negativeText,omitempty"`
}

type bedrockImageGenerationConfig struct {
	NumberOfImages int      `json:"numberOfImages,omitempty"`
	Width          int      `json:"width,omitempty"`
	Height         int      `json:"height,omitempty"`
	CfgScale       *float64 `json:"cfgScale,omitempty"`
	Seed           *int     `json:"seed,omitempty"`
	Quality        string   `json:"quality,omitempty"`
}

type bedrockImageTitanResponse struct {
	Images []string `json:"images"`
	Error  string   `json:"error,omitempty"`
}

// ── Stability Stable Diffusion XL (legacy text_prompts shape) ─────────────────

type bedrockImageStabilityRequest struct {
	TextPrompts []bedrockImageStabilityPrompt `json:"text_prompts"`
	CfgScale    *float64                      `json:"cfg_scale,omitempty"`
	Seed        *int                          `json:"seed,omitempty"`
	Steps       *int                          `json:"steps,omitempty"`
	Samples     *int                          `json:"samples,omitempty"`
}

type bedrockImageStabilityPrompt struct {
	Text string `json:"text"`
}

type bedrockImageStabilityResponse struct {
	Artifacts []struct {
		Base64       string `json:"base64"`
		FinishReason string `json:"finishReason"`
	} `json:"artifacts"`
}

// isBedrockImageModel reports whether the routing model ID belongs to a Bedrock
// image-generation family. Kept distinct from the embeddings prefixes:
// "amazon.titan-image-" must NOT capture "amazon.titan-embed-image-".
func isBedrockImageModel(model string) bool {
	for _, prefix := range []string{
		"amazon.nova-canvas",
		"amazon.titan-image-",
		"stability.stable-diffusion-xl",
	} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// GenerateImage sends a text-to-image request to AWS Bedrock, dispatching to the
// model family (Nova Canvas / Titan Image, or Stability SDXL) that matches the
// routing prefix. The original req.Model is passed to InvokeModel so cross-region
// inference-profile IDs (us./eu./apac./global./region/) are preserved upstream.
func (p *Provider) GenerateImage(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	id := bedrockModelRoutingID(req.Model)
	switch {
	case strings.HasPrefix(id, "amazon.nova-canvas"), strings.HasPrefix(id, "amazon.titan-image-"):
		return p.generateImageTitanNova(ctx, req)
	case strings.HasPrefix(id, "stability.stable-diffusion-xl"):
		return p.generateImageStability(ctx, req)
	default:
		return nil, fmt.Errorf("unsupported Bedrock image model: %s", req.Model)
	}
}

func (p *Provider) generateImageTitanNova(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	config := bedrockImageGenerationConfig{Quality: req.Quality}
	if req.N != nil {
		config.NumberOfImages = *req.N
	}
	if req.Size != "" {
		var w, h int
		// Skip unparseable sizes rather than failing; Bedrock applies model defaults.
		if n, _ := fmt.Sscanf(req.Size, "%dx%d", &w, &h); n == 2 && w > 0 && h > 0 {
			config.Width = w
			config.Height = h
		}
	}

	body := bedrockImageTitanRequest{
		TaskType:              "TEXT_IMAGE",
		TextToImageParams:     bedrockImageTextToImageParams{Text: req.Prompt},
		ImageGenerationConfig: config,
	}

	var resp bedrockImageTitanResponse
	if err := p.invokeModelJSON(ctx, req.Model, body, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("bedrock image generation failed: %s", resp.Error)
	}
	if len(resp.Images) == 0 {
		return nil, fmt.Errorf("bedrock image generation returned no images")
	}
	return bedrockImageResponseFromBase64(resp.Images), nil
}

func (p *Provider) generateImageStability(ctx context.Context, req core.ImageRequest) (*core.ImageResponse, error) {
	body := bedrockImageStabilityRequest{
		TextPrompts: []bedrockImageStabilityPrompt{{Text: req.Prompt}},
	}
	if req.N != nil {
		body.Samples = req.N
	}

	var resp bedrockImageStabilityResponse
	if err := p.invokeModelJSON(ctx, req.Model, body, &resp); err != nil {
		return nil, err
	}

	images := make([]string, 0, len(resp.Artifacts))
	for _, artifact := range resp.Artifacts {
		// Skip filtered/errored artifacts rather than aborting the whole batch;
		// only the good artifacts are collected and returned.
		if artifact.FinishReason == "CONTENT_FILTERED" || artifact.FinishReason == "ERROR" {
			continue
		}
		images = append(images, artifact.Base64)
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("bedrock image generation returned no images")
	}
	return bedrockImageResponseFromBase64(images), nil
}

func bedrockImageResponseFromBase64(images []string) *core.ImageResponse {
	data := make([]core.GeneratedImage, len(images))
	for i, img := range images {
		data[i] = core.GeneratedImage{B64JSON: img}
	}
	return &core.ImageResponse{
		Created: time.Now().Unix(),
		Data:    data,
	}
}
