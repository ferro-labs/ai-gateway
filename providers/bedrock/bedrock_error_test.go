package bedrock

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// smithyResponseError builds the same *smithyhttp.ResponseError shape the AWS
// SDK's ResponseErrorWrapper middleware wraps every InvokeModel failure in
// once a response was received (e.g. a ThrottlingException or a
// ValidationException), so these tests exercise what invokeModelJSON and its
// direct-call-site duplicates actually see.
func smithyResponseError(status int, retryAfter string) error {
	header := http.Header{}
	if retryAfter != "" {
		header.Set("Retry-After", retryAfter)
	}
	return &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{
			Response: &http.Response{StatusCode: status, Header: header},
		},
		Err: errors.New("ThrottlingException: too many requests"),
	}
}

// TestBedrockProvider_Complete_TranslatesThrottlingStatus exercises the
// invokeModelJSON choke point (Nova routes through it) and verifies a 429
// with Retry-After survives as a *core.HTTPStatusError instead of being
// flattened to status 0.
func TestBedrockProvider_Complete_TranslatesThrottlingStatus(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{err: smithyResponseError(http.StatusTooManyRequests, "3")}
	p := &Provider{name: Name, client: fake}

	_, err := p.Complete(context.Background(), core.Request{
		Model:    "amazon.nova-pro-v1:0",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete() error = nil, want a translated throttling error")
	}
	if got := core.ParseStatusCode(err); got != http.StatusTooManyRequests {
		t.Errorf("ParseStatusCode(err) = %d, want 429", got)
	}
	if got := core.RetryAfterFrom(err); got != 3*time.Second {
		t.Errorf("RetryAfterFrom(err) = %v, want 3s", got)
	}
}

// TestBedrockProvider_Complete_TranslatesBadRequestStatus verifies a
// deterministic 400 also survives with its status intact (so the fallback
// strategy stops retrying it against the same target).
func TestBedrockProvider_Complete_TranslatesBadRequestStatus(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{err: smithyResponseError(http.StatusBadRequest, "")}
	p := &Provider{name: Name, client: fake}

	_, err := p.Complete(context.Background(), core.Request{
		Model:    "amazon.nova-pro-v1:0",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete() error = nil, want a translated error")
	}
	if got := core.ParseStatusCode(err); got != http.StatusBadRequest {
		t.Errorf("ParseStatusCode(err) = %d, want 400", got)
	}
	if got := core.RetryAfterFrom(err); got != 0 {
		t.Errorf("RetryAfterFrom(err) = %v, want 0 (no Retry-After header)", got)
	}
}

// TestBedrockProvider_Complete_TranslatesStatusPerFamily covers the three
// model families (Titan, Llama, Anthropic) that build and issue their own
// InvokeModel call directly rather than going through invokeModelJSON, so a
// fix scoped only to the shared helper doesn't leave these call sites behind.
func TestBedrockProvider_Complete_TranslatesStatusPerFamily(t *testing.T) {
	tests := []struct {
		name  string
		model string
	}{
		{name: "titan", model: "amazon.titan-text-express-v1"},
		{name: "llama", model: "meta.llama3-1-8b-instruct-v1:0"},
		{name: "anthropic", model: "anthropic.claude-3-5-haiku-20241022-v1:0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeBedrockRuntimeClient{err: smithyResponseError(http.StatusTooManyRequests, "3")}
			p := &Provider{name: Name, client: fake}

			_, err := p.Complete(context.Background(), core.Request{
				Model:    tt.model,
				Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
			})
			if err == nil {
				t.Fatal("Complete() error = nil, want a translated throttling error")
			}
			if got := core.ParseStatusCode(err); got != http.StatusTooManyRequests {
				t.Errorf("ParseStatusCode(err) = %d, want 429", got)
			}
			if got := core.RetryAfterFrom(err); got != 3*time.Second {
				t.Errorf("RetryAfterFrom(err) = %v, want 3s", got)
			}
		})
	}
}

// TestBedrockProvider_CompleteStream_TranslatesStatus covers the streaming
// InvokeModelWithResponseStream call site (bedrock_stream.go), which builds
// its error independently of the non-streaming path.
func TestBedrockProvider_CompleteStream_TranslatesStatus(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{err: smithyResponseError(http.StatusTooManyRequests, "3")}
	p := &Provider{name: Name, client: fake}

	_, err := p.CompleteStream(context.Background(), core.Request{
		Model:    "anthropic.claude-3-5-haiku-20241022-v1:0",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("CompleteStream() error = nil, want a translated throttling error")
	}
	if got := core.ParseStatusCode(err); got != http.StatusTooManyRequests {
		t.Errorf("ParseStatusCode(err) = %d, want 429", got)
	}
	if got := core.RetryAfterFrom(err); got != 3*time.Second {
		t.Errorf("RetryAfterFrom(err) = %v, want 3s", got)
	}
}

// TestBedrockProvider_Complete_NonHTTPErrorPassesThrough verifies a
// transport/credential failure with no HTTP response (never reached the
// service) is left as-is: it genuinely has no status to report.
// errFakeTransport stands in for a transport/credential failure that never
// reached the service. It is a sentinel so the test can prove the original
// error survives the wrap chain, rather than merely that some error came back.
var errFakeTransport = errors.New("dial tcp: connection refused")

func TestBedrockProvider_Complete_NonHTTPErrorPassesThrough(t *testing.T) {
	fake := &fakeBedrockRuntimeClient{err: errFakeTransport}
	p := &Provider{name: Name, client: fake}

	_, err := p.Complete(context.Background(), core.Request{
		Model:    "amazon.nova-pro-v1:0",
		Messages: []core.Message{{Role: core.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete() error = nil, want a wrapped transport error")
	}
	if got := core.ParseStatusCode(err); got != 0 {
		t.Errorf("ParseStatusCode(err) = %d, want 0 for a non-HTTP transport error", got)
	}
	// The cause must remain reachable: a transport failure carries no status, so
	// callers can only classify it by unwrapping to the underlying error.
	if !errors.Is(err, errFakeTransport) {
		t.Errorf("errors.Is(err, errFakeTransport) = false; the transport error was not preserved through the wrap chain: %v", err)
	}
}
