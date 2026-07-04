package replicate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

func TestNewReplicate(t *testing.T) {
	p, err := New("test-token", "", nil, nil)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if p.Name() != "replicate" {
		t.Errorf("Name() = %q, want replicate", p.Name())
	}
}

func TestReplicateProvider_SupportedModels_Defaults(t *testing.T) {
	p, _ := New("test-token", "", nil, nil)
	models := p.SupportedModels()
	if len(models) == 0 {
		t.Error("SupportedModels() returned empty")
	}
	found := false
	for _, m := range models {
		if strings.Contains(m, "llama") {
			found = true
		}
	}
	if !found {
		t.Error("no llama model found in default supported models")
	}
}

func TestReplicateProvider_SupportedModels_Custom(t *testing.T) {
	textModels := []string{"owner/text-model"}
	imageModels := []string{"owner/image-model"}
	p, _ := New("test-token", "", textModels, imageModels)
	models := p.SupportedModels()
	if len(models) != 2 {
		t.Fatalf("SupportedModels() returned %d, want 2", len(models))
	}
}

func TestReplicateProvider_SupportsModel(t *testing.T) {
	p, _ := New("test-token", "", []string{"meta/meta-llama-3.1-8b-instruct"}, nil)
	if !p.SupportsModel("meta/meta-llama-3.1-8b-instruct") {
		t.Error("expected meta-llama model to be supported")
	}
	if p.SupportsModel("unknown/model") {
		t.Error("unknown model should not be supported")
	}
}

func TestReplicateProvider_SupportsModel_WithVersion(t *testing.T) {
	p, _ := New("test-token", "", []string{"meta/model:abc123"}, nil)
	if !p.SupportsModel("meta/model") {
		t.Error("expected meta/model (without version) to match meta/model:abc123")
	}
}

func TestModelVersion(t *testing.T) {
	tests := []struct {
		path    string
		wantVer string
	}{
		{"owner/name", ""},
		{"owner/name:abc123", "abc123"},
		{"owner/name:sha256deadbeef", "sha256deadbeef"},
	}
	for _, tc := range tests {
		if got := ModelVersion(tc.path); got != tc.wantVer {
			t.Errorf("ModelVersion(%q) = %q, want %q", tc.path, got, tc.wantVer)
		}
	}
}

func TestReplicateProvider_Complete_PinnedVersion(t *testing.T) {
	// Verify that when the registered model has a version suffix, Complete()
	// sends the request to /predictions with a "version" field in the body,
	// instead of /models/{owner}/{name}/predictions.
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		pred := Prediction{ID: "pred-ver", Status: "succeeded", Output: "ok"}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	const versionedModel = "meta/llama:abc123"
	p, _ := New("test-token", srv.URL, []string{versionedModel}, nil)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "meta/llama",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if gotPath != "/predictions" {
		t.Errorf("request path = %q, want /predictions", gotPath)
	}
	if gotBody["version"] != "abc123" {
		t.Errorf("body[\"version\"] = %v, want abc123", gotBody["version"])
	}
	if _, ok := gotBody["input"]; !ok {
		t.Error("body missing \"input\" field")
	}
}

func TestReplicateProvider_GenerateImage_PinnedVersion(t *testing.T) {
	// Same check as above but for GenerateImage().
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		pred := Prediction{ID: "img-ver", Status: "succeeded", Output: []any{"https://example.com/img.png"}}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	const versionedModel = "black-forest-labs/flux-schnell:deadbeef"
	p, _ := New("test-token", srv.URL, nil, []string{versionedModel})
	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "black-forest-labs/flux-schnell",
		Prompt: "A cat",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if gotPath != "/predictions" {
		t.Errorf("request path = %q, want /predictions", gotPath)
	}
	if gotBody["version"] != "deadbeef" {
		t.Errorf("body[\"version\"] = %v, want deadbeef", gotBody["version"])
	}
}

