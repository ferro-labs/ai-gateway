package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/providers"
	openaipkg "github.com/ferro-labs/ai-gateway/providers/openai"
)

// syntheticKey is the stand-in credential every test in this file injects. It is
// not a real key and never was; it only has to clear redact.MinSecretLength.
const syntheticKey = "sk-SYNTHETIC-proxy-credential-0000"

// upstreamResponse builds an upstream response with the given status and body.
func upstreamResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Length": []string{strconv.Itoa(len(body))}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func testSecrets() []string {
	return injectedSecrets(map[string]string{"Authorization": "Bearer " + syntheticKey})
}

// The credential the gateway injects must never reach the client, who never
// sent it. Several providers quote the offending key in a 401/403 body, and the
// pass-through relayed that body verbatim.
func TestSanitizeResponse_RedactsInjectedCredentialInBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"bare token", `{"error":"forbidden","offending_key":"` + syntheticKey + `"}`},
		{"with scheme", `{"error":"forbidden","offending_key":"Bearer ` + syntheticKey + `"}`},
		{"mid-sentence", `Key ` + syntheticKey + ` is not authorised for this account.`},
		{"repeated", syntheticKey + ` and again ` + syntheticKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result: sanitizeResponse closes the original body and readBody closes the replacement.
			resp := upstreamResponse(http.StatusForbidden, tc.body)
			if err := sanitizeResponse(resp, testSecrets()); err != nil {
				t.Fatalf("sanitize: %v", err)
			}
			got := readBody(t, resp)
			if strings.Contains(got, syntheticKey) {
				t.Errorf("credential leaked to client: %s", got)
			}
			if !strings.Contains(got, injectedSecretToken) {
				t.Errorf("no redaction token in %q", got)
			}
		})
	}
}

// Response headers were never scanned at all. WWW-Authenticate is the
// standards-defined slot for a 401 to explain itself (RFC 9110 §11.6.1) and so
// the likeliest place for a provider to quote the key it rejected; a 3xx
// Location can carry one in its query string; and an arbitrary vendor header can
// carry one on any status, success included.
func TestSanitizeResponse_RedactsCredentialInHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header string
		value  string
	}{
		{
			name:   "www-authenticate on 401",
			status: http.StatusUnauthorized,
			header: "Www-Authenticate",
			value:  `Bearer error="invalid_token", key="` + syntheticKey + `"`,
		},
		{
			name:   "location on 302",
			status: http.StatusFound,
			header: "Location",
			value:  "https://upstream.example/login?api_key=" + syntheticKey,
		},
		{
			name:   "vendor header on 400",
			status: http.StatusBadRequest,
			header: "X-Upstream-Debug",
			value:  "rejected credential " + syntheticKey,
		},
		{
			name:   "vendor header on 200",
			status: http.StatusOK,
			header: "X-Upstream-Debug",
			value:  "accepted credential " + syntheticKey,
		},
		{
			name:   "vendor header on 101",
			status: http.StatusSwitchingProtocols,
			header: "X-Session-Key",
			value:  syntheticKey,
		},
		{
			name:   "vendor header on 204",
			status: http.StatusNoContent,
			header: "X-Upstream-Debug",
			value:  syntheticKey,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result.
			resp := upstreamResponse(tc.status, "")
			resp.Header.Set(tc.header, tc.value)

			if err := sanitizeResponse(resp, testSecrets()); err != nil {
				t.Fatalf("sanitize: %v", err)
			}

			got := resp.Header.Get(tc.header)
			if strings.Contains(got, syntheticKey) {
				t.Errorf("credential leaked in %s: %s", tc.header, got)
			}
			if !strings.Contains(got, injectedSecretToken) {
				t.Errorf("%s was not redacted: %q", tc.header, got)
			}
		})
	}
}

