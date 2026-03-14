package ratelimit

import (
	"context"
	"testing"

	"github.com/ferro-labs/ai-gateway/plugin"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

func newPlugin(t *testing.T, cfg map[string]interface{}) *Plugin {
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
	p := newPlugin(t, map[string]interface{}{})
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
	p := newPlugin(t, map[string]interface{}{
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

func TestPlugin_Init_InvalidRPS(t *testing.T) {
	p := &Plugin{}
	if p.Init(map[string]interface{}{"requests_per_second": "bad"}) == nil {
		t.Error("expected error for invalid requests_per_second")
	}
}

func TestPlugin_Init_InvalidBurst(t *testing.T) {
	p := &Plugin{}
	if p.Init(map[string]interface{}{"burst": "bad"}) == nil {
		t.Error("expected error for invalid burst")
	}
}

func TestPlugin_Init_InvalidKeyRPM(t *testing.T) {
	p := &Plugin{}
	if p.Init(map[string]interface{}{"key_rpm": "bad"}) == nil {
		t.Error("expected error for invalid key_rpm type")
	}
}

func TestPlugin_Init_ZeroKeyRPM(t *testing.T) {
	p := &Plugin{}
	if p.Init(map[string]interface{}{"key_rpm": 0.0}) == nil {
		t.Error("expected error for key_rpm=0")
	}
}

func TestPlugin_Init_InvalidUserRPM(t *testing.T) {
	p := &Plugin{}
	if p.Init(map[string]interface{}{"user_rpm": "bad"}) == nil {
		t.Error("expected error for invalid user_rpm type")
	}
}

func TestPlugin_Init_ZeroUserRPM(t *testing.T) {
	p := &Plugin{}
	if p.Init(map[string]interface{}{"user_rpm": 0.0}) == nil {
		t.Error("expected error for user_rpm=0")
	}
}

func TestPlugin_Execute_Allow(t *testing.T) {
	p := newPlugin(t, map[string]interface{}{"requests_per_second": 1000.0, "burst": 1000.0})
	pctx := &plugin.Context{
		Request:  &core.Request{},
		Metadata: map[string]interface{}{},
	}
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Errorf("Execute returned unexpected error: %v", err)
	}
	if pctx.Reject {
		t.Errorf("expected allow, but Reject=true (reason: %q)", pctx.Reason)
	}
}

func TestPlugin_Execute_GlobalDeny(t *testing.T) {
	p := newPlugin(t, map[string]interface{}{"requests_per_second": 0.0, "burst": 0.0})
	pctx := &plugin.Context{
		Request:  &core.Request{},
		Metadata: map[string]interface{}{},
	}
	if p.Execute(context.Background(), pctx) == nil {
		t.Error("expected error from Execute for exhausted global limiter")
	}
	if !pctx.Reject {
		t.Error("expected Reject=true for exhausted global limiter")
	}
}

func TestPlugin_Execute_PerKeyDeny(t *testing.T) {
	p := newPlugin(t, map[string]interface{}{
		"requests_per_second": 1000.0,
		"burst":               1000.0,
		"key_rpm":             0.001,
	})
	mkCtx := func() *plugin.Context {
		return &plugin.Context{
			Request:  &core.Request{},
			Metadata: map[string]interface{}{"api_key": "client-key"},
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
	p := newPlugin(t, map[string]interface{}{
		"requests_per_second": 1000.0,
		"burst":               1000.0,
		"user_rpm":            0.001,
	})
	var gotDeny bool
	for i := 0; i < 20; i++ {
		pctx := &plugin.Context{
			Request:  &core.Request{User: "user-abc"},
			Metadata: map[string]interface{}{},
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
	p := newPlugin(t, map[string]interface{}{
		"requests_per_second": 1000.0,
		"burst":               1000.0,
		"key_rpm":             0.001,
		"user_rpm":            0.001,
	})
	pctx := &plugin.Context{
		Request:  &core.Request{User: ""},
		Metadata: map[string]interface{}{"api_key": ""},
	}
	if err := p.Execute(context.Background(), pctx); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if pctx.Reject {
		t.Errorf("expected allow for empty key/user, got Reject (reason: %q)", pctx.Reason)
	}
}
