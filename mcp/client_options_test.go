package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/version"
)

// Keep the original constructor usable as a function value.
var _ func(string, map[string]string, time.Duration) *Client = NewClient

type optionTransport func(*http.Request) (*http.Response, error)

func (f optionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientOptionsValidation(t *testing.T) {
	for name, opts := range map[string]ClientOptions{
		"negative limit": {MaxResponseBytes: -1},
		"overflow limit": {MaxResponseBytes: math.MaxInt64},
		"name only":      {ClientName: "example"},
		"version only":   {ClientVersion: "1"},
		"blank name":     {ClientName: " ", ClientVersion: "1"},
	} {
		t.Run(name, func(t *testing.T) {
			client, err := NewClientWithOptions("http://example.invalid", nil, 0, opts)
			if err == nil || client != nil {
				t.Fatalf("got %v, %v; want nil and error", client, err)
			}
		})
	}
}

func TestClientOptionsDefaultsAndOwnership(t *testing.T) {
	for _, old := range []bool{false, true} {
		headers := map[string]string{"X-Example": "original"}
		c := NewClient("http://example.invalid", headers, 0)
		if !old {
			var err error
			c, err = NewClientWithOptions("http://example.invalid", headers, 0, ClientOptions{})
			if err != nil {
				t.Fatal(err)
			}
		}
		headers["X-Example"] = "changed"
		if c.headers["X-Example"] != "original" || c.httpClient.Timeout != defaultToolCallTimeout || c.maxResponseBytes != maxResponseBodyBytes || c.clientName != "ferro-ai-gateway" || c.clientVersion != version.Short() {
			t.Fatalf("defaults or header copy changed (legacy=%v)", old)
		}
		if err := c.httpClient.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
			t.Fatalf("redirect policy: %v", err)
		}
	}
	for _, clientTimeout := range []time.Duration{0, time.Second, time.Minute} {
		original := &http.Client{Timeout: clientTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		c, err := NewClientWithOptions("http://example.invalid", nil, 5*time.Second, ClientOptions{HTTPClient: original})
		if err != nil {
			t.Fatal(err)
		}
		want := 5 * time.Second
		if clientTimeout > 0 && clientTimeout < want {
			want = clientTimeout
		}
		if c.httpClient == original || original.Timeout != clientTimeout || c.httpClient.Timeout != want {
			t.Fatal("caller client mutated or timeout extended")
		}
		if !errors.Is(c.httpClient.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
			t.Fatal("caller redirect policy lost")
		}
	}
}

func TestClientOptionsTransportIdentityAndNotification(t *testing.T) {
	var methods []string
	transport := optionTransport(func(r *http.Request) (*http.Response, error) {
		var rpc JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			t.Fatal(err)
		}
		methods = append(methods, rpc.Method)
		if r.Header.Get("X-Example") != "original" {
			t.Error("header copy lost")
		}
		if rpc.Method == "initialize" {
			var params struct {
				ClientInfo struct{ Name, Version string } `json:"clientInfo"`
			}
			if err := json.Unmarshal(rpc.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.ClientInfo.Name != "example-app" || params.ClientInfo.Version != "2" {
				t.Error("custom identity lost")
			}
		} else if r.Header.Get("Mcp-Session-Id") != "session-example" {
			t.Error("notification lost session")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Mcp-Session-Id": []string{"session-example"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":` + string(initializeResult("example", "1")) + `}`))}, nil
	})
	headers := map[string]string{"X-Example": "original"}
	original := &http.Client{Transport: transport}
	c, err := NewClientWithOptions("http://example.invalid", headers, 0, ClientOptions{HTTPClient: original, ClientName: "example-app", ClientVersion: "2"})
	if err != nil {
		t.Fatal(err)
	}
	headers["X-Example"] = "changed"
	original.Transport = optionTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("caller changed transport") })
	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "initialize,notifications/initialized" {
		t.Fatalf("methods: %v", methods)
	}
}

type observedBody struct {
	io.Reader
	closed bool
}

func (b *observedBody) Close() error { b.closed = true; return nil }

func TestClientOptionsResponseLimits(t *testing.T) {
	const response = `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`
	for _, sse := range []bool{false, true} {
		body := response
		contentType := "application/json"
		if sse {
			body = "data: " + response + "\n\n"
			contentType = "text/event-stream"
		}
		for _, overflow := range []bool{false, true} {
			limit := int64(len(body))
			if overflow {
				limit--
			}
			observed := &observedBody{Reader: strings.NewReader(body)}
			c, err := NewClientWithOptions("http://example.invalid", nil, 0, ClientOptions{MaxResponseBytes: limit, HTTPClient: &http.Client{Transport: optionTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: observed}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.ListTools(context.Background())
			if (err != nil) != overflow {
				t.Fatalf("sse=%v overflow=%v: %v", sse, overflow, err)
			}
			if !observed.closed {
				t.Fatal("response body was not closed")
			}
		}
	}
}

func TestClientOptionsCancellation(t *testing.T) {
	c, err := NewClientWithOptions("http://example.invalid", nil, 0, ClientOptions{HTTPClient: &http.Client{Transport: optionTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ListTools(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestClientOptionsTimeout(t *testing.T) {
	c, err := NewClientWithOptions("http://example.invalid", nil, time.Millisecond, ClientOptions{HTTPClient: &http.Client{Transport: optionTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListTools(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
}

func TestClientOptionsResponseErrorsCloseBody(t *testing.T) {
	for _, tt := range []struct {
		name, body, want string
		status           int
	}{
		{"malformed", "{", "unmarshal", http.StatusOK},
		{"rpc", `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"unknown method"}}`, "unknown method", http.StatusOK},
		{"http", strings.Repeat("x", maxErrorBodyBytes) + "not-read", "HTTP 500", http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := &observedBody{Reader: strings.NewReader(tt.body)}
			c, err := NewClientWithOptions("http://example.invalid", nil, 0, ClientOptions{MaxResponseBytes: 1024, HTTPClient: &http.Client{Transport: optionTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Header: http.Header{}, Body: body}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.ListTools(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "not-read") {
				t.Fatalf("response error: %v", err)
			}
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}
