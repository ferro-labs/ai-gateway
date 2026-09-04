package config_test

import (
	"testing"

	"github.com/ferro-labs/ai-gateway/config"
)

func TestTracingConfig_AttemptSpansAndPassthroughPropagation(t *testing.T) {
	cases := []struct {
		name          string
		yaml          string
		wantAttempts  bool
		wantPropagate bool
	}{
		{
			name:          "defaults: attempt spans off, propagation on",
			yaml:          "targets:\n  - virtual_key: openai\nobservability:\n  tracing:\n    enabled: true\n",
			wantAttempts:  false,
			wantPropagate: true,
		},
		{
			name:          "both set explicitly",
			yaml:          "targets:\n  - virtual_key: openai\nobservability:\n  tracing:\n    attempt_spans: true\n    propagate_passthrough: false\n",
			wantAttempts:  true,
			wantPropagate: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempFile(t, "config.yaml", tc.yaml)
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if got := cfg.Observability.Tracing.AttemptSpans; got != tc.wantAttempts {
				t.Errorf("AttemptSpans = %v, want %v", got, tc.wantAttempts)
			}
			if got := cfg.Observability.Tracing.PropagatesPassthrough(); got != tc.wantPropagate {
				t.Errorf("PropagatesPassthrough() = %v, want %v", got, tc.wantPropagate)
			}
		})
	}
}
