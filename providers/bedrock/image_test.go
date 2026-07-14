package bedrock

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestBedrockProvider_GenerateImage_Interface(_ *testing.T) {
	var _ core.ImageProvider = (*Provider)(nil)
}

func TestBedrockProvider_SupportsImageModels(t *testing.T) {
	p := &Provider{name: Name}

	for _, want := range []string{
		"amazon.nova-canvas-v1:0",
		"amazon.titan-image-generator-v1",
		"amazon.titan-image-generator-v2:0",
		"stability.stable-diffusion-xl-v1",
	} {
		if !containsString(p.SupportedModels(), want) {
			t.Errorf("SupportedModels() missing %q", want)
		}
		if !p.SupportsModel(want) {
			t.Errorf("SupportsModel(%q) = false, want true", want)
		}
	}

	// Cross-region inference-profile and region-prefixed forms must also match.
	for _, want := range []string{
		"us.amazon.nova-canvas-v1:0",
		"global.amazon.titan-image-generator-v2:0",
		"us-gov-west-1/stability.stable-diffusion-xl-v1",
	} {
		if !p.SupportsModel(want) {
			t.Errorf("SupportsModel(%q) = false, want true", want)
		}
	}

	// The titan-image prefix must NOT capture the titan-embed-image family.
	if p.SupportsModel("amazon.titan-embed-image-v1") {
		t.Error("SupportsModel(amazon.titan-embed-image-v1) = true, want false (embeddings, not image)")
	}
}

func TestIsBedrockImageModel_StabilityOnlySDXL(t *testing.T) {
	// Only stability.stable-diffusion-xl is actually dispatched, so the
	// capability claim must not cover unimplemented stability.* families.
	if isBedrockImageModel("stability.stable-image-ultra-v1:0") {
		t.Error("isBedrockImageModel(stability.stable-image-ultra-v1:0) = true, want false (not dispatched)")
	}
	if isBedrockImageModel("stability.sd3-large-v1:0") {
		t.Error("isBedrockImageModel(stability.sd3-large-v1:0) = true, want false (not dispatched)")
	}
	if !isBedrockImageModel("stability.stable-diffusion-xl-v1") {
		t.Error("isBedrockImageModel(stability.stable-diffusion-xl-v1) = false, want true")
	}

	p := &Provider{name: Name}
	if p.SupportsModel("stability.stable-image-ultra-v1:0") {
		t.Error("SupportsModel(stability.stable-image-ultra-v1:0) = true, want false")
	}
	if !p.SupportsModel("stability.stable-diffusion-xl-v1") {
		t.Error("SupportsModel(stability.stable-diffusion-xl-v1) = false, want true")
	}
}

func TestBedrockProvider_GenerateImage_NovaCanvas(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{[]byte(`{"images":["aGk="]}`)},
	}
	p := &Provider{name: Name, client: fake}
	n := 1

	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "amazon.nova-canvas-v1:0",
		Prompt: "a red bicycle",
		N:      &n,
		Size:   "1024x768",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if len(fake.invokeCalls) != 1 {
		t.Fatalf("InvokeModel calls = %d, want 1", len(fake.invokeCalls))
	}
	if got := aws.ToString(fake.invokeCalls[0].ModelId); got != "amazon.nova-canvas-v1:0" {
		t.Errorf("ModelId = %q, want original model ID", got)
	}

	var body bedrockImageTitanRequest
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if body.TaskType != "TEXT_IMAGE" {
		t.Errorf("taskType = %q, want TEXT_IMAGE", body.TaskType)
	}
	if body.TextToImageParams.Text != "a red bicycle" {
		t.Errorf("text = %q, want prompt", body.TextToImageParams.Text)
	}
	if body.ImageGenerationConfig.NumberOfImages != 1 {
		t.Errorf("numberOfImages = %d, want 1", body.ImageGenerationConfig.NumberOfImages)
	}
	if body.ImageGenerationConfig.Width != 1024 || body.ImageGenerationConfig.Height != 768 {
		t.Errorf("width/height = %d/%d, want 1024/768", body.ImageGenerationConfig.Width, body.ImageGenerationConfig.Height)
	}

	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "aGk=" {
		t.Errorf("data = %+v, want single base64 image aGk=", resp.Data)
	}
	if resp.Created == 0 {
		t.Error("Created = 0, want unix timestamp")
	}
}

