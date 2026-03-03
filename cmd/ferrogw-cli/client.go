package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// adminClient is a thin HTTP client that calls the gateway admin API.
type adminClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// newAdminClient creates a client from CLI flags or environment variables.
// Priority: flag value > env var > default.
func newAdminClient(gatewayURL, apiKey string) *adminClient {
	if gatewayURL == "" {
		gatewayURL = os.Getenv("FERROGW_URL")
	}
	if gatewayURL == "" {
		gatewayURL = "http://localhost:8080"
	}
	if apiKey == "" {
		apiKey = os.Getenv("FERROGW_API_KEY")
	}
	return &adminClient{
		baseURL:    gatewayURL,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// get performs a GET request to the admin API and decodes the JSON response
// into dest.
func (c *adminClient) get(path string, dest interface{}) error {
	return c.do(http.MethodGet, path, nil, dest)
}

// post performs a POST request with a JSON body.
func (c *adminClient) post(path string, body interface{}, dest interface{}) error {
	return c.do(http.MethodPost, path, body, dest)
}

// put performs a PUT request with a JSON body.
func (c *adminClient) put(path string, body interface{}, dest interface{}) error {
	return c.do(http.MethodPut, path, body, dest)
}

// del performs a DELETE request.
func (c *adminClient) del(path string, dest interface{}) error {
	return c.do(http.MethodDelete, path, nil, dest)
}

func (c *adminClient) do(method, path string, body interface{}, dest interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Try to extract an error message from the response body.
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Message != "" {
			return fmt.Errorf("API error %d: %s", resp.StatusCode, apiErr.Error.Message)
		}
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(raw))
	}

	if dest != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, dest); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
