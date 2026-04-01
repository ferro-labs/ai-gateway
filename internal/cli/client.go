// Package cli provides shared types and helpers for the ferrogw CLI commands.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
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
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Get performs a GET request to the given path.
func (c *AdminClient) Get(path string, dest any) error {
	return c.do(http.MethodGet, path, nil, dest)
}

// Post performs a POST request with a JSON body.
func (c *AdminClient) Post(path string, body, dest any) error {
	return c.do(http.MethodPost, path, body, dest)
}

// Put performs a PUT request with a JSON body.
func (c *AdminClient) Put(path string, body, dest any) error {
	return c.do(http.MethodPut, path, body, dest)
}

// Del performs a DELETE request.
func (c *AdminClient) Del(path string, dest any) error {
	return c.do(http.MethodDelete, path, nil, dest)
}

func (c *AdminClient) do(method, path string, body, dest any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, bodyReader)
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

	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			return fmt.Errorf("%s %s: %s", method, path, apiErr.Error.Message)
		}
		return fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}

	if dest != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, dest); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
