package bootstrap

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/pkg/logger"
	"github.com/ferro-labs/ai-gateway/providers"
)

func TestCheckProductionSafety(t *testing.T) {
	tests := []struct {
		name        string
		allowUnauth string
		gatewayEnv  string
		corsOrigins string
		wantErr     bool
		wantErrText string
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
		{
			// "*" is matched literally against the Origin header, so it permits
			// nothing while reading like it permits everything. It cannot become
			// correct on its own, which is what separates a refusal from a
			// warning.
			name:        "production + CORS_ORIGINS wildcard",
			gatewayEnv:  "production",
			corsOrigins: "*",
			wantErr:     true,
			wantErrText: "matched literally",
		},
		{
			// A compose list entry keeps its quotes, so the check saw the
			// three-character string `"*"` and let the deployment start in the
			// state the refusal exists to prevent.
			name:        "production + CORS_ORIGINS wildcard in quotes",
			gatewayEnv:  "production",
			corsOrigins: `"*"`,
			wantErr:     true,
			wantErrText: "matched literally",
		},
		{
			name:        "production + CORS_ORIGINS wildcard in single quotes",
			gatewayEnv:  "production",
			corsOrigins: "'*'",
			wantErr:     true,
			wantErrText: "matched literally",
		},
		{
			name:        "production + CORS_ORIGINS wildcard among real origins",
			gatewayEnv:  "production",
			corsOrigins: "https://app.example.com, *",
			wantErr:     true,
			wantErrText: "matched literally",
		},
		{
			name:        "production + explicit CORS origins",
			gatewayEnv:  "production",
			corsOrigins: "https://app.example.com,https://admin.example.com",
			wantErr:     false,
		},
		{
			name:        "production + an origin that merely contains an asterisk",
			gatewayEnv:  "production",
			corsOrigins: "https://*.example.com",
			wantErr:     false,
		},
		{
			// Outside production the same value is honoured, with a warning.
			name:        "development + CORS_ORIGINS wildcard",
			gatewayEnv:  "development",
			corsOrigins: "*",
			wantErr:     false,
		},
		{
			// Both problems are reported together, so fixing a production
			// deployment is one edit rather than a restart per problem.
			name:        "production + both refusals at once",
			allowUnauth: "true",
			gatewayEnv:  "production",
			corsOrigins: "*",
			wantErr:     true,
			wantErrText: "ALLOW_UNAUTHENTICATED_PROXY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ALLOW_UNAUTHENTICATED_PROXY", tt.allowUnauth)
			t.Setenv("GATEWAY_ENV", tt.gatewayEnv)
			t.Setenv("CORS_ORIGINS", tt.corsOrigins)

			err := CheckProductionSafety()
			if (err != nil) != tt.wantErr {
				t.Fatalf("CheckProductionSafety() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErrText != "" && !strings.Contains(err.Error(), tt.wantErrText) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErrText)
			}
			if tt.name == "production + both refusals at once" && !strings.Contains(err.Error(), "CORS_ORIGINS") {
				t.Errorf("error = %q, want it to name both problems", err)
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

// TestWarnProductionRisks covers the settings production allows but must not
// pass over in silence. Each was previously an INFO line or nothing at all, so
// a production gateway's own startup log did not distinguish it from a laptop.
func TestWarnProductionRisks(t *testing.T) {
	tests := []struct {
		name            string
		gatewayEnv      string
		rateLimitRPS    string
		enablePprof     string
		keyStoreBackend string
		want            []string
		wantNone        bool
	}{
		{
			name:            "not production: no warnings",
			gatewayEnv:      "development",
			rateLimitRPS:    "0",
			enablePprof:     "true",
			keyStoreBackend: BackendMemory,
			wantNone:        true,
		},
		{
			name:            "production with none of them set",
			gatewayEnv:      "production",
			keyStoreBackend: "sqlite",
			wantNone:        true,
		},
		{
			name:            "production with rate limiting off",
			gatewayEnv:      "production",
			rateLimitRPS:    "0",
			keyStoreBackend: "sqlite",
			want:            []string{"RATE_LIMIT_RPS"},
		},
		{
			// Parsed, not string-compared: these disabled the limiter while the
			// warning stayed silent.
			name:            "production with rate limiting off as a float",
			gatewayEnv:      "production",
			rateLimitRPS:    "0.0",
			keyStoreBackend: "sqlite",
			want:            []string{"RATE_LIMIT_RPS"},
		},
		{
			// The mirror case: this warned about a limiter still running.
			name:            "production with a padded rate that still limits",
			gatewayEnv:      "production",
			rateLimitRPS:    " 0 ",
			keyStoreBackend: "sqlite",
			wantNone:        true,
		},
		{
			name:            "production with profiling mounted",
			gatewayEnv:      "production",
			enablePprof:     "true",
			keyStoreBackend: "sqlite",
			want:            []string{"ENABLE_PPROF"},
		},
		{
			name:            "production with an in-memory key store",
			gatewayEnv:      "production",
			keyStoreBackend: BackendMemory,
			want:            []string{"in-memory"},
		},
		{
			name:            "production with all three",
			gatewayEnv:      "production",
			rateLimitRPS:    "0",
			enablePprof:     "true",
			keyStoreBackend: BackendMemory,
			want:            []string{"RATE_LIMIT_RPS", "ENABLE_PPROF", "in-memory"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GATEWAY_ENV", tt.gatewayEnv)
			t.Setenv("RATE_LIMIT_RPS", tt.rateLimitRPS)
			t.Setenv("ENABLE_PPROF", tt.enablePprof)

			var buf bytes.Buffer
			restore := logger.Default()
			logger.SetDefault(logger.New(logger.Options{Output: &buf}))
			defer logger.SetDefault(restore)

			warnProductionRisks(tt.keyStoreBackend, NewRateLimitStore() == nil)

			got := buf.String()
			if tt.wantNone {
				if strings.Contains(got, "production:") {
					t.Fatalf("expected no production warning, got:\n%s", got)
				}
				return
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing a warning naming %q; log was:\n%s", want, got)
				}
			}
		})
	}
}

// TestLogDeprecatedEnvVars names the replacement, not just the problem: the
// warning is the only place an operator on an Ollama host learns that the
// variable they set is Ollama's own models directory and that
// FERRO_OLLAMA_MODELS is what to set instead.
func TestLogDeprecatedEnvVars(t *testing.T) {
	t.Run("warns and names the replacement", func(t *testing.T) {
		buf := captureDefaultLogger(t)
		t.Setenv("OLLAMA_MODELS", "/root/.ollama/models")

		LogDeprecatedEnvVars()

		out := buf.String()
		for _, want := range []string{"OLLAMA_MODELS", "FERRO_OLLAMA_MODELS", "deprecated"} {
			if !strings.Contains(out, want) {
				t.Errorf("log does not mention %q: %s", want, out)
			}
		}
	})

	t.Run("silent when unset", func(t *testing.T) {
		buf := captureDefaultLogger(t)
		t.Setenv("OLLAMA_MODELS", "")

		LogDeprecatedEnvVars()

		if out := strings.TrimSpace(buf.String()); out != "" {
			t.Errorf("unset OLLAMA_MODELS logged %q, want silence", out)
		}
	})
}
