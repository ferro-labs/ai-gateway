package api

import (
	"context"
	"net/http"
)

// Health reads the unauthenticated GET /health. A 503 is a report, not a
// failure — the body names which providers are down — so it is decoded and the
// HTTP status is returned alongside for the caller to act on.
func (c *Client) Health(ctx context.Context) (*HealthReport, int, error) {
	var out HealthReport
	status, err := c.do(ctx, http.MethodGet, "/health", nil, nil, &out,
		doOpts{tolerate: []int{http.StatusServiceUnavailable}})
	if err != nil {
		return nil, status, err
	}
	return &out, status, nil
}

// Ready reads the unauthenticated GET /readyz. As with Health, a 503 carries
// the reason and is decoded rather than raised.
func (c *Client) Ready(ctx context.Context) (*ReadyReport, int, error) {
	var out ReadyReport
	status, err := c.do(ctx, http.MethodGet, "/readyz", nil, nil, &out,
		doOpts{tolerate: []int{http.StatusServiceUnavailable}})
	if err != nil {
		return nil, status, err
	}
	return &out, status, nil
}

// AdminHealth reads GET /admin/health, which answers 200 for any authenticated
// caller. A 401/403 therefore means the credential, not the gateway, is the
// problem — callers use that to decide what they may display.
func (c *Client) AdminHealth(ctx context.Context) (*AdminHealth, error) {
	var out AdminHealth
	if _, err := c.do(ctx, http.MethodGet, "/admin/health", nil, nil, &out, doOpts{}); err != nil {
		return nil, err
	}
	return &out, nil
}

// Models lists GET /v1/models, unwrapping the {"object":"list","data":[…]}
// envelope.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	var out struct {
		Data []Model `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/v1/models", nil, nil, &out, doOpts{}); err != nil {
		return nil, err
	}
	return out.Data, nil
}