func TestReplicateProvider_Complete_NoVersion_UsesModelPath(t *testing.T) {
	// When no version is present the URL must be /models/{owner}/{name}/predictions.
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		pred := Prediction{ID: "pred-nover", Status: "succeeded", Output: "ok"}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, _ := New("test-token", srv.URL, []string{"meta/llama"}, nil)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "meta/llama",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if gotPath != "/models/meta/llama/predictions" {
		t.Errorf("request path = %q, want /models/meta/llama/predictions", gotPath)
	}
}

func TestReplicateProvider_Models(t *testing.T) {
	p, _ := New("test-token", "", nil, nil)
	models := p.Models()
	for _, m := range models {
		if m.OwnedBy != "replicate" {
			t.Errorf("ModelInfo.OwnedBy = %q, want replicate", m.OwnedBy)
		}
	}
}

func TestReplicateProvider_AuthHeaders(t *testing.T) {
	p, _ := New("test-token", "", nil, nil)
	headers := p.AuthHeaders()
	if headers["Authorization"] != "Bearer test-token" {
		t.Errorf("AuthHeaders Authorization = %q, want Bearer test-token", headers["Authorization"])
	}
}

func TestReplicateProvider_Complete_MockHTTP(t *testing.T) {
	// Mock Replicate: first POST creates the prediction, returns succeeded immediately.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		pred := Prediction{
			ID:     "pred-123",
			Status: "succeeded",
			Output: []any{"Hello", " world"},
		}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, _ := New("test-token", srv.URL, []string{"meta/meta-llama-3.1-8b-instruct"}, nil)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "meta/meta-llama-3.1-8b-instruct",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.ID != "pred-123" {
		t.Errorf("Response.ID = %q, want pred-123", resp.ID)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if resp.Choices[0].Message.Content != "Hello world" {
		t.Errorf("content = %q, want 'Hello world'", resp.Choices[0].Message.Content)
	}
}

func TestReplicateProvider_CompleteStream_Interface(_ *testing.T) {
	p, _ := New("test-token", "", nil, nil)
	var _ core.StreamProvider = p
}

