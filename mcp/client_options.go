package mcp

import (
	"errors"
	"maps"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/internal/version"
)

// ClientOptions configures an HTTP MCP client. Its zero value uses the same
// defaults as NewClient.
type ClientOptions struct {
	// HTTPClient is shallow-copied at construction. Its transport, redirect
	// policy and cookie jar are retained; shared objects must be safe for
	// concurrent use and must not be reconfigured while requests are running.
	// A nil client uses the gateway's pooled transport and rejects redirects.
	HTTPClient *http.Client

	// MaxResponseBytes bounds successful JSON and SSE response bodies before
	// decoding. Zero selects 10 MiB. Values must be nonnegative and less than
	// math.MaxInt64. Error response bodies remain bounded at 4 KiB.
	MaxResponseBytes int64

	// ClientName and ClientVersion override the initialization identity. Both
	// must be supplied together, or both empty to use the gateway identity.
	ClientName    string
	ClientVersion string
}

// NewClientWithOptions creates an MCP client with explicit HTTP and protocol
// settings. Headers and the HTTP client configuration are copied. A nonpositive
// timeout selects 30 seconds; a shorter positive HTTPClient.Timeout is retained.
// Request contexts may impose an earlier deadline.
func NewClientWithOptions(endpoint string, headers map[string]string, timeout time.Duration, options ClientOptions) (*Client, error) {
	if options.MaxResponseBytes < 0 || options.MaxResponseBytes == math.MaxInt64 {
		return nil, errors.New("mcp: response limit must be nonnegative and less than MaxInt64")
	}
	if options.ClientName != "" || options.ClientVersion != "" {
		if strings.TrimSpace(options.ClientName) == "" || strings.TrimSpace(options.ClientVersion) == "" {
			return nil, errors.New("mcp: client name and version must both be nonblank")
		}
	}
	return newClient(endpoint, headers, timeout, options), nil
}

// newClient accepts validated options; zero options are valid by construction.
func newClient(endpoint string, headers map[string]string, timeout time.Duration, options ClientOptions) *Client {
	if timeout <= 0 {
		timeout = defaultToolCallTimeout
	}
	var client *http.Client
	if options.HTTPClient == nil {
		client = httpclient.New(timeout)
	} else {
		copyClient := *options.HTTPClient
		if copyClient.Timeout <= 0 || timeout < copyClient.Timeout {
			copyClient.Timeout = timeout
		}
		client = &copyClient
	}
	if options.MaxResponseBytes == 0 {
		options.MaxResponseBytes = maxResponseBodyBytes
	}
	if options.ClientName == "" {
		options.ClientName = "ferro-ai-gateway"
		options.ClientVersion = version.Short()
	}
	return &Client{
		endpoint:         endpoint,
		headers:          maps.Clone(headers),
		httpClient:       client,
		maxResponseBytes: options.MaxResponseBytes,
		clientName:       options.ClientName,
		clientVersion:    options.ClientVersion,
	}
}
