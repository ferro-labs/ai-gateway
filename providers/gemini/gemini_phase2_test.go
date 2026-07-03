package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// geminiCompleteBody runs one Complete against a stub returning respBody and
// returns both the decoded request body and the provider response.
func geminiCompleteBody(t *testing.T, req core.Request, respBody string) (map[string]json.RawMessage, *core.Response) {
	t.Helper()
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	resp, err := p.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return captured, resp
}

type geminiWirePart struct {
	Text       string `json:"text"`
	InlineData *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData"`
	FileData *struct {
		FileURI string `json:"fileUri"`
	} `json:"fileData"`
}

// TestGeminiProvider_Complete_ForwardsVisionParts verifies multimodal image
// parts are mapped to Gemini inlineData (data URI) and fileData (remote URL)
// instead of being dropped.
func TestGeminiProvider_Complete_ForwardsVisionParts(t *testing.T) {
	body, _ := geminiCompleteBody(t, core.Request{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{{
			Role: core.RoleUser,
			ContentParts: []core.ContentPart{
				{Type: "text", Text: "what is this"},
				{Type: "image_url", ImageURL: &core.ImageURLPart{URL: "data:image/png;base64,QUJD"}},
				{Type: "image_url", ImageURL: &core.ImageURLPart{URL: "https://example.com/cat.png"}},
			},
		}},
	}, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":1}}`)

	var contents []struct {
		Parts []geminiWirePart `json:"parts"`
	}
	if err := json.Unmarshal(body["contents"], &contents); err != nil {
		t.Fatalf("decode contents: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("contents len = %d, want 1", len(contents))
	}
	parts := contents[0].Parts
	var sawInline, sawFile bool
	for _, p := range parts {
		if p.InlineData != nil && p.InlineData.Data == "QUJD" && p.InlineData.MimeType == "image/png" {
			sawInline = true
		}
		if p.FileData != nil && p.FileData.FileURI == "https://example.com/cat.png" {
			sawFile = true
		}
	}
	if !sawInline {
		t.Errorf("data-URI image not mapped to inlineData; parts = %+v", parts)
	}
	if !sawFile {
		t.Errorf("remote image not mapped to fileData; parts = %+v", parts)
	}
}

// TestGeminiProvider_Complete_SurfacesProviderIDAndTokens verifies the response
// carries the provider name, the upstream responseId, and the cached/reasoning
// token detail.
func TestGeminiProvider_Complete_SurfacesProviderIDAndTokens(t *testing.T) {
	_, resp := geminiCompleteBody(t, core.Request{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	}, `{"responseId":"resp_abc","candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8,"cachedContentTokenCount":2,"thoughtsTokenCount":4}}`)

	if resp.Provider != "gemini" {
		t.Errorf("Provider = %q, want gemini", resp.Provider)
	}
	if resp.ID != "resp_abc" {
		t.Errorf("ID = %q, want resp_abc (upstream responseId)", resp.ID)
	}
	if resp.Usage.CacheReadTokens != 2 || resp.Usage.ReasoningTokens != 4 {
		t.Errorf("usage cache/reasoning = %d/%d, want 2/4", resp.Usage.CacheReadTokens, resp.Usage.ReasoningTokens)
	}
}

// TestGeminiProvider_FinishReason_ContentFilter verifies Gemini's content-block
// reasons normalize to the canonical content_filter value (#264).
func TestGeminiProvider_FinishReason_ContentFilter(t *testing.T) {
	_, resp := geminiCompleteBody(t, core.Request{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	}, `{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"RECITATION"}],"usageMetadata":{"totalTokenCount":1}}`)

	if len(resp.Choices) != 1 || resp.Choices[0].FinishReason != core.FinishReasonContentFilter {
		t.Fatalf("finish_reason = %v, want content_filter", resp.Choices)
	}
}

// TestGeminiProvider_Complete_ErrorPath verifies a non-200 surfaces the upstream
// message via core.APIError.
func TestGeminiProvider_Complete_ErrorPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"quota exceeded"}}`)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "quota exceeded") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want status + upstream message", err)
	}
}

// TestGeminiProvider_CompleteStream_ReportsUsage verifies streaming surfaces
// token usage from the final chunk's usageMetadata.
func TestGeminiProvider_CompleteStream_ReportsUsage(t *testing.T) {
	sse := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":9,\"candidatesTokenCount\":4,\"totalTokenCount\":13,\"thoughtsTokenCount\":2}}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	p, _ := New("test-key", srv.URL)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "gemini-2.5-flash",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}

	var usage *core.Usage
	for c := range ch {
		if c.Error != nil {
			t.Fatalf("stream error: %v", c.Error)
		}
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	if usage == nil {
		t.Fatal("no usage reported on gemini stream")
	}
	if usage.PromptTokens != 9 || usage.CompletionTokens != 4 || usage.TotalTokens != 13 || usage.ReasoningTokens != 2 {
		t.Fatalf("usage = %+v, want 9/4/13 with 2 reasoning", usage)
	}
}

func TestGeminiProvider_New_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("test-key", "://nope"); err == nil {
		t.Fatal("New() accepted an invalid base URL")
	}
}

func TestBuildImagenRequest_MapsSizeToAspectRatio(t *testing.T) {
	cases := map[string]string{
		"1024x1024": "1:1",
		"1792x1024": "16:9",
		"1024x1792": "9:16",
		"640x480":   "", // unmapped
	}
	for size, want := range cases {
		got := buildImagenRequest(core.ImageRequest{Prompt: "x", Size: size})
		var ar string
		if got.Parameters != nil {
			ar = got.Parameters.AspectRatio
		}
		if ar != want {
			t.Errorf("size %q -> aspectRatio %q, want %q", size, ar, want)
		}
	}
}