func TestReplicateProvider_CompleteStream_MockSSE(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotAccept string
	var gotBody map[string]any

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models/test/model/predictions":
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatalf("decode prediction request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{
				"id":"pred-stream",
				"status":"starting",
				"urls":{"stream":"` + srv.URL + `/stream"}
			}`))
		case "/stream":
			gotAccept = r.Header.Get("Accept")
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("event: output\nid: 1\ndata: Hello\n\n" +
				"event: output\nid: 2\ndata:  world\n\n" +
				"event: done\ndata: {}\n\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p, _ := New("test-token", srv.URL, []string{"test/model"}, nil)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "test/model",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		if c.Error != nil {
			t.Fatalf("stream chunk error: %v", c.Error)
		}
		chunks = append(chunks, c)
	}

	if gotPath != "/models/test/model/predictions" {
		t.Fatalf("request path = %q, want /models/test/model/predictions", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("authorization = %q, want Bearer test-token", gotAuth)
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("stream Accept = %q, want text/event-stream", gotAccept)
	}
	if gotBody["stream"] != true {
		t.Fatalf("body[\"stream\"] = %v, want true", gotBody["stream"])
	}
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3: %+v", len(chunks), chunks)
	}
	for i, chunk := range chunks {
		if chunk.ID != "pred-stream" {
			t.Fatalf("chunk %d ID = %q, want pred-stream", i, chunk.ID)
		}
	}
	if chunks[0].Choices[0].Delta.Content != "Hello" {
		t.Fatalf("first chunk content = %q, want Hello", chunks[0].Choices[0].Delta.Content)
	}
	if chunks[1].Choices[0].Delta.Content != " world" {
		t.Fatalf("second chunk content = %q, want ' world'", chunks[1].Choices[0].Delta.Content)
	}
	if chunks[2].Choices[0].FinishReason != "stop" {
		t.Fatalf("final finish_reason = %q, want stop", chunks[2].Choices[0].FinishReason)
	}
}

func TestReplicateProvider_GenerateImage_MockHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pred := Prediction{
			ID:     "img-pred-1",
			Status: "succeeded",
			Output: []any{"https://example.com/image.png"},
		}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, _ := New("test-token", srv.URL, nil, []string{"black-forest-labs/flux-schnell"})
	resp, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "black-forest-labs/flux-schnell",
		Prompt: "A robot",
		Size:   "1024x1024",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error: %v", err)
	}
	if len(resp.Data) == 0 {
		t.Fatal("expected at least one image")
	}
	if resp.Data[0].URL != "https://example.com/image.png" {
		t.Errorf("image URL = %q, want https://example.com/image.png", resp.Data[0].URL)
	}
}

func TestReplicateProvider_Complete_PollingBehavior(t *testing.T) {
	// First call: prediction is "processing", second call (poll): "succeeded"
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		var pred Prediction
		if callCount == 1 {
			// Initial submission: 201 Created with processing status.
			pred = Prediction{ID: "pred-poll", Status: "processing"}
			w.WriteHeader(http.StatusCreated)
		} else {
			// Poll request: 200 OK with succeeded status.
			pred = Prediction{ID: "pred-poll", Status: "succeeded", Output: "text result"}
			w.WriteHeader(http.StatusOK)
		}
		data, _ := json.Marshal(pred)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, _ := New("test-token", srv.URL, []string{"test/model"}, nil)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "test/model",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() polling error: %v", err)
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 calls (submit + poll), got %d", callCount)
	}
	if resp.Choices[0].Message.Content != "text result" {
		t.Errorf("polled content = %q, want 'text result'", resp.Choices[0].Message.Content)
	}
}

// ── GenerateImage size validation ─────────────────────────────────────────────

func TestReplicateProvider_GenerateImage_InvalidSize(t *testing.T) {
	p, _ := New("test-token", "http://unused", nil, []string{"owner/model"})

	badSizes := []string{
		"1024",        // only one dimension
		"axb",         // non-integer
		"0x1024",      // zero width
		"1024x0",      // zero height
		"-512x512",    // negative width
		"512x-512",    // negative height
		"1024 x 1024", // spaces instead of 'x'
		"",            // empty is fine (skipped) — not tested here, see valid test
	}
	for _, size := range badSizes {
		if size == "" {
			continue
		}
		_, err := p.GenerateImage(context.Background(), core.ImageRequest{
			Model:  "owner/model",
			Prompt: "A robot",
			Size:   size,
		})
		if err == nil {
			t.Errorf("GenerateImage() with Size=%q: expected error, got nil", size)
		}
		if !strings.Contains(err.Error(), size) {
			t.Errorf("error for size %q should mention the bad value; got: %v", size, err)
		}
	}
}

func TestReplicateProvider_GenerateImage_ValidSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pred := Prediction{ID: "img-sz", Status: "succeeded", Output: []any{"https://example.com/img.png"}}
		data, _ := json.Marshal(pred)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	p, _ := New("test-token", srv.URL, nil, []string{"owner/model"})
	_, err := p.GenerateImage(context.Background(), core.ImageRequest{
		Model:  "owner/model",
		Prompt: "A robot",
		Size:   "1024x1024",
	})
	if err != nil {
		t.Fatalf("GenerateImage() with valid size: unexpected error: %v", err)
	}
}

// ── Poll loop error handling ───────────────────────────────────────────────────

func TestReplicateProvider_Poll_NonOKStatus(t *testing.T) {
	// First call: submit — returns processing. Second call: poll — returns 429.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		if callCount == 1 {
			pred := Prediction{ID: "pred-poll-err", Status: "processing"}
			data, _ := json.Marshal(pred)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(data)
			return
		}
		// Poll response: non-200 error.
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"detail":"rate limited"}`))
	}))
	defer srv.Close()

	p, _ := New("test-token", srv.URL, []string{"test/model"}, nil)
	_, err := p.Complete(context.Background(), core.Request{
		Model:    "test/model",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error from non-200 poll response, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention HTTP status 429; got: %v", err)
	}
}

// ── Submit error paths (4xx / 5xx) ─────────────────────────────────────────────

