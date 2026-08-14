// Package api provides the HTTP, JSON, and SSE client used by ferro.
// It centralizes redirect refusal, non-2xx errors unless an endpoint tolerates
// them, empty-response refusal when a payload is expected, and uniform error
// envelope decoding.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds a single request/response exchange. SSE uses a
	// separate no-timeout client with context cancellation instead.
	DefaultTimeout = 15 * time.Second
	maxBody        = 4 << 20
)

// Client is a gateway HTTP client. Every endpoint method in this package
// funnels through the unexported do method, which owns the safety contracts.
type Client struct {
	base              *url.URL
	apiKey            string
	hc                *http.Client
	userAgent         string
	streamIdleTimeout time.Duration
}

// Option customizes a Client at construction time.
type Option func(*Client)

// WithTimeout overrides DefaultTimeout for this client's requests, including
// the response-header bound enforced by the shared transport.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.hc.Timeout = d
		if tr, ok := c.hc.Transport.(*http.Transport); ok {
			clone := tr.Clone()
			clone.ResponseHeaderTimeout = d
			c.hc.Transport = clone
		}
	}
}

// WithStreamIdleTimeout overrides the maximum time a chat stream may remain
// silent. A non-positive duration disables the client-side idle bound.
func WithStreamIdleTimeout(d time.Duration) Option {
	return func(c *Client) { c.streamIdleTimeout = d }
}

// WithUserAgent overrides the User-Agent header sent on every request.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// New builds a Client for the gateway at rawURL, authenticating with apiKey
// (empty means send no Authorization header).
func New(rawURL, apiKey string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid gateway URL %q (want e.g. http://localhost:8080)", rawURL)
	}
	// do builds every request's path and query from this base, so a query
	// string here is either silently dropped or silently replaced, and a
	// fragment never leaves the process at all. Neither reading produces the
	// request the operator wrote — the same reason the gateway refuses a query
	// or fragment in a provider base URL.
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("invalid gateway URL %q: a gateway URL carries no query string or fragment (each request builds its own)", rawURL)
	}
	c := &Client{
		base:   u,
		apiKey: apiKey,
		hc: &http.Client{
			Timeout: DefaultTimeout,
			Transport: func() http.RoundTripper {
				tr := http.DefaultTransport.(*http.Transport).Clone()
				tr.ResponseHeaderTimeout = DefaultTimeout
				return tr
			}(),
			// Redirects are surfaced, never followed: a bearer token must not
			// replay to whatever host a redirect names.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		userAgent:         "ferro-cli",
		streamIdleTimeout: DefaultStreamIdleTimeout,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// BaseURL returns the gateway URL this client talks to.
func (c *Client) BaseURL() string { return c.base.String() }

// resolveURL builds the absolute URL for endpoint path p against c.base. It
// is the one join every request goes through — do and StreamChat both call
// it — so a path-prefixed base cannot resolve to two different URLs for the
// same endpoint depending on which caller built the request.
func (c *Client) resolveURL(p string) *url.URL {
	u := *c.base
	u.Path = path.Join(c.base.Path, p)
	return &u
}

// Error is a non-2xx (or refused-redirect) response from the gateway.
//
// The name follows net/url's Error: a package-level struct named Error that
// carries an Error() method is the standard shape for this, and it reads at
// the call site as api.Error rather than the stuttering api.APIError.
type Error struct {
	Status     int
	Message    string
	Type       string
	Code       string
	RedirectTo string
}

func (e *Error) Error() string {
	if e.RedirectTo != "" {
		return fmt.Sprintf("HTTP %d redirect to %s refused — credentials are never replayed to another host (usual cause: an ingress upgrading http to https; point --gateway-url at the target)", e.Status, e.RedirectTo)
	}
	if e.Code != "" {
		return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// IsNotSupported reports whether an optional endpoint is absent on this
// gateway build (older version or feature disabled): screens degrade, the
// app never terminates on it.
func IsNotSupported(err error) bool {
	var ae *Error
	return errors.As(err, &ae) && (ae.Status == http.StatusNotFound || ae.Status == http.StatusNotImplemented)
}

// IsUnauthorized reports whether the gateway refused the credential, as
// opposed to failing for any other reason.
//
// It exists so a caller degrading on an admin-only endpoint can say which
// happened. "No admin credential accepted" is a specific claim, and answering
// it to a timeout or a 500 sends the reader to check a credential that was
// never the problem.
func IsUnauthorized(err error) bool {
	var ae *Error
	return errors.As(err, &ae) &&
		(ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden)
}

type doOpts struct {
	tolerate []int // status codes decoded as success (e.g. 503 for /health)
}

func (c *Client) do(ctx context.Context, method, p string, q url.Values, in, out any, o doOpts) (int, error) {
	u := c.resolveURL(p)
	if q != nil {
		u.RawQuery = q.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("gateway unreachable at %s: %w", c.BaseURL(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp.StatusCode, &Error{
			Status:     resp.StatusCode,
			Message:    "redirect refused",
			RedirectTo: resp.Header.Get("Location"),
		}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !ok && !slices.Contains(o.tolerate, resp.StatusCode) {
		return resp.StatusCode, decodeAPIError(resp.StatusCode, raw)
	}
	if out == nil {
		return resp.StatusCode, nil
	}
	if len(raw) == 0 {
		return resp.StatusCode, &Error{Status: resp.StatusCode,
			Message: fmt.Sprintf("HTTP %d with an empty response body, expected a payload", resp.StatusCode)}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, fmt.Errorf("decode response: %w", err)
	}
	return resp.StatusCode, nil
}

func decodeAPIError(status int, raw []byte) *Error {
	e := &Error{Status: status, Message: strings.TrimSpace(string(raw))}
	var env struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error.Message != "" {
		e.Message, e.Type, e.Code = env.Error.Message, env.Error.Type, env.Error.Code
	}
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}
