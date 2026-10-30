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

func TestResponseCache_InvalidUTF8NamespaceBypassesCache(t *testing.T) {
	for _, field := range []string{"metadata key", "metadata value", "key ID"} {
		t.Run(field, func(t *testing.T) {
			c := initCache(t, nil)
			makeContext := func(value string) *plugin.Context {
				pctx := &plugin.Context{
					Request:  testRequest("test-model", "hello"),
					Metadata: map[string]any{"api_key": "key-id"},
					Stage:    plugin.StageBeforeRequest,
				}
				switch field {
				case "metadata key":
					pctx.Request.RoutingMetadata = map[string]string{value: "route"}
				case "metadata value":
					pctx.Request.RoutingMetadata = map[string]string{"route": value}
				case "key ID":
					pctx.Metadata["api_key"] = value
				}
				return pctx
			}
			// Distinct invalid bytes are both replaced during JSON encoding.
			store := makeContext(string([]byte{0xfe}))
			store.Stage = plugin.StageAfterRequest
			store.Response = testResponse()
			if err := c.Execute(context.Background(), store); err != nil {
				t.Fatal(err)
			}
			if c.Len() != 0 {
				t.Error("invalid UTF-8 namespace was stored")
			}
			lookup := makeContext(string([]byte{0xff}))
			if err := c.Execute(context.Background(), lookup); err != nil {
				t.Fatal(err)
			}
			if lookup.SkipProvider || lookup.Response != nil {
				t.Error("different invalid UTF-8 namespaces shared a response")
			}

			// Bypass must survive a later plugin repairing the namespace.
			c.Clear()
			repaired := makeContext("valid-route")
			lookup.Request = repaired.Request
			lookup.Metadata["api_key"] = repaired.Metadata["api_key"]
			lookup.Stage = plugin.StageAfterRequest
			lookup.Response = testResponse()
			lookup.SkipProvider = false
			if err := c.Execute(context.Background(), lookup); err != nil {
				t.Fatal(err)
			}
			if c.Len() != 0 {
				t.Error("repaired namespace lost the request's cache bypass")
			}

			// Valid Unicode, including the replacement character, remains cacheable.
			valid := makeContext("雪\ufffd")
			valid.Stage = plugin.StageAfterRequest
			valid.Response = testResponse()
			if err := c.Execute(context.Background(), valid); err != nil {
				t.Fatal(err)
			}
			hit := makeContext("雪\ufffd")
			if err := c.Execute(context.Background(), hit); err != nil {
				t.Fatal(err)
			}
			if !hit.SkipProvider {
				t.Error("valid Unicode namespace did not hit")
			}
		})
	}
}