func TestReplicateProvider_Complete_SubmitError(t *testing.T) {
	// The initial POST fails; submitAndPoll must surface a core.APIError that
	// embeds the status code and the {"detail":…} message Replicate returns.
	cases := []struct {
		name       string
		status     int
		body       string
		wantSubstr string
	}{
		{"bad request", http.StatusBadRequest, `{"detail":"invalid input"}`, "invalid input"},
		{"server error", http.StatusInternalServerError, `{"detail":"internal"}`, "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			p, _ := New("test-token", srv.URL, []string{"test/model"}, nil)
			_, err := p.Complete(context.Background(), core.Request{
				Model:    "test/model",
				Messages: []core.Message{{Role: "user", Content: "Hi"}},
			})
			if err == nil {
				t.Fatalf("expected error for status %d, got nil", tc.status)
			}
			if got := core.ParseStatusCode(err); got != tc.status {
				t.Errorf("ParseStatusCode = %d, want %d (err: %v)", got, tc.status, err)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error should contain %q; got: %v", tc.wantSubstr, err)
			}
		})
	}
}

func TestReplicateProvider_CompleteStream_SubmitError(t *testing.T) {
	// The streaming path's initial submit POST fails; the error must be a
	// core.APIError carrying the status code.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"stream submit rejected"}`))
	}))
	defer srv.Close()

	p, _ := New("test-token", srv.URL, []string{"test/model"}, nil)
	_, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "test/model",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected submit error from CompleteStream, got nil")
	}
	if core.ParseStatusCode(err) != http.StatusBadRequest {
		t.Errorf("expected status 400 in error; got: %v", err)
	}
	if !strings.Contains(err.Error(), "stream submit rejected") {
		t.Errorf("error should carry the detail message; got: %v", err)
	}
}

// ── Usage + finish_reason ──────────────────────────────────────────────────────

func TestReplicateProvider_Complete_UsageAndFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id":"pred-usage",
			"status":"succeeded",
			"output":"hello",
			"metrics":{"input_token_count":12,"output_token_count":7}
		}`))
	}))
	defer srv.Close()

	p, _ := New("test-token", srv.URL, []string{"test/model"}, nil)
	resp, err := p.Complete(context.Background(), core.Request{
		Model:    "test/model",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 7 || resp.Usage.TotalTokens != 19 {
		t.Errorf("usage = %+v, want prompt=12 completion=7 total=19", resp.Usage)
	}
	if resp.Choices[0].FinishReason != core.FinishReasonStop {
		t.Errorf("finish_reason = %q, want %q", resp.Choices[0].FinishReason, core.FinishReasonStop)
	}
}

// ── Streaming: error event + done reason ───────────────────────────────────────

func TestReplicateProvider_CompleteStream_ErrorEvent(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models/test/model/predictions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"pred-err","status":"starting","urls":{"stream":"` + srv.URL + `/stream"}}`))
		case "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("event: output\ndata: partial\n\n" +
				"event: error\ndata: model exploded\n\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p, _ := New("test-token", srv.URL, []string{"test/model"}, nil)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "test/model",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var streamErr error
	for c := range ch {
		if c.Error != nil {
			streamErr = c.Error
		}
	}
	if streamErr == nil {
		t.Fatal("expected an error chunk from the error event")
	}
	if !strings.Contains(streamErr.Error(), "model exploded") {
		t.Errorf("error chunk should carry the payload; got: %v", streamErr)
	}
}