func TestBedrockProvider_GenerateImage_TitanImage(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{[]byte(`{"images":["aGk="]}`)},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "amazon.titan-image-generator-v2:0",
		Prompt: "a blue cat",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if got := aws.ToString(fake.invokeCalls[0].ModelId); got != "amazon.titan-image-generator-v2:0" {
		t.Errorf("ModelId = %q, want original model ID", got)
	}
	var body bedrockImageTitanRequest
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if body.TaskType != "TEXT_IMAGE" || body.TextToImageParams.Text != "a blue cat" {
		t.Errorf("body = %+v, want TEXT_IMAGE shape with prompt", body)
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "aGk=" {
		t.Errorf("data = %+v, want single base64 image aGk=", resp.Data)
	}
}

func TestBedrockProvider_GenerateImage_StabilitySDXL(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{[]byte(`{"artifacts":[{"base64":"aGk=","finishReason":"SUCCESS"}]}`)},
	}
	p := &Provider{name: Name, client: fake}

	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "stability.stable-diffusion-xl-v1",
		Prompt: "a green tree",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if got := aws.ToString(fake.invokeCalls[0].ModelId); got != "stability.stable-diffusion-xl-v1" {
		t.Errorf("ModelId = %q, want original model ID", got)
	}
	var body bedrockImageStabilityRequest
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if len(body.TextPrompts) != 1 || body.TextPrompts[0].Text != "a green tree" {
		t.Errorf("text_prompts = %+v, want single prompt", body.TextPrompts)
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "aGk=" {
		t.Errorf("data = %+v, want single base64 image aGk=", resp.Data)
	}
}

func TestBedrockProvider_GenerateImage_StabilityAllFilteredReturnsNoImages(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{[]byte(`{"artifacts":[{"base64":"","finishReason":"CONTENT_FILTERED"}]}`)},
	}
	p := &Provider{name: Name, client: fake}

	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "stability.stable-diffusion-xl-v1",
		Prompt: "blocked",
	})
	if err == nil {
		t.Fatal("GenerateImage() error = nil, want no-images error")
	}
	if !strings.Contains(err.Error(), "no images") {
		t.Errorf("error = %q, want no-images error after all artifacts filtered", err.Error())
	}
}

func TestBedrockProvider_GenerateImage_StabilitySamplesAndSkipsFiltered(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{[]byte(`{"artifacts":[{"base64":"aGk=","finishReason":"SUCCESS"},{"base64":"","finishReason":"CONTENT_FILTERED"}]}`)},
	}
	p := &Provider{name: Name, client: fake}
	n := 2

	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "stability.stable-diffusion-xl-v1",
		Prompt: "two cats",
		N:      &n,
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}

	var body bedrockImageStabilityRequest
	mustUnmarshalBody(t, fake.invokeCalls[0].Body, &body)
	if body.Samples == nil || *body.Samples != 2 {
		t.Errorf("samples = %v, want 2 from req.N", body.Samples)
	}

	if len(resp.Data) != 1 || resp.Data[0].B64JSON != "aGk=" {
		t.Errorf("data = %+v, want single good image with filtered artifact skipped", resp.Data)
	}
}

func TestBedrockProvider_GenerateImage_NovaErrorFieldFails(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{
		responses: [][]byte{[]byte(`{"images":[],"error":"prompt rejected"}`)},
	}
	p := &Provider{name: Name, client: fake}

	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "amazon.nova-canvas-v1:0",
		Prompt: "blocked",
	})
	if err == nil {
		t.Fatal("GenerateImage() error = nil, want error from response error field")
	}
	if !strings.Contains(err.Error(), "prompt rejected") {
		t.Errorf("error = %q, want response error message", err.Error())
	}
}

func TestBedrockProvider_GenerateImage_UnsupportedModel(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{}
	p := &Provider{name: Name, client: fake}

	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Prompt: "hello",
	})
	if err == nil {
		t.Fatal("GenerateImage() error = nil, want unsupported-model error")
	}
	if !strings.Contains(err.Error(), "unsupported Bedrock image model") {
		t.Errorf("error = %q, want unsupported model message", err.Error())
	}
	if len(fake.invokeCalls) != 0 {
		t.Errorf("InvokeModel calls = %d, want 0 for unsupported model", len(fake.invokeCalls))
	}
}
