package transport

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MaxIdleConnsPerHost != 100 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 100", cfg.MaxIdleConnsPerHost)
	}
	if cfg.MaxIdleConns != 1000 {
		t.Errorf("MaxIdleConns = %d, want 1000", cfg.MaxIdleConns)
	}
	if !cfg.ForceHTTP2 {
		t.Error("ForceHTTP2 = false, want true")
	}

	// Streaming client must have no ResponseHeaderTimeout.
	m := NewDefault()
	st := m.streamClient.Transport.(*http.Transport)
	if st.ResponseHeaderTimeout != 0 {
		t.Errorf("streaming ResponseHeaderTimeout = %v, want 0", st.ResponseHeaderTimeout)
	}

	// Default client must have ResponseHeaderTimeout set.
	dt := m.defaultClient.Transport.(*http.Transport)
	if dt.ResponseHeaderTimeout != cfg.ResponseHeaderTimeout {
		t.Errorf("default ResponseHeaderTimeout = %v, want %v", dt.ResponseHeaderTimeout, cfg.ResponseHeaderTimeout)
	}
}

func TestForProvider_Isolation(t *testing.T) {
	m := NewDefault()

	openaiCfg := DefaultConfig()
	openaiCfg.MaxIdleConnsPerHost = 50
	m.RegisterProvider("openai", openaiCfg)

	anthropicCfg := DefaultConfig()
	anthropicCfg.MaxIdleConnsPerHost = 30
	m.RegisterProvider("anthropic", anthropicCfg)

	oaiClient := m.ForProvider("openai")
	antClient := m.ForProvider("anthropic")
	defClient := m.ForProvider("unknown-provider")

	if oaiClient == antClient {
		t.Error("openai and anthropic clients must be different instances")
	}
	if defClient != m.defaultClient {
		t.Error("unregistered provider must return defaultClient")
	}
	if oaiClient == m.defaultClient {
		t.Error("registered provider must NOT return defaultClient")
	}

	// Verify transport configs are isolated.
	oaiTransport := oaiClient.Transport.(*http.Transport)
	antTransport := antClient.Transport.(*http.Transport)
	if oaiTransport.MaxIdleConnsPerHost != 50 {
		t.Errorf("openai MaxIdleConnsPerHost = %d, want 50", oaiTransport.MaxIdleConnsPerHost)
	}
	if antTransport.MaxIdleConnsPerHost != 30 {
		t.Errorf("anthropic MaxIdleConnsPerHost = %d, want 30", antTransport.MaxIdleConnsPerHost)
	}
}

func TestForStreaming(t *testing.T) {
	m := NewDefault()
	sc := m.ForStreaming("any-provider")

	transport, ok := sc.Transport.(*http.Transport)
	if !ok {
		t.Fatal("streaming client transport is not *http.Transport")
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Errorf("streaming ResponseHeaderTimeout = %v, want 0", transport.ResponseHeaderTimeout)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Error("streaming ForceAttemptHTTP2 = false, want true")
	}
}

func TestBufferPool(t *testing.T) {
	buf := BufferPool.Get()
	if buf.Len() != 0 {
		t.Errorf("fresh buffer Len = %d, want 0", buf.Len())
	}

	initialCap := buf.Cap()
	buf.WriteString("hello world")
	if buf.Len() != 11 {
		t.Errorf("after write Len = %d, want 11", buf.Len())
	}

	BufferPool.Put(buf)

	// Get again — should be reset but cap preserved.
	buf2 := BufferPool.Get()
	if buf2.Len() != 0 {
		t.Errorf("recycled buffer Len = %d, want 0", buf2.Len())
	}
	if buf2.Cap() < initialCap {
		t.Errorf("recycled buffer Cap = %d, want >= %d", buf2.Cap(), initialCap)
	}
	BufferPool.Put(buf2)
}

func TestBufferPool_OversizedDiscard(t *testing.T) {
	buf := BufferPool.Get()
	// Grow past 1MB threshold.
	buf.Grow(2 * 1024 * 1024)
	bigCap := buf.Cap()
	BufferPool.Put(buf) // should be discarded

	// Next Get should return a fresh buffer, not the oversized one.
	buf2 := BufferPool.Get()
	if buf2.Cap() >= bigCap {
		t.Errorf("oversized buffer should have been discarded, got cap=%d", buf2.Cap())
	}
	BufferPool.Put(buf2)
}

func TestCloseIdleConnections(_ *testing.T) {
	m := NewDefault()
	m.RegisterProvider("test", DefaultConfig())
	// Should not panic.
	m.CloseIdleConnections()
}

func TestDefaultClient(t *testing.T) {
	m := NewDefault()
	if m.DefaultClient() == nil {
		t.Error("DefaultClient() must not be nil")
	}
	if m.DefaultTransport() == nil {
		t.Error("DefaultTransport() must not be nil")
	}
}

func BenchmarkBufferPool(b *testing.B) {
	b.Run("pool", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				buf := BufferPool.Get()
				buf.WriteString("benchmark payload data for testing pool performance")
				BufferPool.Put(buf)
			}
		})
	})

	b.Run("bare_alloc", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				buf := make([]byte, 0, 32*1024)
				_ = append(buf, "benchmark payload data for testing pool performance"...)
			}
		})
	})
}

func BenchmarkTransportConcurrent(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"test","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	defer srv.Close()

	m := NewDefault()
	client := m.ForProvider("bench")
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`

	// Warm up connections.
	for i := 0; i < 20; i++ {
		resp, err := client.Post(srv.URL, "application/json", strings.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := client.Post(srv.URL, "application/json", strings.NewReader(body))
			if err != nil {
				b.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	})
}

func BenchmarkForProvider(b *testing.B) {
	m := NewDefault()
	m.RegisterProvider("openai", DefaultConfig())
	m.RegisterProvider("anthropic", DefaultConfig())

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		providers := []string{"openai", "anthropic", "unknown"}
		i := 0
		for pb.Next() {
			_ = m.ForProvider(providers[i%3])
			i++
		}
	})
}

func BenchmarkBufferPoolContention(b *testing.B) {
	b.ReportAllocs()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < b.N; i++ {
				buf := BufferPool.Get()
				buf.WriteString(`{"model":"gpt-4o","messages":[{"role":"user","content":"test"}]}`)
				BufferPool.Put(buf)
			}
		}()
	}
	wg.Wait()
}
