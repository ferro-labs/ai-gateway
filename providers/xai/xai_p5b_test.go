package xai

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestDroppedImageParams verifies the warn-on-drop helper reports exactly the
// image parameters Grok image models ignore.
func TestDroppedImageParams(t *testing.T) {
	cases := []struct {
		name string
		req  core.ImageRequest
		want []string
	}{
		{"none", core.ImageRequest{}, nil},
		{"size", core.ImageRequest{Size: "1024x1024"}, []string{"size"}},
		{"quality", core.ImageRequest{Quality: "hd"}, []string{"quality"}},
		{"style", core.ImageRequest{Style: "vivid"}, []string{"style"}},
		{"all", core.ImageRequest{Size: "1024x1024", Quality: "hd", Style: "vivid"}, []string{"size", "quality", "style"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := droppedImageParams(tc.req)
			if len(got) != len(tc.want) {
				t.Fatalf("droppedImageParams = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("droppedImageParams[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestNewXAI_RejectsInvalidBaseURL locks in the base-URL validation.
func TestNewXAI_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://bad"); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}
