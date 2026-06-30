package bootstrap

import (
	"os"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"
)

func TestCheckProductionSafety(t *testing.T) {
	tests := []struct {
		name        string
		allowUnauth string
		gatewayEnv  string
		wantErr     bool
	}{
		{
			name:        "production + unauthenticated proxy enabled",
			allowUnauth: "true",
			gatewayEnv:  "production",
			wantErr:     true,
		},
		{
			name:        "production + unauthenticated proxy case insensitive",
			allowUnauth: "TRUE",
			gatewayEnv:  "PRODUCTION",
			wantErr:     true,
		},
		{
			name:        "development + unauthenticated proxy allowed",
			allowUnauth: "true",
			gatewayEnv:  "development",
			wantErr:     false,
		},
		{
			name:        "no GATEWAY_ENV + unauthenticated proxy allowed",
			allowUnauth: "true",
			gatewayEnv:  "",
			wantErr:     false,
		},
		{
			name:        "production + auth not bypassed",
			allowUnauth: "false",
			gatewayEnv:  "production",
			wantErr:     false,
		},
		{
			name:        "production + ALLOW_UNAUTHENTICATED_PROXY unset",
			allowUnauth: "",
			gatewayEnv:  "production",
			wantErr:     false,
		},
		{
			name:        "staging + unauthenticated proxy allowed",
			allowUnauth: "true",
			gatewayEnv:  "staging",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", tt.allowUnauth)
			t.Setenv("GATEWAY_ENV", tt.gatewayEnv)

			err := CheckProductionSafety()
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckProductionSafety() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRegisterProvidersRegistersBedrockWithBearerTokenOnly(t *testing.T) {
	for _, entry := range providers.AllProviders() {
		for _, mapping := range entry.EnvMappings {
			t.Setenv(mapping.EnvVar, "")
		}
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "test-bearer-token")

	registry := RegisterProviders()
	p, ok := registry.Get(providers.NameBedrock)
	if !ok {
		t.Fatal("Bedrock provider was not registered")
	}

	proxiable, ok := p.(interface {
		AuthHeaders() map[string]string
	})
	if !ok {
		t.Fatalf("Bedrock provider type %T does not expose AuthHeaders", p)
	}
	if got := proxiable.AuthHeaders()["Authorization"]; got != "Bearer test-bearer-token" {
		t.Errorf("Authorization = %q, want Bearer test-bearer-token", got)
	}
}

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
