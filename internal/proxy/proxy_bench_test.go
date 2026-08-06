package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"testing"
)

func BenchmarkExtractTopLevelModel(b *testing.B) {
	body := []byte(`{
		"messages":[
			{"role":"system","content":"You are a routing benchmark."},
			{"role":"user","content":"Find the best provider for this request."}
		],
		"metadata":{
			"tenant":"bench",
			"tags":["proxy","model-scan"],
			"nested":{"a":[1,2,3],"b":{"c":"d","e":["x","y","z"]}}
		},
		"tools":[
			{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}
		],
		"model":"gpt-4o",
		"stream":true
	}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := &http.Request{
			Method:        http.MethodPost,
			URL:           &url.URL{Path: "/v1/chat/completions"},
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}
		model, err := ExtractTopLevelModel(req)
		if err != nil {
			b.Fatal(err)
		}
		if model != "gpt-4o" {
			b.Fatalf("model = %q, want gpt-4o", model)
		}
	}
}

func BenchmarkResolveProvider_ModelInBody(b *testing.B) {
	reg := buildTestRegistry(b, "http://localhost")
	body := []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"world"}
		],
		"metadata":{"tenant":"bench","trace_id":"trace-123"},
		"model":"gpt-4o",
		"stream":false
	}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := &http.Request{
			Method:        http.MethodPost,
			URL:           &url.URL{Path: "/v1/files"},
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}
		p, _, ok := ResolveProvider(req, reg)
		if !ok {
			b.Fatal("ResolveProvider() returned false")
		}
		if p.Name() != providerOpenAI {
			b.Fatalf("provider = %q, want %q", p.Name(), providerOpenAI)
		}
	}
}
