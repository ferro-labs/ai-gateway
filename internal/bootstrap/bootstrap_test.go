package bootstrap

import (
	"os"
	"testing"
	"time"
)

func TestDiscoveryIntervalFromEnv(t *testing.T) {
	const envVar = "FERRO_MODEL_DISCOVERY_INTERVAL"

	tests := []struct {
		name    string
		set     bool
		value   string
		wantDur time.Duration
		wantOK  bool
	}{
		{name: "unset", set: false, wantDur: 0, wantOK: false},
		{name: "empty", set: true, value: "", wantDur: 0, wantOK: false},
		{name: "zero duration", set: true, value: "0s", wantDur: 0, wantOK: false},
		{name: "negative", set: true, value: "-5m", wantDur: 0, wantOK: false},
		{name: "invalid", set: true, value: "invalid", wantDur: 0, wantOK: false},
		{name: "below minimum seconds", set: true, value: "30s", wantDur: 0, wantOK: false},
		{name: "below minimum nanos", set: true, value: "1ns", wantDur: 0, wantOK: false},
		{name: "five minutes", set: true, value: "5m", wantDur: 5 * time.Minute, wantOK: true},
		{name: "thirty minutes", set: true, value: "30m", wantDur: 30 * time.Minute, wantOK: true},
		{name: "six hours", set: true, value: "6h", wantDur: 6 * time.Hour, wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(envVar, tt.value)
			} else {
				// t.Setenv registers cleanup that restores the prior value, so
				// unsetting here is safe even though the helper only reads it.
				t.Setenv(envVar, "")
				if err := os.Unsetenv(envVar); err != nil {
					t.Fatalf("failed to unset %s: %v", envVar, err)
				}
			}

			gotDur, gotOK := discoveryIntervalFromEnv()
			if gotDur != tt.wantDur || gotOK != tt.wantOK {
				t.Errorf("discoveryIntervalFromEnv() = (%v, %v), want (%v, %v)",
					gotDur, gotOK, tt.wantDur, tt.wantOK)
			}
		})
	}
}
