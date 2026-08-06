package imagenwire

import (
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestBuildRequest_MapsSizeToAspectRatio(t *testing.T) {
	cases := map[string]string{
		"1024x1024": "1:1",
		"1792x1024": "16:9",
		"1024x1792": "9:16",
		"1408x1024": "4:3",
		"1024x1408": "3:4",
		"640x480":   "", // unmapped
	}
	for size, want := range cases {
		got := BuildRequest(core.ImageRequest{Prompt: "x", Size: size}, "imagen-4.0-generate-001")
		var ar string
		if got.Parameters != nil {
			ar = got.Parameters.AspectRatio
		}
		if ar != want {
			t.Errorf("size %q -> aspectRatio %q, want %q", size, ar, want)
		}
	}
}

func TestBuildRequest_SampleCount(t *testing.T) {
	four := 4

	tests := []struct {
		name    string
		modelID string
		n       *int
		want    *int
	}{
		{name: "standard model forwards n", modelID: "imagen-4.0-generate-001", n: &four, want: &four},
		{name: "ultra model clamps n to 1", modelID: "imagen-4.0-ultra-generate-001", n: &four, want: intPtr(1)},
		{name: "ultra model pins 1 when n is unset", modelID: "imagen-4.0-ultra-generate-001", want: intPtr(1)},
		{name: "standard model omits sampleCount when n is unset", modelID: "imagen-4.0-generate-001"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildRequest(core.ImageRequest{Prompt: "x", Model: tt.modelID, N: tt.n}, tt.modelID)
			var sampleCount *int
			if got.Parameters != nil {
				sampleCount = got.Parameters.SampleCount
			}
			switch {
			case tt.want == nil && sampleCount != nil:
				t.Errorf("sampleCount = %d, want it omitted", *sampleCount)
			case tt.want != nil && (sampleCount == nil || *sampleCount != *tt.want):
				t.Errorf("sampleCount = %v, want %d", sampleCount, *tt.want)
			}
			// The caller's n must never be rewritten by the clamp.
			if tt.n != nil && *tt.n != four {
				t.Errorf("request N mutated to %d", *tt.n)
			}
		})
	}
}

func TestMapPredictions(t *testing.T) {
	images, err := MapPredictions("gemini", "imagen-4.0-generate-001", []Prediction{
		{RAIFilteredReason: "safety"},
		{BytesBase64Encoded: "aGk="},
	})
	if err != nil {
		t.Fatalf("MapPredictions() error: %v", err)
	}
	if len(images) != 1 || images[0].B64JSON != "aGk=" {
		t.Errorf("images = %#v, want the single unfiltered image", images)
	}

	_, err = MapPredictions("vertex ai", "imagen-4.0-generate-001", []Prediction{{RAIFilteredReason: "safety"}})
	if err == nil {
		t.Fatal("expected an error when every prediction is filtered")
	}
	if !strings.Contains(err.Error(), "vertex ai") || !strings.Contains(err.Error(), "safety") {
		t.Errorf("error = %q, want the calling provider and the filter reason", err)
	}
}

func intPtr(n int) *int { return &n }
