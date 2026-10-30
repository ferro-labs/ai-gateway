package cache

import (
	"context"
	"testing"

	cachestore "github.com/ferro-labs/ai-gateway/pkg/cache"
	"github.com/ferro-labs/ai-gateway/plugin"
)

func TestResponseCache_UnhashableRequestBypassesCache(t *testing.T) {
	c := initCache(t, nil)
	for range 2 {
		pctx := &plugin.Context{
			Request:  testRequest("test-model", "hello"),
			Metadata: make(map[string]any),
			Stage:    plugin.StageBeforeRequest,
		}
		pctx.Request.ToolChoice = make(chan int)
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatal(err)
		}
		if pctx.SkipProvider || pctx.Response != nil {
			t.Fatal("unhashable request used a cached response")
		}
		pctx.Stage = plugin.StageAfterRequest
		pctx.Response = testResponse()
		if err := c.Execute(context.Background(), pctx); err != nil {
			t.Fatal(err)
		}
		if c.Len() != 0 {
			t.Fatal("unhashable request was stored")
		}
	}
}

func TestResponseCache_UnhashableRequestStaysBypassed(t *testing.T) {
	c := initCache(t, nil)
	pctx := &plugin.Context{Request: testRequest("test-model", "hello"), Metadata: make(map[string]any), Stage: plugin.StageBeforeRequest}
	pctx.Request.ToolChoice = make(chan int)
	if err := c.Execute(context.Background(), pctx); err != nil {
		t.Fatal(err)
	}
	pctx.Request.ToolChoice = nil
	pctx.Stage = plugin.StageAfterRequest
	pctx.Response = testResponse()
	if err := c.Execute(context.Background(), pctx); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 0 {
		t.Fatal("previously unhashable request was stored")
	}
}

func BenchmarkCacheKey(b *testing.B) {
	req := multimodalToolRequest()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := cachestore.Fingerprint(req, "example-key-id"); err != nil {
			b.Fatal(err)
		}
	}
}
