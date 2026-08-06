package wordfilter

import (
	"context"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func testRequest(content string) *providers.Request {
	return &providers.Request{
		Model: "gpt-4",
		Messages: []providers.Message{
			{Role: "user", Content: content},
		},
	}
}

func initFilter(t *testing.T, config map[string]any) *WordFilter {
	t.Helper()
	f := &WordFilter{}
	if err := f.Init(config); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return f
}

func TestWordFilter_Init(t *testing.T) {
	f := initFilter(t, map[string]any{
		"blocked_words": []any{"badword", "forbidden"},
	})
	if len(f.blockedWords) != 2 {
		t.Errorf("expected 2 blocked words, got %d", len(f.blockedWords))
	}
	if f.caseSensitive {
		t.Error("expected case_sensitive to default to false")
	}
}

func TestWordFilter_BlocksMessage(t *testing.T) {
	f := initFilter(t, map[string]any{
		"blocked_words": []any{"badword"},
	})
	pctx := plugin.NewContext(testRequest("this contains badword in it"))

	if err := f.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Error("expected request to be rejected")
	}
	if pctx.Reason != "request blocked by content policy" {
		t.Errorf("unexpected reason: %q", pctx.Reason)
	}
	if strings.Contains(pctx.Reason, "badword") {
		t.Errorf("rejection reason must not echo the blocked word: %q", pctx.Reason)
	}
}

func TestWordFilter_AllowsCleanMessage(t *testing.T) {
	f := initFilter(t, map[string]any{
		"blocked_words": []any{"badword"},
	})
	pctx := plugin.NewContext(testRequest("this is a clean message"))

	if err := f.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Error("expected request to be allowed")
	}
}

func TestWordFilter_CaseInsensitive(t *testing.T) {
	f := initFilter(t, map[string]any{
		"blocked_words":  []any{"BadWord"},
		"case_sensitive": false,
	})
	pctx := plugin.NewContext(testRequest("this has BADWORD in it"))

	if err := f.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Error("expected case-insensitive match to reject")
	}
}

// TestWordFilter_ReasonDoesNotLeakBlockedWord asserts that the rejection
// reason exposed to the caller is a generic policy message and does NOT
// contain the operator-configured blocked word.
func TestWordFilter_ReasonDoesNotLeakBlockedWord(t *testing.T) {
	f := initFilter(t, map[string]any{
		"blocked_words": []any{"supersecretword"},
	})
	pctx := plugin.NewContext(testRequest("this message contains supersecretword inside"))

	if err := f.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Fatal("expected request to be rejected")
	}
	if strings.Contains(pctx.Reason, "supersecretword") {
		t.Errorf("rejection reason leaks blocked word to caller: %q", pctx.Reason)
	}
	if pctx.Reason == "" {
		t.Error("rejection reason must not be empty")
	}
}

func TestWordFilter_CaseSensitive(t *testing.T) {
	f := initFilter(t, map[string]any{
		"blocked_words":  []any{"BadWord"},
		"case_sensitive": true,
	})

	t.Run("no match", func(t *testing.T) {
		pctx := plugin.NewContext(testRequest("this has badword in it"))
		if err := f.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if pctx.Reject {
			t.Error("expected case-sensitive mismatch to allow")
		}
	})

	t.Run("exact match", func(t *testing.T) {
		pctx := plugin.NewContext(testRequest("this has BadWord in it"))
		if err := f.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !pctx.Reject {
			t.Error("expected case-sensitive exact match to reject")
		}
	})
}

// TestWordFilter_ScreensContentParts covers the text the filter used to skip.
//
// Message.UnmarshalJSON collapses only parts typed "text" into Content, and
// providers forward every part verbatim — so a blocked word carried by a part
// of any other type, or by a message built in Go rather than decoded from
// JSON, reached the provider with a 200 while the same word in Content was a
// 400.
func TestWordFilter_ScreensContentParts(t *testing.T) {
	f := initFilter(t, map[string]any{"blocked_words": []any{"badword"}})

	tests := []struct {
		name    string
		message providers.Message
		reject  bool
	}{
		{
			name: "non-text part type never collapses into Content",
			message: providers.Message{
				Role:    "user",
				Content: "describe this",
				ContentParts: []providers.ContentPart{
					{Type: "text", Text: "describe this"},
					{Type: "input_text", Text: "and also badword"},
				},
			},
			reject: true,
		},
		{
			name: "text part with no collapsed Content",
			message: providers.Message{
				Role:         "user",
				ContentParts: []providers.ContentPart{{Type: "text", Text: "badword"}},
			},
			reject: true,
		},
		{
			name: "clean parts still pass",
			message: providers.Message{
				Role:    "user",
				Content: "describe this",
				ContentParts: []providers.ContentPart{
					{Type: "text", Text: "describe this"},
					{Type: "image_url", ImageURL: &providers.ImageURLPart{URL: "https://example.com/cat.jpg"}},
				},
			},
			reject: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pctx := plugin.NewContext(&providers.Request{
				Model:    "gpt-4",
				Messages: []providers.Message{tt.message},
			})
			if err := f.Execute(context.Background(), pctx); err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if pctx.Reject != tt.reject {
				t.Errorf("Reject = %v, want %v", pctx.Reject, tt.reject)
			}
		})
	}
}

