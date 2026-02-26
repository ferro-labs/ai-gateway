package wordfilter

import (
	"context"
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

func initFilter(t *testing.T, config map[string]interface{}) *WordFilter {
	t.Helper()
	f := &WordFilter{}
	if err := f.Init(config); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return f
}

func TestWordFilter_Init(t *testing.T) {
	f := initFilter(t, map[string]interface{}{
		"blocked_words": []interface{}{"badword", "forbidden"},
	})
	if len(f.blockedWords) != 2 {
		t.Errorf("expected 2 blocked words, got %d", len(f.blockedWords))
	}
	if f.caseSensitive {
		t.Error("expected case_sensitive to default to false")
	}
}

func TestWordFilter_BlocksMessage(t *testing.T) {
	f := initFilter(t, map[string]interface{}{
		"blocked_words": []interface{}{"badword"},
	})
	pctx := plugin.NewContext(testRequest("this contains badword in it"))

	if err := f.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !pctx.Reject {
		t.Error("expected request to be rejected")
	}
	if pctx.Reason != "blocked word detected: badword" {
		t.Errorf("unexpected reason: %s", pctx.Reason)
	}
}

func TestWordFilter_AllowsCleanMessage(t *testing.T) {
	f := initFilter(t, map[string]interface{}{
		"blocked_words": []interface{}{"badword"},
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
	f := initFilter(t, map[string]interface{}{
		"blocked_words":  []interface{}{"BadWord"},
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

func TestWordFilter_CaseSensitive(t *testing.T) {
	f := initFilter(t, map[string]interface{}{
		"blocked_words":  []interface{}{"BadWord"},
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
