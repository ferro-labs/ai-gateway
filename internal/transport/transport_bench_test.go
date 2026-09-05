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

func BenchmarkForProvider_KnownProviders(b *testing.B) {
	m := NewDefault()
	m.RegisterKnownProviders()

	providers := []string{"openai", "anthropic", "gemini", "groq", "unknown"}

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_ = m.ForProvider(providers[i%len(providers)])
			i++
		}
	})
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

	postJSON := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(b.Context(), http.MethodPost, srv.URL, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return client.Do(req)
	}

	// Warm up connections.
	for i := 0; i < 20; i++ {
		resp, err := postJSON()
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		if err := resp.Body.Close(); err != nil {
			b.Errorf("close response body: %v", err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := postJSON()
			if err != nil {
				b.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			if err := resp.Body.Close(); err != nil {
				b.Errorf("close response body: %v", err)
			}
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
