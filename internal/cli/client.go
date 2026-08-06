// Package cli provides shared types and helpers for the ferrogw CLI commands.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/transport"
)

// AdminClient talks to a running gateway's admin API.
type AdminClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// NewAdminClient creates a client, resolving URL and key from flags then env.
func NewAdminClient(flagURL, flagKey string) *AdminClient {
	url := flagURL
	if url == "" {
		url = os.Getenv("FERROGW_URL")
	}
	if url == "" {
		url = "http://localhost:8080"
	}

	key := flagKey
	if key == "" {
		key = os.Getenv("FERROGW_API_KEY")
	}
	if key == "" {
		key = os.Getenv("MASTER_KEY")
	}

	return &AdminClient{
		BaseURL: url,
		APIKey:  key,
		// Stdlib defaults, not the provider transport in internal/httpclient: a
		// CLI invocation makes a handful of admin calls to one host and wants
		// neither a 100-connection idle pool nor outbound OTel client spans.
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
			// Redirects are surfaced, not followed: every admin call carries the
			// operator's key as a bearer token, and following a redirect would
			// replay it to whatever host the response names.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Get performs a GET request to the given path.
func (c *AdminClient) Get(ctx context.Context, path string, dest any) error {
	return c.do(ctx, http.MethodGet, path, nil, dest)
}

// GetHealth performs a GET request against a health endpoint, decoding a 503
// "degraded" body rather than reporting it as a failed request. A gateway that
// answers with 503 is reachable — that distinction is the whole point of asking.
func (c *AdminClient) GetHealth(ctx context.Context, path string, dest any) error {
	return c.do(ctx, http.MethodGet, path, nil, dest, http.StatusServiceUnavailable)
}

// Post performs a POST request with a JSON body.
func (c *AdminClient) Post(ctx context.Context, path string, body, dest any) error {
	return c.do(ctx, http.MethodPost, path, body, dest)
}

// Put performs a PUT request with a JSON body.
func (c *AdminClient) Put(ctx context.Context, path string, body, dest any) error {
	return c.do(ctx, http.MethodPut, path, body, dest)
}

// do issues the request and decodes dest. Status codes in tolerate are decoded
// instead of being turned into an error.
func (c *AdminClient) do(ctx context.Context, method, path string, body, dest any, tolerate ...int) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	// Anything outside 2xx is a failure. The bound is not `>= 400`: this client
	// surfaces redirects rather than following them (SEC-002), so a 3xx reaches
	// here as an ordinary response. A bodyless redirect — which is what Go emits
	// for any non-GET/HEAD method — would otherwise skip the decode below and
	// return nil, making `keys revoke` report success while the key stayed live
	// (REG-002).
	if (resp.StatusCode < 200 || resp.StatusCode >= 300) && !slices.Contains(tolerate, resp.StatusCode) {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		// Named before the body is consulted: a refused redirect has none, and
		// the realistic cause is an ingress upgrading http to https, which the
		// status alone gives the operator no way to guess.
		if hint := transport.RedirectTarget(resp); hint != "" {
			return fmt.Errorf("%s %s: HTTP %d (%s)", method, path, resp.StatusCode, hint)
		}
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			return fmt.Errorf("%s %s: %s", method, path, apiErr.Error.Message)
		}
		return fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}

	// A caller that passed a destination asked for a payload, so a 2xx carrying
	// no body did not do what it reports having done. Skipping the decode
	// returned nil and left dest at its zero value: `admin keys create` printed
	// `null` and exited 0, which a script reads as a key it never received.
	//
	// This is REG-002's harm one status band over — there a bodyless redirect,
	// here a bodyless success — so it is refused the same way. A caller that
	// passed no destination is unaffected, which is what keeps `keys revoke`
	// working: its endpoint answers 204 and owes nothing back.
	if dest == nil {
		return nil
	}
	if len(respBody) == 0 {
		return fmt.Errorf("%s %s: HTTP %d with an empty response body, expected a payload",
			method, path, resp.StatusCode)
	}
	if err := json.Unmarshal(respBody, dest); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
