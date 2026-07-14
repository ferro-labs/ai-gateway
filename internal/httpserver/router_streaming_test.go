package httpserver_test

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/admin"
	"github.com/ferro-labs/ai-gateway/internal/httpserver"
	"github.com/ferro-labs/ai-gateway/providers"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
)

// middleware.RecoverJSON wraps every request's ResponseWriter to track whether
// the response has been committed. That wrapper does not satisfy http.Flusher or
// http.Hijacker itself — the stack reaches them through Unwrap and
// http.NewResponseController. These tests pin that contract through the real
// router, where a broken Unwrap chain would silently stop flushing or turn every
// protocol upgrade into a 502.

// buildProxyTestRouter wires the full router with a proxiable provider aimed at
// upstreamURL, reachable as "X-Provider: openai".
func buildProxyTestRouter(t *testing.T, upstreamURL string) http.Handler {
	t.Helper()
	t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", "true")

	gw, err := aigateway.New(aigateway.Config{
		Strategy: aigateway.StrategyConfig{Mode: aigateway.ModeSingle},
		Targets:  []aigateway.Target{{VirtualKey: "stub"}},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}

	reg := providers.NewRegistry()
	p, err := openaipkg.New("sk-test-key", upstreamURL)
	if err != nil {
		t.Fatalf("build openai provider: %v", err)
	}
	reg.Register(p)

	return httpserver.NewRouter(reg, admin.NewKeyStore(), nil, gw, nil, nil, nil, nil, "", nil)
}

func TestRouter_ProxyUpgradeSurvivesResponseWriterWrapping(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("upstream hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buf.Flush()

		line, err := buf.ReadString('\n')
		if err != nil {
			return
		}
		_, _ = buf.WriteString("echo:" + line)
		_ = buf.Flush()
	}))
	defer upstream.Close()

	gateway := httptest.NewServer(buildProxyTestRouter(t, upstream.URL))
	defer gateway.Close()

	var dialer net.Dialer
	conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(gateway.URL, "http://"))
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := conn.Write([]byte("GET /v1/realtime HTTP/1.1\r\nHost: gateway\r\n" +
		"X-Provider: openai\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")); err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("status = %q, want 101 Switching Protocols", strings.TrimSpace(status))
	}
	for { // drain headers
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write through tunnel: %v", err)
	}
	echo, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read through tunnel: %v", err)
	}
	if strings.TrimSpace(echo) != "echo:ping" {
		t.Fatalf("tunnel echo = %q, want echo:ping", strings.TrimSpace(echo))
	}
}

func TestRouter_ProxyStreamReachesClientIncrementally(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: first\n\n"))
		_ = http.NewResponseController(w).Flush()
		<-release // hold the response open: the first chunk must already be out
		_, _ = w.Write([]byte("data: second\n\n"))
	}))
	defer upstream.Close()
	defer close(release)

	gateway := httptest.NewServer(buildProxyTestRouter(t, upstream.URL))
	defer gateway.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, gateway.URL+"/v1/responses", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Provider", "openai")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// If flushing did not survive the wrapper chain, this read blocks until the
	// upstream handler returns — which it cannot do until release is closed.
	type readResult struct {
		data string
		err  error
	}
	got := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 64)
		n, readErr := resp.Body.Read(buf)
		got <- readResult{data: string(buf[:n]), err: readErr}
	}()

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("read first chunk: %v", r.err)
		}
		if !strings.Contains(r.data, "first") {
			t.Fatalf("first chunk = %q, want it to contain \"first\"", r.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first chunk never arrived: flushing was lost through the ResponseWriter wrapper chain")
	}
}