// TestWordFilter_ScreensResponseContentParts holds the after_request stage to
// the same rule: a response is screened through the same walk, so a provider
// that answers in parts is not a way past the blocklist.
func TestWordFilter_ScreensResponseContentParts(t *testing.T) {
	f := initFilter(t, map[string]any{"blocked_words": []any{"badword"}})

	pctx := plugin.NewContext(testRequest("clean prompt"))
	pctx.Stage = plugin.StageAfterRequest
	pctx.Response = &providers.Response{
		Choices: []providers.Choice{{
			Message: providers.Message{
				Role:         "assistant",
				ContentParts: []providers.ContentPart{{Type: "output_text", Text: "here is a badword"}},
			},
		}},
	}

	if err := f.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Error("expected a blocked word in a response content part to be rejected")
	}
}

// TestWordFilter_MatchesSubstringNotWholeWord pins the matcher the catalog
// summary describes. A blocklist that only matched whole words would be evaded
// by concatenation, which is why the summary was corrected to the matcher
// rather than the matcher loosened to the summary.
func TestWordFilter_MatchesSubstringNotWholeWord(t *testing.T) {
	f := initFilter(t, map[string]any{"blocked_words": []any{"badword"}})
	pctx := plugin.NewContext(testRequest("prefixbadwordsuffix"))

	if err := f.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Error("expected a blocked entry embedded in a longer word to be rejected")
	}
}

// Content the gateway could not read as text — an embeddings input sent as
// token IDs — is content this filter was REQUIRED to screen and could not.
// Serving it would leave the blocklist evadable by one tokenizer call on the
// client, so it is denied. It is a rejection (a verdict, 4xx), never a returned
// error (breakage, 500).
func TestWordFilter_RejectsUninspectableContent(t *testing.T) {
	tests := []struct {
		name          string
		blockedWords  []any
		stage         plugin.Stage
		uninspectable bool
		wantReject    bool
	}{
		{
			name:          "guardrail configured and content unreadable",
			blockedWords:  []any{"badword"},
			stage:         plugin.StageBeforeRequest,
			uninspectable: true,
			wantReject:    true,
		},
		{
			// The scope of the refusal: a deployment that configured no blocked
			// words asked for no inspection, so nothing was required and nothing
			// is refused. Token-id embeddings keep working exactly as before.
			name:          "no blocked words configured",
			stage:         plugin.StageBeforeRequest,
			uninspectable: true,
		},
		{
			name:         "readable content is screened as usual",
			blockedWords: []any{"badword"},
			stage:        plugin.StageBeforeRequest,
		},
		{
			// after_request screens the RESPONSE. The request's content being
			// unreadable says nothing about what came back, and the provider has
			// already been called, so denying there would report a violation
			// nobody committed.
			name:          "after_request is about the response",
			blockedWords:  []any{"badword"},
			stage:         plugin.StageAfterRequest,
			uninspectable: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := map[string]any{}
			if tc.blockedWords != nil {
				config["blocked_words"] = tc.blockedWords
			}
			f := initFilter(t, config)

			pctx := plugin.NewContext(testRequest("a clean message"))
			defer plugin.PutContext(pctx)
			pctx.Stage = tc.stage
			pctx.Response = &providers.Response{}
			if tc.uninspectable {
				pctx.Metadata[plugin.MetadataUninspectableContent] = true
			}

			if err := f.Execute(context.Background(), pctx); err != nil {
				t.Fatalf("Execute returned an error, which is a 500: %v", err)
			}
			if pctx.Reject != tc.wantReject {
				t.Fatalf("Reject = %v, want %v", pctx.Reject, tc.wantReject)
			}
			if tc.wantReject && strings.Contains(pctx.Reason, "badword") {
				t.Errorf("rejection reason leaks the blocklist: %q", pctx.Reason)
			}
		})
	}
}
