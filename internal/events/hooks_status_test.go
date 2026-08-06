package events

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// A failed-request event carries the status the caller was actually given.
//
// A plugin rejection is the case that matters: the client got a 429 and the
// gateway's own metrics counted a rejection, while the event shipped off-box
// said 500 — so an exporter's error rate disagreed with both.
func TestFailedRequest_StatusMatchesTheHTTPSurface(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "rate-limit rejection",
			err: &plugin.RejectionError{
				Plugin:     "rate-limit",
				PluginType: plugin.TypeRateLimit,
				Stage:      plugin.StageBeforeRequest,
				Reason:     "too many requests",
			},
			want: http.StatusTooManyRequests,
		},
		{
			name: "guardrail rejection",
			err: &plugin.RejectionError{
				Plugin:     "word-filter",
				PluginType: plugin.TypeGuardrail,
				Stage:      plugin.StageBeforeRequest,
				Reason:     "blocked word",
			},
			want: http.StatusBadRequest,
		},
		{
			name: "broken plugin is still a server error",
			err: &plugin.FailureError{
				Plugin:     "budget",
				PluginType: plugin.TypeGuardrail,
				Stage:      plugin.StageBeforeRequest,
				Err:        errors.New("store unavailable"),
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "no target serves the model",
			err:  core.ErrNoCapableProvider,
			want: http.StatusNotFound,
		},
		{
			name: "provider failure",
			err:  errors.New("upstream is unavailable"),
			want: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			he := FailedRequest("trace-1", "openai", "gpt-4o", tt.err, time.Second, false)
			if he.Status != tt.want {
				t.Errorf("Status = %d, want %d", he.Status, tt.want)
			}
			if got, _ := he.Map()["status"].(int); got != tt.want {
				t.Errorf("map status = %d, want %d", got, tt.want)
			}
		})
	}
}