// A header carrying no credential must survive byte-identical. Scrubbing matches
// literal credential values, never a shape, so an opaque high-entropy header is
// not a candidate for redaction.
func TestSanitizeResponse_LeavesUnrelatedHeadersIntact(t *testing.T) {
	//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result.
	resp := upstreamResponse(http.StatusOK, "")
	want := map[string]string{
		"Etag":         `W/"a1b2c3d4e5f60718293a4b5c6d7e8f90"`,
		"Set-Cookie":   "session=9f8e7d6c5b4a39281706f5e4d3c2b1a0; Path=/; HttpOnly",
		"X-Request-Id": "req_01HZY8Q3M4N5P6R7S8T9V0W1X2",
		"Content-Type": "application/json",
	}
	for k, v := range want {
		resp.Header.Set(k, v)
	}

	if err := sanitizeResponse(resp, testSecrets()); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	for k, v := range want {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q unchanged", k, got, v)
		}
	}
}

// A multi-valued header must have every value scanned, not only the first.
func TestSanitizeResponse_RedactsEveryHeaderValue(t *testing.T) {
	//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result.
	resp := upstreamResponse(http.StatusUnauthorized, "")
	resp.Header.Add("Www-Authenticate", "Basic realm=\"upstream\"")
	resp.Header.Add("Www-Authenticate", "Bearer key=\""+syntheticKey+"\"")

	if err := sanitizeResponse(resp, testSecrets()); err != nil {
		t.Fatalf("sanitize: %v", err)
	}

	values := resp.Header.Values("Www-Authenticate")
	if len(values) != 2 {
		t.Fatalf("got %d values, want 2", len(values))
	}
	if values[0] != `Basic realm="upstream"` {
		t.Errorf("first value altered: %q", values[0])
	}
	if strings.Contains(values[1], syntheticKey) {
		t.Errorf("credential leaked in second value: %s", values[1])
	}
}

