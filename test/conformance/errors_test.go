package conformance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// nativeErrorStatuses maps each provider's native error-body testdata file to
// the upstream HTTP status the stub serves it with.
var nativeErrorStatuses = map[string]int{
	"error.401.json": http.StatusUnauthorized,
	"error.404.json": http.StatusNotFound,
}

// newNativeErrorStub answers every path with the provider's native error
// envelope and the given non-success status.
func newNativeErrorStub(body string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// TestProviderNativeErrorConformance proves each adapter maps its OWN vendor's
// native error envelope — Anthropic's {"type":"error",...}, Cloudflare's
// {"success":false,"errors":[...]}, Azure's {"error":{"code":...}}, and the rest
// — to a typed *core.HTTPStatusError that carries the upstream status and a
// non-empty caller-facing message.
//
// It is the structured-body companion to
// providers.TestProviderStatusConformance, which proves the same typed-status
// contract survives even an UNRECOGNISED body. Together they bracket the
// contract: a body the adapter understands must be parsed, and one it does not
// must still not leak or lose the status. The native bodies live next to each
// provider in providers/<dir>/testdata/error.<status>.json; a fixtured provider
// missing one fails here (loadTestdata is fatal), which is the drift guard.
func TestProviderNativeErrorConformance(t *testing.T) {
	for id, fx := range fixtures() {
		for file, status := range nativeErrorStatuses {
			t.Run(fmt.Sprintf("%s/%d", id, status), func(t *testing.T) {
				srv := newNativeErrorStub(loadTestdata(t, id, file), status)
				defer srv.Close()

				p := buildProvider(t, id, fx.extraCfg, fx.baseURLKey, srv.URL)

				_, err := p.Complete(context.Background(), canonicalRequest(fx.model))
				if err == nil {
					t.Fatalf("Complete() returned no error for a %d upstream response", status)
				}

				var statusErr *core.HTTPStatusError
				if !errors.As(err, &statusErr) {
					t.Fatalf("errors.As found no *core.HTTPStatusError in %T: %v", err, err)
				}
				if statusErr.StatusCode != status {
					t.Errorf("StatusCode = %d, want %d; err = %v", statusErr.StatusCode, status, err)
				}
				if statusErr.Message == "" {
					t.Error("Message is empty; the caller-facing error would say nothing")
				}
				if got := core.ParseStatusCode(err); got != status {
					t.Errorf("ParseStatusCode = %d, want %d", got, status)
				}
			})
		}
	}
}
