package maxtoken

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

func intPtr(v int) *int { return &v }

func testRequest(model string, messages ...string) *providers.Request {
	msgs := make([]providers.Message, len(messages))
	for i, m := range messages {
		msgs[i] = providers.Message{Role: "user", Content: m}
	}
	return &providers.Request{
		Model:    model,
		Messages: msgs,
	}
}

func initMaxToken(t *testing.T, config map[string]interface{}) *MaxToken {
	t.Helper()
	m := &MaxToken{}
	if err := m.Init(config); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return m
}

func TestMaxToken_MaxTokensEnforcement(t *testing.T) {
	m := initMaxToken(t, map[string]interface{}{"max_tokens": 100})

	t.Run("exceeds limit", func(t *testing.T) {
		req := testRequest("gpt-4", "hello")
		req.MaxTokens = intPtr(200)
		pctx := plugin.NewContext(req)

		if err := m.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !pctx.Reject {
			t.Error("expected request to be rejected")
		}
	})

	t.Run("within limit", func(t *testing.T) {
		req := testRequest("gpt-4", "hello")
		req.MaxTokens = intPtr(50)
		pctx := plugin.NewContext(req)

		if err := m.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if pctx.Reject {
			t.Error("expected request to be allowed")
		}
	})
}

func TestMaxToken_MaxMessagesEnforcement(t *testing.T) {
	m := initMaxToken(t, map[string]interface{}{"max_messages": 2})

	t.Run("exceeds limit", func(t *testing.T) {
		req := testRequest("gpt-4", "msg1", "msg2", "msg3")
		pctx := plugin.NewContext(req)

		if err := m.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !pctx.Reject {
			t.Error("expected request to be rejected")
		}
	})

	t.Run("within limit", func(t *testing.T) {
		req := testRequest("gpt-4", "msg1", "msg2")
		pctx := plugin.NewContext(req)

		if err := m.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if pctx.Reject {
			t.Error("expected request to be allowed")
		}
	})
}

func TestMaxToken_MaxInputLengthEnforcement(t *testing.T) {
	m := initMaxToken(t, map[string]interface{}{"max_input_length": 10})

	t.Run("exceeds limit", func(t *testing.T) {
		req := testRequest("gpt-4", "this is a long message")
		pctx := plugin.NewContext(req)

		if err := m.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !pctx.Reject {
			t.Error("expected request to be rejected")
		}
	})

	t.Run("within limit", func(t *testing.T) {
		req := testRequest("gpt-4", "short")
		pctx := plugin.NewContext(req)

		if err := m.Execute(context.Background(), pctx); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if pctx.Reject {
			t.Error("expected request to be allowed")
		}
	})
}

func TestMaxToken_AllowedRequestPassesThrough(t *testing.T) {
	m := initMaxToken(t, map[string]interface{}{})
	req := testRequest("gpt-4", "hello")
	req.MaxTokens = intPtr(100)
	pctx := plugin.NewContext(req)

	if err := m.Execute(context.Background(), pctx); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if pctx.Reject {
		t.Error("expected default config to allow request")
	}
}
