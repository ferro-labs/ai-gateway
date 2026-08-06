package apierror

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/plugin/budget"
)

// budgetDenial is the error an exhausted spend cap actually reaches the handler
// in: the plugin manager mints it from the plugin's own Name() and Type().
func budgetDenial() error {
	return &plugin.RejectionError{
		Plugin:     (&budget.Plugin{}).Name(),
		PluginType: (&budget.Plugin{}).Type(),
		Stage:      plugin.StageBeforeRequest,
		Reason:     "budget exceeded: spent $50.0000 of $50.00 limit",
	}
}

// A cumulative USD cap does not refill. Answering 429 with Retry-After told
// every OpenAI SDK to come back in a second — openai-go retries 408/409/429/5xx
// — so an over-budget key re-asked a question whose answer could not change
// until an operator reset the spend, roughly once a second, until its whole
// backoff schedule was spent and it failed anyway. 402 is in no SDK's retry
// set, so the first refusal is the last request.
func TestWriteRouteError_ExhaustedBudgetIs402InsufficientQuotaWithoutRetryAfter(t *testing.T) {
	w := httptest.NewRecorder()
	WriteRouteError(w, budgetDenial())

	if w.Code != http.StatusPaymentRequired {
		t.Errorf("status = %d, want %d — waiting does not restore a spend cap", w.Code, http.StatusPaymentRequired)
	}
	if got := w.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q, want no header: nothing clears this denial on a timer", got)
	}

	message, errType, code := decodeError(t, w)
	if errType != "insufficient_quota" {
		t.Errorf("type = %q, want insufficient_quota", errType)
	}
	if code != "insufficient_quota" {
		t.Errorf("code = %q, want insufficient_quota", code)
	}
	// The denial's reason is the caller's only route to a fix, exactly as it is
	// for every other plugin rejection.
	if message == "" {
		t.Error("message is empty: the caller is owed the reason the plugin wrote")
	}
}

// The rate limiter's own 429 is correct and must not move. A token bucket DOES
// refill, so telling the client when to come back is the whole point of the
// status, and the header is what makes it work.
func TestWriteRouteError_RateLimitRejectionKeeps429AndRetryAfter(t *testing.T) {
	w := httptest.NewRecorder()
	WriteRouteError(w, &plugin.RejectionError{
		Plugin:     "rate-limit",
		PluginType: plugin.TypeRateLimit,
		Stage:      plugin.StageBeforeRequest,
		Reason:     "rate limit exceeded",
	})

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
	if got := w.Header().Get("Retry-After"); got != retryAfterSeconds {
		t.Errorf("Retry-After = %q, want %q", got, retryAfterSeconds)
	}
	if _, errType, code := decodeError(t, w); errType != errTypeRateLimit || code != "rate_limit_exceeded" {
		t.Errorf("type/code = %q/%q, want %q/rate_limit_exceeded", errType, code, errTypeRateLimit)
	}
}

// The classification above keys on the plugin's registered NAME rather than its
// type — see budgetPluginName for why. A name is a string, so this holds the
// two equal: renaming the plugin without updating the constant would silently
// drop its denials back onto the rate-limit branch and restore the retry loop,
// with nothing else failing.
func TestBudgetPluginNameMatchesTheClassifiedName(t *testing.T) {
	if got := (&budget.Plugin{}).Name(); got != budgetPluginName {
		t.Fatalf("budget plugin Name() = %q, apierror classifies %q", got, budgetPluginName)
	}
}
