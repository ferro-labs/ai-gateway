package ratelimit

import (
	"context"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

func newPlugin(t *testing.T, cfg map[string]any) *Plugin {
	t.Helper()
	p := &Plugin{}
	if err := p.Init(cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return p
}

func TestPlugin_Name(t *testing.T) {
	p := &Plugin{}
	if p.Name() != "rate-limit" {
		t.Errorf("Name() = %q, want %q", p.Name(), "rate-limit")
	}
}

func TestPlugin_Type(t *testing.T) {
	p := &Plugin{}
	if p.Type() != plugin.TypeRateLimit {
		t.Errorf("Type() = %v, want TypeRateLimit", p.Type())
	}
}

func TestPlugin_Init_Defaults(t *testing.T) {
	p := newPlugin(t, map[string]any{})
	if p.limiter == nil {
		t.Error("expected global limiter to be set")
	}
	if p.keyStore != nil {
		t.Error("expected keyStore to be nil when key_rpm not set")
	}
	if p.userStore != nil {
		t.Error("expected userStore to be nil when user_rpm not set")
	}
}

func TestPlugin_Init_AllKeys(t *testing.T) {
	p := newPlugin(t, map[string]any{
		"requests_per_second": 50.0,
		"burst":               100.0,
		"key_rpm":             600.0,
		"user_rpm":            300.0,
	})
	if p.limiter == nil {
		t.Error("expected global limiter")
	}
	if p.keyStore == nil {
		t.Error("expected keyStore when key_rpm set")
	}
	if p.userStore == nil {
		t.Error("expected userStore when user_rpm set")
	}
}

func TestPlugin_Execute_Allow(t *testing.T) {
	p := newPlugin(t, map[string]any{"requests_per_second": 1000.0, "burst": 1000.0})
	pctx := &plugin.Context{
		Request:  &core.Request{},
		Metadata: map[string]any{},
	}
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Errorf("Execute returned unexpected error: %v", err)
	}
	if pctx.Reject {
		t.Errorf("expected allow, but Reject=true (reason: %q)", pctx.Reason)
	}
}

func TestPlugin_Execute_GlobalDeny(t *testing.T) {
	// The smallest configurable bucket: one token, refilled slowly enough that
	// the second request in the same instant cannot find one. A rate of zero
	// would deny more directly, and used to be how this was written, but it is
	// now rejected at load precisely because it denies forever.
	p := newPlugin(t, map[string]any{"requests_per_second": 0.001, "burst": 1.0})
	newCtx := func() *plugin.Context {
		return &plugin.Context{Request: &core.Request{}, Metadata: map[string]any{}}
	}

	first := newCtx()
	if err := p.Execute(context.Background(), first); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if first.Reject {
		t.Fatalf("first request rejected with a full bucket: %s", first.Reason)
	}

	second := newCtx()
	if err := p.Execute(context.Background(), second); err != nil {
		t.Fatalf("an exhausted limiter is a verdict, not a plugin malfunction: %v", err)
	}
	if !second.Reject {
		t.Error("expected Reject=true for exhausted global limiter")
	}
}

func TestPlugin_Execute_PerKeyDeny(t *testing.T) {
	p := newPlugin(t, map[string]any{
		"requests_per_second": 1000.0,
		"burst":               1000.0,
		"key_rpm":             0.001,
	})
	mkCtx := func() *plugin.Context {
		return &plugin.Context{
			Request:  &core.Request{},
			Metadata: map[string]any{"api_key": "client-key"},
		}
	}
	var gotDeny bool
	for i := 0; i < 20; i++ {
		pctx := mkCtx()
		_ = p.Execute(context.Background(), pctx)
		if pctx.Reject {
			gotDeny = true
			if pctx.Reason != "per-key rate limit exceeded" {
				t.Errorf("expected per-key reason, got %q", pctx.Reason)
			}
			break
		}
	}
	if !gotDeny {
		t.Error("expected per-key rate limit to trigger within 20 calls")
	}
}

func TestPlugin_Execute_PerUserDeny(t *testing.T) {
	p := newPlugin(t, map[string]any{
		"requests_per_second": 1000.0,
		"burst":               1000.0,
		"user_rpm":            0.001,
	})
	var gotDeny bool
	for i := 0; i < 20; i++ {
		pctx := &plugin.Context{
			Request:  &core.Request{User: "user-abc"},
			Metadata: map[string]any{},
		}
		_ = p.Execute(context.Background(), pctx)
		if pctx.Reject {
			gotDeny = true
			if pctx.Reason != "per-user rate limit exceeded" {
				t.Errorf("expected per-user reason, got %q", pctx.Reason)
			}
			break
		}
	}
	if !gotDeny {
		t.Error("expected per-user rate limit to trigger within 20 calls")
	}
}

func TestPlugin_Execute_NoKeyOrUser_SkipsStores(t *testing.T) {
	p := newPlugin(t, map[string]any{
		"requests_per_second": 1000.0,
		"burst":               1000.0,
		"key_rpm":             0.001,
		"user_rpm":            0.001,
	})
	pctx := &plugin.Context{
		Request:  &core.Request{User: ""},
		Metadata: map[string]any{"api_key": ""},
	}
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if pctx.Reject {
		t.Errorf("expected allow for empty key/user, got Reject (reason: %q)", pctx.Reason)
	}
}

// TestParseLimits_RejectsUnusableRates covers every field being a rate rather
// than a switch.
//
// requests_per_second: 0 used to start a gateway that reported healthy and
// answered 429 to every request for the rest of its life, while its siblings
// key_rpm and user_rpm already refused to start on the same value. All four now
// agree, and Init and ValidateConfig agree with each other, so the answer does
// not depend on whether it was asked by `ferrogw validate` or by startup.
func TestParseLimits_RejectsUnusableRates(t *testing.T) {
	tests := []struct {
		name    string
		config  map[string]any
		wantErr string
	}{
		{name: "empty config uses defaults", config: map[string]any{}},
		{name: "all rates positive", config: map[string]any{
			"requests_per_second": 50.0, "burst": 100.0, "key_rpm": 600.0, "user_rpm": 300.0,
		}},
		{name: "fractional rate", config: map[string]any{"requests_per_second": 0.5}},
		{name: "zero rps", config: map[string]any{"requests_per_second": 0}, wantErr: "requests_per_second must be > 0"},
		{name: "zero rps as float", config: map[string]any{"requests_per_second": 0.0}, wantErr: "requests_per_second must be > 0"},
		{name: "negative rps", config: map[string]any{"requests_per_second": -1}, wantErr: "requests_per_second must be > 0"},
		{name: "zero rps with a burst", config: map[string]any{
			"requests_per_second": 0, "burst": 10,
		}, wantErr: "requests_per_second must be > 0"},
		{name: "zero burst", config: map[string]any{"burst": 0}, wantErr: "burst must be > 0"},
		{name: "negative burst", config: map[string]any{"burst": -5}, wantErr: "burst must be > 0"},
		{name: "zero key_rpm", config: map[string]any{"key_rpm": 0}, wantErr: "key_rpm must be > 0"},
		{name: "zero user_rpm", config: map[string]any{"user_rpm": 0}, wantErr: "user_rpm must be > 0"},
		{name: "non-numeric rps", config: map[string]any{"requests_per_second": "fast"}, wantErr: "requests_per_second"},
		{name: "non-numeric burst", config: map[string]any{"burst": "big"}, wantErr: "burst"},
		{name: "non-numeric key_rpm", config: map[string]any{"key_rpm": "many"}, wantErr: "key_rpm"},
		{name: "non-numeric user_rpm", config: map[string]any{"user_rpm": "many"}, wantErr: "user_rpm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initErr := (&Plugin{}).Init(tt.config)
			validateErr := (&Plugin{}).ValidateConfig(tt.config)

			if (initErr == nil) != (validateErr == nil) {
				t.Fatalf("Init and ValidateConfig disagree: Init = %v, ValidateConfig = %v", initErr, validateErr)
			}
			if tt.wantErr == "" {
				if initErr != nil {
					t.Fatalf("unexpected error: %v", initErr)
				}
				return
			}
			if initErr == nil {
				t.Fatalf("expected an error naming %q, got nil", tt.wantErr)
			}
			if !strings.Contains(initErr.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", initErr, tt.wantErr)
			}
		})
	}
}

// TestPlugin_ImplementsConfigValidator keeps the pre-flight check wired: the
// CLI discovers it through the interface, so dropping the method would silently
// send these config errors back to being startup-only failures.
func TestPlugin_ImplementsConfigValidator(t *testing.T) {
	var _ plugin.ConfigValidator = (*Plugin)(nil)

	if err := plugin.ValidateConfigFor("rate-limit", map[string]any{"requests_per_second": 0}); err == nil {
		t.Error("the registered rate-limit plugin does not reject requests_per_second: 0 through the registry")
	}
}