// A redirect body was unscanned because the gate was >= 400. The reverse proxy
// does not follow redirects, so a 3xx body reaches the client intact and is not
// a success payload the caller owns.
func TestSanitizeResponse_RedactsBodyBelow400(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		redacted bool
	}{
		{"302 found", http.StatusFound, true},
		{"301 moved", http.StatusMovedPermanently, true},
		{"307 temporary redirect", http.StatusTemporaryRedirect, true},
		{"400 bad request", http.StatusBadRequest, true},
		{"500 server error", http.StatusInternalServerError, true},
		{"200 ok", http.StatusOK, false},
		{"201 created", http.StatusCreated, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"detail":"see ` + syntheticKey + `"}`
			//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result.
			resp := upstreamResponse(tc.status, body)
			if err := sanitizeResponse(resp, testSecrets()); err != nil {
				t.Fatalf("sanitize: %v", err)
			}

			got := readBody(t, resp)
			if tc.redacted && strings.Contains(got, syntheticKey) {
				t.Errorf("credential leaked in %d body: %s", tc.status, got)
			}
			if !tc.redacted && got != body {
				t.Errorf("%d body altered: %q, want %q", tc.status, got, body)
			}
		})
	}
}

// Redaction changes the length; a stale Content-Length truncates the response
// or hangs the client.
func TestSanitizeResponse_RestatesContentLength(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusBadGateway} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result.
			resp := upstreamResponse(status, `{"k":"`+syntheticKey+`"}`)
			if err := sanitizeResponse(resp, testSecrets()); err != nil {
				t.Fatalf("sanitize: %v", err)
			}
			body := readBody(t, resp)
			if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(body)) {
				t.Errorf("Content-Length %s, body is %d bytes", got, len(body))
			}
			if resp.ContentLength != int64(len(body)) {
				t.Errorf("resp.ContentLength %d, body is %d bytes", resp.ContentLength, len(body))
			}
		})
	}
}

// A success body is the caller's own payload — possibly streaming or binary.
// Buffering or rewriting it would corrupt pass-through streaming, so it is
// relayed byte for byte and the reader is never even consumed.
func TestSanitizeResponse_LeavesSuccessBodyUntouched(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			const payload = "\x00\x01binary-or-streaming-payload\xff"
			//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result; readBody closes it.
			resp := upstreamResponse(status, payload)
			original := resp.Body
			if err := sanitizeResponse(resp, testSecrets()); err != nil {
				t.Fatalf("sanitize: %v", err)
			}
			// dropTrailers installs a transparent Close-time wrapper on any
			// status that can carry a body. Unwrap it, so "the body was not
			// replaced" is still checked by identity rather than weakened.
			relayed := resp.Body
			if stripper, ok := relayed.(trailerStripper); ok {
				relayed = stripper.ReadCloser
			}
			if relayed != original {
				t.Fatal("success body was replaced; it must pass through untouched")
			}
			if got := readBody(t, resp); got != payload {
				t.Errorf("success body altered: %q", got)
			}
		})
	}
}

// A 101's body is the tunnelled connection, which handleUpgradeResponse requires
// to stay an io.ReadWriteCloser. A 304 carries no body and must not be given
// framing headers. Neither may be replaced.
func TestSanitizeResponse_LeavesBodylessStatusesUntouched(t *testing.T) {
	for _, status := range []int{
		http.StatusSwitchingProtocols,
		http.StatusNoContent,
		http.StatusNotModified,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result; readBody closes it.
			resp := upstreamResponse(status, "")
			resp.Header.Del("Content-Length")
			original := resp.Body

			if err := sanitizeResponse(resp, testSecrets()); err != nil {
				t.Fatalf("sanitize: %v", err)
			}
			if resp.Body != original {
				t.Error("body was replaced on a status that carries none")
			}
			if got := resp.Header.Get("Content-Length"); got != "" {
				t.Errorf("Content-Length %q invented on a %d", got, status)
			}
			_ = readBody(t, resp)
		})
	}
}

// A body the gateway cannot scan as text must be replaced, not relayed: it may
// contain the credential in a form the scan cannot see.
func TestSanitizeResponse_ReplacesUnscannableBodies(t *testing.T) {
	secrets := testSecrets()

	t.Run("compressed", func(t *testing.T) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write([]byte(`{"offending_key":"` + syntheticKey + `"}`)); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}
		resp := &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Content-Encoding": []string{"gzip"}},
			Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
		}
		if err := sanitizeResponse(resp, secrets); err != nil {
			t.Fatalf("sanitize: %v", err)
		}
		got := readBody(t, resp)
		if got != genericUpstreamErrorBody {
			t.Errorf("compressed error body not replaced: %q", got)
		}
		if resp.Header.Get("Content-Encoding") != "" {
			t.Error("Content-Encoding must be dropped when the body is replaced")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		body := strings.Repeat("x", maxRedactableBody+1) + syntheticKey
		//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result.
		resp := upstreamResponse(http.StatusInternalServerError, body)
		if err := sanitizeResponse(resp, secrets); err != nil {
			t.Fatalf("sanitize: %v", err)
		}
		got := readBody(t, resp)
		if strings.Contains(got, syntheticKey) {
			t.Error("credential leaked past the size bound")
		}
		if got != genericUpstreamErrorBody {
			t.Errorf("oversized body not replaced: %d bytes", len(got))
		}
	})

	t.Run("compressed redirect", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Content-Encoding": []string{"gzip"}},
			Body:       io.NopCloser(strings.NewReader("\x1f\x8b compressed " + syntheticKey)),
		}
		if err := sanitizeResponse(resp, secrets); err != nil {
			t.Fatalf("sanitize: %v", err)
		}
		if got := readBody(t, resp); got != genericUpstreamErrorBody {
			t.Errorf("compressed redirect body not replaced: %q", got)
		}
	})
}

// A credential the gateway did not inject on this request — a co-tenant's key,
// or a second provider's quoted in a federated error — is caught by the
// environment-seeded value redactor, in headers as well as in bodies.
func TestSanitizeResponse_RedactsEnvironmentCredentials(t *testing.T) {
	const other = "sk-SYNTHETIC-other-provider-9911"
	redact.RegisterSecretValues(other)

	//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result.
	resp := upstreamResponse(http.StatusBadGateway, `{"upstream":"quoted `+other+` back"}`)
	resp.Header.Set("X-Upstream-Detail", "rejected "+other)

	if err := sanitizeResponse(resp, nil); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if got := resp.Header.Get("X-Upstream-Detail"); strings.Contains(got, other) {
		t.Errorf("environment credential leaked in header: %s", got)
	}
	if got := readBody(t, resp); strings.Contains(got, other) {
		t.Errorf("environment credential leaked in body: %s", got)
	}
}

// A value below the length floor would redact ordinary words out of the
// response, and a header that does not name a credential does not hold one: a
// provider is free to put an API version in AuthHeaders, and blanking that out
// of every relayed response would corrupt ordinary traffic.
func TestInjectedSecrets_CollectsOnlyCredentialHeaders(t *testing.T) {
	short := strings.Repeat("a", redact.MinSecretLength-1)
	long := strings.Repeat("b", redact.MinSecretLength)

	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    []string
	}{
		{
			name:    "authorization with scheme yields value and token",
			headers: map[string]string{"Authorization": "Bearer " + long},
			want:    []string{"Bearer " + long, long},
		},
		{
			name:    "vendor credential header names",
			headers: map[string]string{"x-api-key": long},
			want:    []string{long},
		},
		{
			name:    "google credential header name",
			headers: map[string]string{"x-goog-api-key": long},
			want:    []string{long},
		},
		{
			name:    "short value below the floor is skipped",
			headers: map[string]string{"x-api-key": short},
			want:    nil,
		},
		{
			name:    "non-credential header is not a secret",
			headers: map[string]string{"anthropic-version": long},
			want:    nil,
		},
		{
			name:    "credential collected alongside a non-credential",
			headers: map[string]string{"x-api-key": long, "anthropic-version": strings.Repeat("c", 20)},
			want:    []string{long},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := injectedSecrets(tc.headers)
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("injectedSecrets() = %v, want %v", got, want)
			}
		})
	}
}

// buildLeakTestRegistry is buildTestRegistry with a credential long enough to
// clear redact.MinSecretLength, so the value actually enrols for scanning.
func buildLeakTestRegistry(tb testing.TB, upstreamURL string) *providers.Registry {
	tb.Helper()
	reg := providers.NewRegistry()
	p, err := openaipkg.New(syntheticKey, upstreamURL)
	if err != nil {
		tb.Fatalf("openai.New: %v", err)
	}
	reg.Register(p)
	return reg
}

// proxyGet drives a request through a live gateway in front of a raw-socket
// upstream that writes the exact bytes given, framing included.
func proxyGet(t *testing.T, rawResponse string) *http.Response {
	t.Helper()

	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Consume the request head so the client is not writing into a closed pipe.
		br := bufio.NewReader(conn)
		for {
			line, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(line) == "" {
				break
			}
		}
		_, _ = conn.Write([]byte(rawResponse))
	}()

	gateway := httptest.NewServer(Handler(buildLeakTestRegistry(t, "http://"+listener.Addr().String())))
	t.Cleanup(gateway.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, gateway.URL+"/v1/files", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Provider", providerOpenAI)

	// A redirect must be observed as it was relayed, not followed.
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxied request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// End to end, through a live gateway: none of the three relayed slots may carry
// the gateway's credential to the client. Each case pins the exact upstream
// bytes, so status, header and framing are all controlled.
func TestProxyHandler_DoesNotRelayCredential(t *testing.T) {
	for _, tc := range []struct {
		name        string
		raw         string
		wantStatus  int
		checkHeader string
	}{
		{
			name: "www-authenticate on 401",
			raw: "HTTP/1.1 401 Unauthorized\r\n" +
				"Www-Authenticate: Bearer error=\"invalid_token\", key=\"" + syntheticKey + "\"\r\n" +
				"Content-Length: 2\r\n\r\n{}",
			wantStatus:  http.StatusUnauthorized,
			checkHeader: "Www-Authenticate",
		},
		{
			name: "location and body on 302",
			raw: "HTTP/1.1 302 Found\r\n" +
				"Location: https://upstream.example/login?api_key=" + syntheticKey + "\r\n" +
				"Content-Type: application/json\r\n" +
				"Content-Length: " + strconv.Itoa(len(`{"key":"`+syntheticKey+`"}`)) + "\r\n\r\n" +
				`{"key":"` + syntheticKey + `"}`,
			wantStatus:  http.StatusFound,
			checkHeader: "Location",
		},
		{
			name: "vendor header on 200",
			raw: "HTTP/1.1 200 OK\r\n" +
				"X-Upstream-Debug: accepted " + syntheticKey + "\r\n" +
				"Content-Length: 2\r\n\r\n{}",
			wantStatus:  http.StatusOK,
			checkHeader: "X-Upstream-Debug",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			//nolint:bodyclose // proxyGet registers the close with t.Cleanup.
			resp := proxyGet(t, tc.raw)
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if got := resp.Header.Get(tc.checkHeader); strings.Contains(got, syntheticKey) {
				t.Errorf("credential relayed in %s: %s", tc.checkHeader, got)
			}
			if strings.Contains(string(body), syntheticKey) {
				t.Errorf("credential relayed in body: %s", body)
			}
			if int64(len(body)) != resp.ContentLength && resp.ContentLength >= 0 {
				t.Errorf("Content-Length %d, read %d bytes", resp.ContentLength, len(body))
			}
		})
	}
}

// A 200 the caller owns is relayed byte for byte, framing included. This is the
// accepted gap the doc comment names, and it must stay a gap: buffering success
// bodies is what would break streaming pass-through.
func TestProxyHandler_RelaysSuccessBodyByteIdentical(t *testing.T) {
	const payload = "\x00\x01binary-payload-\xfe\xff-not-utf8"
	//nolint:bodyclose // proxyGet registers the close with t.Cleanup.
	resp := proxyGet(t, "HTTP/1.1 200 OK\r\n"+
		"Content-Type: application/octet-stream\r\n"+
		"Content-Length: "+strconv.Itoa(len(payload))+"\r\n\r\n"+payload)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != payload {
		t.Errorf("success body altered: %q, want %q", body, payload)
	}
	if resp.ContentLength != int64(len(payload)) {
		t.Errorf("Content-Length = %d, want %d", resp.ContentLength, len(payload))
	}
}

// Scanning must not introduce buffering on a streamed success response: the
// client has to see chunk 1 before the upstream has written chunk 2. If the
// success body were buffered, this blocks until the deadline instead.
func TestProxyHandler_StreamingIsNotBuffered(t *testing.T) {
	firstChunkSeen := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: chunk-1\n\n"))
		_ = http.NewResponseController(w).Flush()

		select {
		case <-firstChunkSeen:
		case <-time.After(10 * time.Second):
			return
		}
		_, _ = w.Write([]byte("data: chunk-2\n\n"))
		_ = http.NewResponseController(w).Flush()
	}))
	defer upstream.Close()

	gateway := httptest.NewServer(Handler(buildLeakTestRegistry(t, upstream.URL)))
	defer gateway.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, gateway.URL+"/v1/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Provider", providerOpenAI)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxied request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	br := bufio.NewReader(resp.Body)
	first, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read chunk 1: %v", err)
	}
	if strings.TrimSpace(first) != "data: chunk-1" {
		t.Fatalf("chunk 1 = %q, want \"data: chunk-1\"", strings.TrimSpace(first))
	}
	close(firstChunkSeen)

	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read chunk 2: %v", err)
	}
	if !strings.Contains(string(rest), "data: chunk-2") {
		t.Fatalf("chunk 2 missing from %q", rest)
	}
}

// A 101's headers reach the client through handleUpgradeResponse, so they are
// scannable and are scanned. The tunnel that follows is copied raw in both
// directions and is not, so the test also pins that the upgrade still works.
func TestProxyHandler_SwitchingProtocolsRedactsHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("upstream hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
			"X-Session-Key: " + syntheticKey + "\r\n\r\n")
		_ = buf.Flush()

		line, err := buf.ReadString('\n')
		if err != nil {
			return
		}
		_, _ = buf.WriteString("echo:" + line)
		_ = buf.Flush()
	}))
	defer upstream.Close()

	gateway := httptest.NewServer(Handler(buildLeakTestRegistry(t, upstream.URL)))
	defer gateway.Close()

	var dialer net.Dialer
	conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(gateway.URL, "http://"))
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	_, err = conn.Write([]byte("GET /v1/realtime HTTP/1.1\r\nHost: gateway\r\n" +
		"X-Provider: " + providerOpenAI + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))
	if err != nil {
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

	var head strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		head.WriteString(line)
	}
	if strings.Contains(head.String(), syntheticKey) {
		t.Errorf("credential relayed in 101 headers:\n%s", head.String())
	}
	if !strings.Contains(head.String(), injectedSecretToken) {
		t.Errorf("101 header was not redacted:\n%s", head.String())
	}

	// The tunnel must still carry bytes both ways.
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

// A trailer arrives after the body, so its value is not present at the only
// point this code inspects the response. It is withheld rather than relayed
// unscanned: the credential the proxy injected is exactly what an upstream
// might quote back, and this slot cannot be scanned.
func TestSanitizeResponse_WithholdsTrailers(t *testing.T) {
	//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result.
	resp := upstreamResponse(http.StatusOK, `{"ok":true}`)
	resp.Header.Set("Trailer", "X-Upstream-Key")
	// As http.ReadResponse presents it before the body is read: announced, empty.
	resp.Trailer = http.Header{"X-Upstream-Key": []string{""}}

	if err := sanitizeResponse(resp, injectedSecrets(map[string]string{"Authorization": syntheticKey})); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if resp.Trailer != nil {
		t.Errorf("Trailer = %v, want nil; an unscannable slot must not be relayed", resp.Trailer)
	}
	if got := resp.Header.Get("Trailer"); got != "" {
		t.Errorf("Trailer announcement = %q, want it dropped with the values", got)
	}
}

// chunkedWithTrailer builds an upstream response that really carries a trailer
// on the wire: chunked framing, an announcement in the head, and the value in
// the trailer section after the terminating chunk.
func chunkedWithTrailer(statusLine, body string) string {
	return statusLine + "\r\n" +
		"Content-Type: text/plain\r\n" +
		"Trailer: X-Upstream-Key\r\n" +
		"Transfer-Encoding: chunked\r\n\r\n" +
		strconv.FormatInt(int64(len(body)), 16) + "\r\n" + body + "\r\n" +
		"0\r\n" +
		"X-Upstream-Key: " + syntheticKey + "\r\n" +
		"\r\n"
}

// The trailer slot end to end, through httputil.ReverseProxy's own response
// serialization — which is where the withholding actually has to hold.
//
// Clearing resp.Trailer inside ModifyResponse does not survive the copy: the
// transport merges the wire trailers back into that same field when the body
// reaches EOF or is closed, both of which happen after ModifyResponse returns.
// ReverseProxy reads the field again afterwards and relays whatever it finds,
// so the credential the proxy injected reached the client in a trailer even
// though the helper had "dropped" it.
func TestProxyHandler_WithholdsTrailers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusLine string
		wantStatus int
	}{
		// A success body is relayed untouched, so nothing closes the upstream
		// body before ReverseProxy copies it.
		{"200 relayed body", "HTTP/1.1 200 OK", http.StatusOK},
		// A non-2xx body is buffered and closed inside ModifyResponse, which
		// repopulates resp.Trailer before ReverseProxy even reads its length.
		{"500 replaced body", "HTTP/1.1 500 Internal Server Error", http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			//nolint:bodyclose // proxyGet registers the close with t.Cleanup.
			resp := proxyGet(t, chunkedWithTrailer(tc.statusLine, "hello"))
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if strings.Contains(string(body), syntheticKey) {
				t.Errorf("credential relayed in body: %s", body)
			}
			// Read to EOF above, so the client has parsed the trailer section.
			// Anything here is either a value relayed unscanned or an
			// announcement of a value that never arrives; both are the defect.
			if len(resp.Trailer) != 0 {
				t.Errorf("trailers reached the client: %v", resp.Trailer)
			}
			if got := resp.Header.Get("Trailer"); got != "" {
				t.Errorf("Trailer announcement = %q, want it dropped with the values", got)
			}
		})
	}
}

// Content-Encoding is a list, and http.Header.Get reads only the first field
// line. An upstream sending identity then gzip answered "identity", had its
// compressed bytes scanned as text, matched nothing, and relayed the syntheticKey.
func TestBodyIsPlainText(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		want   bool
	}{
		{"absent", nil, true},
		{"single identity", []string{"identity"}, true},
		{"identity cased", []string{"IDENTITY"}, true},
		{"single gzip", []string{"gzip"}, false},
		{"identity then gzip on separate lines", []string{"identity", "gzip"}, false},
		{"gzip then identity on separate lines", []string{"gzip", "identity"}, false},
		{"two on one line", []string{"identity, gzip"}, false},
		{"br", []string{"br"}, false},
		{"padded gzip", []string{" gzip "}, false},
		{"empty value", []string{""}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for _, v := range tc.values {
				h.Add("Content-Encoding", v)
			}
			if got := bodyIsPlainText(h); got != tc.want {
				t.Errorf("bodyIsPlainText(%v) = %v, want %v", tc.values, got, tc.want)
			}
		})
	}
}

// The split-header case end to end: the body must be replaced, not relayed.
func TestSanitizeBody_SplitContentEncodingIsRefused(t *testing.T) {
	//nolint:bodyclose // a synthetic response over a strings.Reader, not a client result.
	resp := upstreamResponse(http.StatusInternalServerError, "compressed-bytes-holding-"+syntheticKey)
	resp.Header.Add("Content-Encoding", "identity")
	resp.Header.Add("Content-Encoding", "gzip")

	if err := sanitizeResponse(resp, injectedSecrets(map[string]string{"Authorization": syntheticKey})); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	got := readBody(t, resp)
	if strings.Contains(got, syntheticKey) {
		t.Errorf("credential relayed past the encoding guard: %s", got)
	}
	if got != genericUpstreamErrorBody {
		t.Errorf("body = %q, want it replaced", got)
	}
}

// A non-2xx body is buffered so it can be scanned for an injected credential.
// That buffer is bounded in SIZE (maxRedactableBody) but the read it feeds was
// not bounded in TIME, and the two are not the same protection.
//
// An idle bound does not close this: a trickling upstream returns from every
// Read promptly, so an idle timer is re-armed each time and never fires. The
// read simply continues, one keepalive at a time, until 256 KiB have arrived —
// which at any realistic trickle rate is indefinitely.
//
// It needs no hostile upstream. A provider that answers 429 on a streaming
// endpoint and keeps the connection alive produces exactly this shape, and the
// gateway goroutine stays parked until the client gives up.
func TestProxyHandler_NonSuccessStreamDoesNotStallTheGateway(t *testing.T) {
	defer SetSanitizeScanBudgetForTest(750 * time.Millisecond)()

	stop := make(chan struct{})
	var closeOnce sync.Once
	stopUpstream := func() { closeOnce.Do(func() { close(stop) }) }
	defer stopUpstream()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusTooManyRequests)
		for {
			select {
			case <-stop:
				return
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
				if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
					return
				}
				_ = http.NewResponseController(w).Flush()
			}
		}
	}))
	defer upstream.Close()

	gateway := httptest.NewServer(Handler(buildLeakTestRegistry(t, upstream.URL)))
	defer gateway.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, gateway.URL+"/v1/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Provider", providerOpenAI)

	answered := make(chan struct{})
	go func() {
		defer close(answered)
		resp, doErr := (&http.Client{}).Do(req)
		if doErr != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	select {
	case <-answered:
	case <-time.After(15 * time.Second):
		// Release the upstream first, or the deferred Close blocks on it and the
		// whole package hangs instead of reporting this failure.
		stopUpstream()
		t.Fatal("gateway never answered a trickling non-2xx stream: the scan read is bounded in size but not in time")
	}
}
