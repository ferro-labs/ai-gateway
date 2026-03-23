package transport

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDefaultStreamingConfig(t *testing.T) {
	cfg := DefaultStreamingConfig()
	if cfg.IdleTimeout == 0 {
		t.Error("IdleTimeout must be set")
	}
	if cfg.ReadBufferSize == 0 {
		t.Error("ReadBufferSize must be set")
	}
	if cfg.WriteBufferSize == 0 {
		t.Error("WriteBufferSize must be set")
	}
	if cfg.MaxIdleConnsPerHost == 0 {
		t.Error("MaxIdleConnsPerHost must be set")
	}
}

func TestNewStreamTransport(t *testing.T) {
	base := DefaultConfig()
	sse := DefaultStreamingConfig()
	st := NewStreamTransport(base, sse)

	if st.Client() == nil {
		t.Fatal("Client() must not be nil")
	}

	// Verify no ResponseHeaderTimeout on streaming transport.
	transport := st.Client().Transport.(*http.Transport)
	if transport.ResponseHeaderTimeout != 0 {
		t.Errorf("streaming ResponseHeaderTimeout = %v, want 0", transport.ResponseHeaderTimeout)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Error("streaming ForceAttemptHTTP2 = false, want true")
	}
}

func TestStreamTransport_ReaderPool(t *testing.T) {
	st := NewStreamTransport(DefaultConfig(), DefaultStreamingConfig())

	data := "data: {\"id\":\"test\"}\n\ndata: [DONE]\n\n"
	reader := strings.NewReader(data)

	br := st.GetReader(reader)
	if br == nil {
		t.Fatal("GetReader must not return nil")
	}

	// Read all data through the buffered reader.
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if string(got) != data {
		t.Errorf("got %q, want %q", got, data)
	}

	// Return to pool and get again — should be reset.
	st.PutReader(br)
	br2 := st.GetReader(strings.NewReader("more"))
	got2, _ := io.ReadAll(br2)
	if string(got2) != "more" {
		t.Errorf("recycled reader got %q, want %q", got2, "more")
	}
	st.PutReader(br2)
}

func TestStreamTransport_WriterPool(t *testing.T) {
	st := NewStreamTransport(DefaultConfig(), DefaultStreamingConfig())

	var buf bytes.Buffer
	bw := st.GetWriter(&buf)
	if bw == nil {
		t.Fatal("GetWriter must not return nil")
	}

	_, _ = bw.WriteString("data: test\n\n")
	_ = bw.Flush()
	if buf.String() != "data: test\n\n" {
		t.Errorf("got %q, want %q", buf.String(), "data: test\n\n")
	}

	// Return and reuse.
	st.PutWriter(bw)
	var buf2 bytes.Buffer
	bw2 := st.GetWriter(&buf2)
	_, _ = bw2.WriteString("data: chunk2\n\n")
	_ = bw2.Flush()
	if buf2.String() != "data: chunk2\n\n" {
		t.Errorf("recycled writer got %q, want %q", buf2.String(), "data: chunk2\n\n")
	}
	st.PutWriter(bw2)
}

func TestStreamTransport_CloseIdleConnections(_ *testing.T) {
	st := NewStreamTransport(DefaultConfig(), DefaultStreamingConfig())
	// Should not panic.
	st.CloseIdleConnections()
}

func BenchmarkStreamTransport_ReaderPool(b *testing.B) {
	st := NewStreamTransport(DefaultConfig(), DefaultStreamingConfig())
	data := strings.Repeat("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n", 10)

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			br := st.GetReader(strings.NewReader(data))
			_, _ = io.Copy(io.Discard, br)
			st.PutReader(br)
		}
	})
}

func BenchmarkStreamTransport_WriterPool(b *testing.B) {
	st := NewStreamTransport(DefaultConfig(), DefaultStreamingConfig())

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			bw := st.GetWriter(io.Discard)
			_, _ = bw.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
			_ = bw.Flush()
			st.PutWriter(bw)
		}
	})
}

func TestIsStreamingRequest(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"stream true", `{"model":"gpt-4o","stream":true}`, true},
		{"stream true spaces", `{"model":"gpt-4o","stream": true}`, true},
		{"stream true newline", "{\"stream\":\n  true}", true},
		{"stream false", `{"model":"gpt-4o","stream":false}`, false},
		{"no stream field", `{"model":"gpt-4o"}`, false},
		{"empty body", `{}`, false},
		{"stream in value", `{"model":"stream-model"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsStreamingRequest([]byte(tc.body))
			if got != tc.want {
				t.Errorf("IsStreamingRequest(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func BenchmarkIsStreamingRequest(b *testing.B) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":100}`)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = IsStreamingRequest(body)
		}
	})
}
