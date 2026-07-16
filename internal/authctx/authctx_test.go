package authctx

import (
	"context"
	"testing"
)

func TestKeyID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		ctx    func() context.Context
		wantID string
		wantOK bool
	}{
		{
			name:   "absent id reports not-ok",
			ctx:    context.Background,
			wantID: "",
			wantOK: false,
		},
		{
			name:   "stored id round-trips",
			ctx:    func() context.Context { return WithKeyID(context.Background(), "key-123") },
			wantID: "key-123",
			wantOK: true,
		},
		{
			// Security boundary: an empty id must read as absent so per-key
			// limits never collapse empty-keyed callers into a shared bucket.
			name:   "empty id is treated as absent",
			ctx:    func() context.Context { return WithKeyID(context.Background(), "") },
			wantID: "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotID, gotOK := KeyID(tt.ctx())
			if gotID != tt.wantID || gotOK != tt.wantOK {
				t.Errorf("KeyID() = (%q, %v), want (%q, %v)", gotID, gotOK, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestWithKeyID_OverwritesPreviousValue(t *testing.T) {
	t.Parallel()
	ctx := WithKeyID(WithKeyID(context.Background(), "first"), "second")
	if id, ok := KeyID(ctx); !ok || id != "second" {
		t.Errorf("KeyID() = (%q, %v), want (second, true)", id, ok)
	}
}

func TestWithKeyID_DoesNotMutateParent(t *testing.T) {
	t.Parallel()
	parent := context.Background()
	_ = WithKeyID(parent, "child")
	if id, ok := KeyID(parent); ok || id != "" {
		t.Errorf("parent context was mutated: KeyID() = (%q, %v), want empty", id, ok)
	}
}
