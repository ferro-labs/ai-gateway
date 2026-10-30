package mcp_test

import (
	"fmt"
	"net/http"
	"time"

	"github.com/ferro-labs/ai-gateway/mcp"
)

func ExampleNewClientWithOptions() {
	client, err := mcp.NewClientWithOptions("https://example.com/mcp", nil, 10*time.Second, mcp.ClientOptions{
		HTTPClient: &http.Client{
			Timeout:       5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		MaxResponseBytes: 2 << 20,
		ClientName:       "example-app",
		ClientVersion:    "1.0.0",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(client != nil)
	// Output: true
}