func TestReplicateProvider_CompleteStream_DoneReason(t *testing.T) {
	// A "done" event carrying a reason must normalize into the terminal chunk's
	// finish_reason (max_tokens → length), not be treated as an error.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models/test/model/predictions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"pred-done","status":"starting","urls":{"stream":"` + srv.URL + `/stream"}}`))
		case "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("event: output\ndata: hi\n\n" +
				"event: done\ndata: {\"reason\":\"max_tokens\"}\n\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p, _ := New("test-token", srv.URL, []string{"test/model"}, nil)
	ch, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "test/model",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var chunks []core.StreamChunk
	for c := range ch {
		if c.Error != nil {
			t.Fatalf("unexpected error chunk: %v", c.Error)
		}
		chunks = append(chunks, c)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	last := chunks[len(chunks)-1]
	if last.Choices[0].FinishReason != core.FinishReasonLength {
		t.Errorf("terminal finish_reason = %q, want %q", last.Choices[0].FinishReason, core.FinishReasonLength)
	}
}

// ── Context cancellation ───────────────────────────────────────────────────────

func TestReplicateProvider_Poll_ContextCancel(t *testing.T) {
	// The prediction never resolves; canceling the context mid-poll must return
	// promptly with a context error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"pred-hang","status":"processing"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after submit succeeds so the poll loop observes the cancellation.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	p, _ := New("test-token", srv.URL, []string{"test/model"}, nil)
	_, err := p.Complete(ctx, core.Request{
		Model:    "test/model",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from poll loop, got: %v", err)
	}
}

func TestReplicateProvider_Stream_ContextCancel(t *testing.T) {
	// The stream never ends; canceling the context must terminate the read loop
	// with a context-related error chunk.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models/test/model/predictions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"pred-stream-cancel","status":"starting","urls":{"stream":"` + srv.URL + `/stream"}}`))
		case "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("stream response writer is not a Flusher")
			}
			// Emit output events until the client disconnects.
			for i := 0; i < 100000; i++ {
				select {
				case <-r.Context().Done():
					return
				default:
				}
				if _, err := w.Write([]byte("event: output\ndata: x\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, _ := New("test-token", srv.URL, []string{"test/model"}, nil)
	ch, err := p.CompleteStream(ctx, core.Request{
		Model:    "test/model",
		Messages: []core.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("CompleteStream() error: %v", err)
	}

	var cancelErr error
	count := 0
	for c := range ch {
		count++
		if count == 1 {
			cancel()
		}
		if c.Error != nil {
			cancelErr = c.Error
			break
		}
	}
	if cancelErr == nil {
		t.Fatal("expected an error chunk after context cancellation")
	}
	if !errors.Is(cancelErr, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", cancelErr)
	}
}

// TestNewReplicate_RejectsInvalidBaseURL locks in base-URL validation.
func TestNewReplicate_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := New("k", "://nope", nil, nil); err == nil {
		t.Fatal("New accepted an invalid base URL")
	}
}

// TestResolveModelURL_EscapesModelPath locks in that special characters in a
// model path are percent-escaped so they cannot alter the request URL.
func TestResolveModelURL_EscapesModelPath(t *testing.T) {
	p, err := New("k", "https://api.replicate.com/v1", nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, _, err := p.resolveModelURL("owner/bad#name")
	if err != nil {
		t.Fatalf("resolveModelURL: %v", err)
	}
	want := "https://api.replicate.com/v1/models/owner/bad%23name/predictions"
	if got != want {
		t.Errorf("resolveModelURL = %q, want %q", got, want)
	}
}

// TestResolveModelURL_RejectsTraversal locks in that dot segments are rejected
// rather than escaping to a different path.
func TestResolveModelURL_RejectsTraversal(t *testing.T) {
	p, _ := New("k", "https://api.replicate.com/v1", nil, nil)
	if _, _, err := p.resolveModelURL("../../etc"); err == nil {
		t.Fatal("resolveModelURL accepted a traversal model path, want error")
	}
}

// TestResolveModelURL_RejectsNonOwnerName locks in that only an owner/name model
// path builds a /models/ URL; missing or extra segments are rejected.
func TestResolveModelURL_RejectsNonOwnerName(t *testing.T) {
	p, _ := New("k", "https://api.replicate.com/v1", nil, nil)
	for _, m := range []string{"solo", "owner/name/extra", "a/b/c/d"} {
		if _, _, err := p.resolveModelURL(m); err == nil {
			t.Errorf("resolveModelURL(%q) accepted a non owner/name path, want error", m)
		}
	}
}
