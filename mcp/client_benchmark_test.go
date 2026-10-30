package mcp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type benchmarkTransport struct{}

func (benchmarkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))}, nil
}

func BenchmarkClientListTools(b *testing.B) {
	c := NewClient("http://example.invalid", nil, 0)
	c.httpClient.Transport = benchmarkTransport{}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.ListTools(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
