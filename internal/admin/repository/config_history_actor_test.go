package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

// The config trail recorded what changed and when, but never by whom, so
// "who rolled the gateway back at 02:00" was unanswerable by construction.
// These cover the recording, the identity chosen, and the upgrade of a
// database written before the column existed.

func newActorConfigStore(t *testing.T) *SQLConfigStore {
	t.Helper()
	store, err := NewSQLiteConfigStore(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("open config store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func latestHistoryActor(t *testing.T, store *SQLConfigStore) string {
	t.Helper()
	history, err := store.LoadHistory(t.Context())
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("no history rows written")
	}
	return history[len(history)-1].Actor
}

func TestConfigHistory_RecordsTheActingCredential(t *testing.T) {
	store := newActorConfigStore(t)

	ctx := model.ContextWithAPIKey(t.Context(), &model.APIKey{ID: "key-7f3a", Name: "alice-laptop", Scopes: []string{model.ScopeAdmin}})
	if err := store.Save(ctx, fallbackConfig()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	actor := latestHistoryActor(t, store)
	// Both halves matter: the name so a human can read the trail, the opaque
	// ID so two operators who chose the same name stay distinguishable.
	if !strings.Contains(actor, "alice-laptop") {
		t.Errorf("actor %q does not name the operator", actor)
	}
	if !strings.Contains(actor, "key-7f3a") {
		t.Errorf("actor %q does not carry the credential ID", actor)
	}
}

func TestConfigHistory_AttributesASessionToItsSourceCredential(t *testing.T) {
	store := newActorConfigStore(t)

	// A dashboard session's own ID identifies the browser, not the operator.
	// storeKeyInContext is what the session middleware uses: the APIKey slot
	// holds the session and the authctx slot holds the credential it was
	// minted from. The credential is the actor.
	ctx := model.StoreKeyInContext(t.Context(),
		&model.APIKey{ID: "session:abc123", Name: "alice-laptop", Scopes: []string{model.ScopeAdmin}},
		"key-7f3a")
	if err := store.Save(ctx, fallbackConfig()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	actor := latestHistoryActor(t, store)
	if strings.Contains(actor, "session:") {
		t.Errorf("actor %q records the browser session; it should record the credential behind it", actor)
	}
	if !strings.Contains(actor, "key-7f3a") {
		t.Errorf("actor %q does not carry the source credential ID", actor)
	}
}

func TestConfigHistory_MarksAnUnauthenticatedChange(t *testing.T) {
	store := newActorConfigStore(t)

	// No credential in context: the process applied this itself. It must be
	// recorded as something, or a blank would be indistinguishable from a row
	// written before actors were tracked.
	if err := store.Save(t.Context(), fallbackConfig()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if actor := latestHistoryActor(t, store); actor != model.ActorUnrecorded {
		t.Errorf("actor = %q, want %q", actor, model.ActorUnrecorded)
	}
}

func TestConfigHistory_UpgradesADatabaseWrittenBeforeActors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-actor-config.db")

	// Build the v2 shape by hand: config_history with no actor column, holding
	// a row an operator would still expect to read after upgrading.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(), `
CREATE TABLE gateway_config (
	id INTEGER PRIMARY KEY,
	config_json TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL
)`); err != nil {
		t.Fatalf("create gateway_config: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(), `
CREATE TABLE config_history (
	version INTEGER PRIMARY KEY,
	config_json TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL
)`); err != nil {
		t.Fatalf("create config_history: %v", err)
	}
	data, err := json.Marshal(fallbackConfig())
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if _, err := raw.ExecContext(context.Background(),
		"INSERT INTO config_history(version, config_json, updated_at) VALUES(1, ?, datetime('now'))",
		string(data)); err != nil {
		t.Fatalf("seed history row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	store, err := NewSQLiteConfigStore(t.Context(), path)
	if err != nil {
		t.Fatalf("open store on pre-actor db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	history, err := store.LoadHistory(t.Context())
	if err != nil {
		t.Fatalf("load history after upgrade: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history has %d rows, want the 1 that predated the upgrade", len(history))
	}
	// The pre-existing row keeps an empty actor. Backfilling it with anything
	// else would be inventing an attribution nobody recorded.
	if history[0].Actor != "" {
		t.Errorf("pre-upgrade row actor = %q, want empty", history[0].Actor)
	}

	// New writes are attributed normally.
	ctx := model.ContextWithAPIKey(t.Context(), &model.APIKey{ID: "key-99", Name: "bob", Scopes: []string{model.ScopeAdmin}})
	if err := store.Save(ctx, fallbackConfig()); err != nil {
		t.Fatalf("save after upgrade: %v", err)
	}
	if actor := latestHistoryActor(t, store); !strings.Contains(actor, "bob") {
		t.Errorf("post-upgrade actor = %q, want it to name the operator", actor)
	}
}
