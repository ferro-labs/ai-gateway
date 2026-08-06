package maxtoken

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers"
)

// A batch embedding is projected to one message per document, so a
// conversation-turn ceiling must not refuse it. The size limits still apply.
func TestMaxToken_MessageCountIsChatOnly(t *testing.T) {
	m := &MaxToken{}
	if err := m.Init(map[string]any{"max_messages": 10}); err != nil {
		t.Fatalf("init: %v", err)
	}
	msgs := make([]providers.Message, 150)
	for i := range msgs {
		msgs[i] = providers.Message{Role: "user", Content: "doc"}
	}

	for _, tc := range []struct {
		name       string
		surface    string
		wantReject bool
	}{
		{"chat request over the ceiling is refused", "", true},
		{"projected embeddings batch is not", "embeddings", false},
		{"projected image request is not", "images", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := providers.Request{Messages: msgs}
			pctx := &plugin.Context{Request: &req, Metadata: map[string]any{}}
			if tc.surface != "" {
				pctx.Metadata[plugin.MetadataSurface] = tc.surface
			}
			if err := m.Execute(context.Background(), pctx); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if pctx.Reject != tc.wantReject {
				t.Errorf("Reject = %v, want %v (reason %q)", pctx.Reject, tc.wantReject, pctx.Reason)
			}
		})
	}
}
