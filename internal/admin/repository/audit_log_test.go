package repository

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
)

// The secret-shaped values the redaction tests feed through Detail. They are
// fabricated: the AWS id is the one AWS itself publishes as an example, and the
// other two are literals no service ever issued.
//
//nolint:gosec // G101: these are deliberately credential-shaped fixtures, not credentials — the redaction test needs strings the default policies actually match.
const (
	fakeBearerSecret = "y7Qh2Kd9Lm4Pv1Rz"
	fakeAWSKeyID     = "AKIAIOSFODNN7EXAMPLE"
	fakeOpenAIKey    = "sk-" + "aZ9bY8cX7dW6eV5fU4tS3rQ2pP1oN0mM"
)

// newSQLiteAuditStore returns a SQLite-backed audit store on a temp file.
func newSQLiteAuditStore(t *testing.T) *SQLAuditStore {
	t.Helper()

	store, err := NewSQLiteAuditStore(t.Context(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("new sqlite audit store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// newPostgresAuditStoreOrSkip returns a Postgres-backed audit store, skipping
// the test when no test database is configured. Postgres is the backend an
// operator who cares about the audit trail actually runs, so the contract is
// asserted against it wherever one is available rather than only against
// SQLite.
func newPostgresAuditStoreOrSkip(t *testing.T) *SQLAuditStore {
	t.Helper()

	dsn := os.Getenv("FERROGW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set FERROGW_TEST_POSTGRES_DSN to run Postgres audit store integration tests")
	}
	store, err := NewPostgresAuditStore(t.Context(), dsn)
	if err != nil {
		t.Fatalf("new postgres audit store: %v", err)
	}
	t.Cleanup(func() {
		// t.Context() is already canceled by the time Cleanup runs; use a fresh
		// context for this cleanup query.
		_, _ = store.db.ExecContext(context.Background(), "DELETE FROM audit_log")
		_ = store.Close()
	})
	if _, err := store.db.ExecContext(t.Context(), "DELETE FROM audit_log"); err != nil {
		t.Fatalf("clear audit_log: %v", err)
	}
	return store
}

// TestAuditStoreAppendOnly pins the interface's method set.
//
// If this fails, someone added a way for the application to change or remove an
// audit record. A trail the audited party can edit answers nothing, which is
// the whole reason there is no Update and no Delete here; retention, when it
// arrives, belongs in a job with its own credentials, not on this interface.
func TestAuditStoreAppendOnly(t *testing.T) {
	want := map[string]struct{}{"Append": {}, "List": {}, "Ping": {}, "Close": {}}

	iface := reflect.TypeOf((*AuditStore)(nil)).Elem()
	got := make(map[string]struct{}, iface.NumMethod())
	for i := range iface.NumMethod() {
		got[iface.Method(i).Name] = struct{}{}
	}

	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("AuditStore gained method %s; the interface is append-only by design", name)
		}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("AuditStore lost method %s", name)
		}
	}
}

// TestMemoryAuditStoreRedactsDetail and TestSQLiteAuditStoreRedactsDetail are
// the highest-value assertions in this file.
//
// If either fails, a bearer token or an AWS key handed to Append is stored
// verbatim in the one table built to be read during an incident — by more
// people than hold the credentials it describes. A leak into the audit trail is
// a leak to everyone who can read the trail.
func TestMemoryAuditStoreRedactsDetail(t *testing.T) {
	assertDetailRedacted(t, NewMemoryAuditStore())
}

func TestSQLiteAuditStoreRedactsDetail(t *testing.T) {
	store := newSQLiteAuditStore(t)
	assertDetailRedacted(t, store)
	assertStoredDetailRedacted(t, store)
}

func TestPostgresAuditStoreRedactsDetail(t *testing.T) {
	store := newPostgresAuditStoreOrSkip(t)
	assertDetailRedacted(t, store)
	assertStoredDetailRedacted(t, store)
}

// secretDetail is a detail payload of the shape a handler really produces: a
// JSON fragment quoting an upstream error that happens to carry credentials.
func secretDetail() string {
	return `{"error":"upstream rejected Authorization: Bearer ` + fakeBearerSecret +
		`","aws_key":"` + fakeAWSKeyID + `","provider_key":"` + fakeOpenAIKey + `"}`
}

// assertDetailRedacted appends a secret-bearing detail and checks what comes
// back out of List.
func assertDetailRedacted(t *testing.T, store AuditStore) {
	t.Helper()

	if err := store.Append(t.Context(), model.AuditEntry{
		Action:  "key.create",
		Actor:   "Ops laptop (key-7f2)",
		ActorID: "key-7f2",
		Outcome: model.AuditOK,
		Detail:  secretDetail(),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	result, err := store.List(t.Context(), model.AuditQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("got %d entries, want 1", len(result.Data))
	}
	assertNoSecrets(t, "listed detail", result.Data[0].Detail)
}

// assertStoredDetailRedacted reads the column directly, proving the redaction
// happened before the write rather than on the way out. Redacting only on read
// would leave the secret on disk, where a backup, a replica, or a SQL client
// still finds it.
func assertStoredDetailRedacted(t *testing.T, store *SQLAuditStore) {
	t.Helper()

	rows, err := store.db.QueryContext(t.Context(), "SELECT detail FROM audit_log")
	if err != nil {
		t.Fatalf("read stored detail: %v", err)
	}
	defer func() { _ = rows.Close() }()

	seen := 0
	for rows.Next() {
		var detail sql.NullString
		if err := rows.Scan(&detail); err != nil {
			t.Fatalf("scan stored detail: %v", err)
		}
		assertNoSecrets(t, "stored detail", detail.String)
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate stored detail: %v", err)
	}
	if seen == 0 {
		t.Fatal("no rows read back; the assertion above proved nothing")
	}
}

// assertNoSecrets fails when detail still carries a secret, and fails just as
// hard when the redaction markers are missing — a detail emptied or dropped
// would otherwise pass the first half of this check trivially.
func assertNoSecrets(t *testing.T, what, detail string) {
	t.Helper()

	for _, secret := range []string{fakeBearerSecret, fakeAWSKeyID, fakeOpenAIKey} {
		if strings.Contains(detail, secret) {
			t.Errorf("%s still contains a secret %q: %s", what, secret, detail)
		}
	}
	for _, marker := range []string{"[REDACTED_BEARER_TOKEN]", "[REDACTED_AWS_KEY]", "[REDACTED_OPENAI_KEY]"} {
		if !strings.Contains(detail, marker) {
			t.Errorf("%s is missing %s; it was not redacted, it was lost: %s", what, marker, detail)
		}
	}
}

func TestMemoryAuditStoreContract(t *testing.T) {
	runAuditStoreContract(t, NewMemoryAuditStore())
}

func TestSQLiteAuditStoreContract(t *testing.T) {
	runAuditStoreContract(t, newSQLiteAuditStore(t))
}

func TestPostgresAuditStoreContract(t *testing.T) {
	runAuditStoreContract(t, newPostgresAuditStoreOrSkip(t))
}

// runAuditStoreContract asserts the behaviour both backends owe a caller, so
// the in-memory default and the SQL stores cannot answer the same question
// differently.
//
// If this fails, the audit page shows one thing on a gateway with a database
// and another on a gateway without one. The denied case is called out
// separately below because it is the one this table exists for.
func runAuditStoreContract(t *testing.T, store AuditStore) {
	t.Helper()

	ctx := t.Context()
	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	entries := []model.AuditEntry{
		{OccurredAt: base, Action: "key.create", Actor: "Ops (key-1)", ActorID: "key-1", TargetID: "key-9", Outcome: model.AuditOK},
		{OccurredAt: base.Add(time.Minute), Action: "key.delete", Actor: "Ops (key-1)", ActorID: "key-1", TargetID: "key-9", Outcome: model.AuditDenied, Detail: `{"reason":"last admin key"}`},
		{OccurredAt: base.Add(2 * time.Minute), Action: "config.rollback", Actor: "CI (key-2)", ActorID: "key-2", TargetID: "7", Outcome: model.AuditError},
		{OccurredAt: base.Add(3 * time.Minute), Action: "session.create", Actor: "master-key", ActorID: "master-key", TargetID: "*", Outcome: model.AuditOK, SourceIP: "10.0.0.4", TraceID: "trace-abc"},
	}
	for _, entry := range entries {
		if err := store.Append(ctx, entry); err != nil {
			t.Fatalf("append %s: %v", entry.Action, err)
		}
	}

	assertAuditReads(t, store, base, len(entries))
	assertAuditPaging(t, store, len(entries))

	if err := store.Ping(ctx); err != nil {
		t.Errorf("ping: %v", err)
	}
}

// assertAuditReads covers ordering, round-tripping, the denied record, and the
// filters, over the fixture runAuditStoreContract seeds.
func assertAuditReads(t *testing.T, store AuditStore, base time.Time, count int) {
	t.Helper()

	ctx := t.Context()

	t.Run("newest first with exact total", func(t *testing.T) {
		result, err := store.List(ctx, model.AuditQuery{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if result.Total != count {
			t.Errorf("total = %d, want %d", result.Total, count)
		}
		if len(result.Data) != count {
			t.Fatalf("got %d entries, want %d", len(result.Data), count)
		}
		if result.Data[0].Action != "session.create" {
			t.Errorf("first entry is %q, want the newest (session.create)", result.Data[0].Action)
		}
		last := result.Data[len(result.Data)-1]
		if last.Action != "key.create" {
			t.Errorf("last entry is %q, want the oldest (key.create)", last.Action)
		}
	})

	t.Run("optional fields round-trip", func(t *testing.T) {
		result, err := store.List(ctx, model.AuditQuery{Action: "session.create"})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(result.Data) != 1 {
			t.Fatalf("got %d entries, want 1", len(result.Data))
		}
		got := result.Data[0]
		if got.SourceIP != "10.0.0.4" || got.TraceID != "trace-abc" || got.TargetID != "*" {
			t.Errorf("source_ip/trace_id/target_id lost: %+v", got)
		}
		if !got.OccurredAt.Equal(base.Add(3 * time.Minute)) {
			t.Errorf("occurred_at = %s, want %s", got.OccurredAt, base.Add(3*time.Minute))
		}
	})

	// A refusal is the record this table exists for: without it, "someone tried
	// to delete the last admin key and was stopped" is indistinguishable from
	// nobody having tried.
	t.Run("denied outcome is stored and filterable", func(t *testing.T) {
		result, err := store.List(ctx, model.AuditQuery{Outcome: model.AuditDenied})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if result.Total != 1 || len(result.Data) != 1 {
			t.Fatalf("total=%d data=%d, want 1 denied entry", result.Total, len(result.Data))
		}
		got := result.Data[0]
		if got.Action != "key.delete" || got.Outcome != model.AuditDenied {
			t.Errorf("got %s/%s, want key.delete/denied", got.Action, got.Outcome)
		}
		if got.Detail != `{"reason":"last admin key"}` {
			t.Errorf("detail = %q, want the refusal reason", got.Detail)
		}
	})

	t.Run("filters narrow both data and total", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			query model.AuditQuery
			want  int
		}{
			{name: "by actor", query: model.AuditQuery{ActorID: "key-1"}, want: 2},
			{name: "by action", query: model.AuditQuery{Action: "config.rollback"}, want: 1},
			{name: "by outcome", query: model.AuditQuery{Outcome: model.AuditOK}, want: 2},
			{name: "since", query: model.AuditQuery{Since: ptr(base.Add(2 * time.Minute))}, want: 2},
			{name: "combined", query: model.AuditQuery{ActorID: "key-1", Outcome: model.AuditOK}, want: 1},
			{name: "no match", query: model.AuditQuery{ActorID: "nobody"}, want: 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				result, err := store.List(ctx, tc.query)
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if result.Total != tc.want {
					t.Errorf("total = %d, want %d", result.Total, tc.want)
				}
				if len(result.Data) != tc.want {
					t.Errorf("data = %d entries, want %d", len(result.Data), tc.want)
				}
			})
		}
	})

}

// assertAuditPaging covers the page bounds and the exact Total that makes
// paging possible.
func assertAuditPaging(t *testing.T, store AuditStore, count int) {
	t.Helper()

	ctx := t.Context()

	// Total counts the filter's matches, not the page. A dashboard that pages on
	// len(Data) stops after one page and silently hides the rest of the trail.
	t.Run("total ignores limit and offset", func(t *testing.T) {
		result, err := store.List(ctx, model.AuditQuery{Limit: 2})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if result.Total != count {
			t.Errorf("total = %d, want %d", result.Total, count)
		}
		if len(result.Data) != 2 {
			t.Fatalf("got %d entries, want the 2 asked for", len(result.Data))
		}

		next, err := store.List(ctx, model.AuditQuery{Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("list page 2: %v", err)
		}
		if next.Total != count {
			t.Errorf("page 2 total = %d, want %d", next.Total, count)
		}
		if len(next.Data) != 2 {
			t.Fatalf("page 2 got %d entries, want 2", len(next.Data))
		}
		// Every entry appears exactly once across the two pages: an unstable
		// order would repeat one and drop another.
		seen := map[string]int{}
		for _, e := range append(append([]model.AuditEntry{}, result.Data...), next.Data...) {
			seen[e.Action]++
		}
		if len(seen) != count {
			t.Errorf("pages cover %d distinct actions, want %d: %v", len(seen), count, seen)
		}

		beyond, err := store.List(ctx, model.AuditQuery{Offset: 99})
		if err != nil {
			t.Fatalf("list past the end: %v", err)
		}
		if beyond.Total != count || len(beyond.Data) != 0 {
			t.Errorf("past the end: total=%d data=%d, want %d and 0", beyond.Total, len(beyond.Data), count)
		}
	})

	// A caller asking for everything gets a page. This is the audit trail: the
	// query most likely to be pointed at the largest table in the deployment.
	t.Run("limit is clamped", func(t *testing.T) {
		result, err := store.List(ctx, model.AuditQuery{Limit: maxAuditListLimit + 500})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(result.Data) > maxAuditListLimit {
			t.Errorf("got %d entries, want at most %d", len(result.Data), maxAuditListLimit)
		}
	})
}

// TestSQLiteAuditStoreBuildsIndexes proves the whole migration ran, not just
// its first statement.
//
// The step is one file holding a CREATE TABLE and three CREATE INDEXes, and the
// runner hands all of it to a single Exec. A driver that executed only the
// leading statement — or an edit that split the file — would leave a working
// table with no indexes, which nothing else here would notice until an audit
// page started scanning a large table.
func TestSQLiteAuditStoreBuildsIndexes(t *testing.T) {
	store := newSQLiteAuditStore(t)

	rows, err := store.db.QueryContext(t.Context(), "SELECT name FROM sqlite_master WHERE type = 'index'")
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer func() { _ = rows.Close() }()

	found := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		found[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}

	for _, want := range []string{"idx_audit_log_occurred_at", "idx_audit_log_actor_id", "idx_audit_log_action"} {
		if _, ok := found[want]; !ok {
			t.Errorf("index %s was not created; audit queries will scan", want)
		}
	}
}

// TestAuditStoreNormalisesOccurredAtToUTC covers the SQLite hazard the
// requestlog writer documents: the driver stores a time.Time as Go's rendering
// of it, offset and all, and the Since filter compares that text. If this
// fails, an entry stamped in a non-UTC zone sorts into the wrong window and
// drops out of — or wrongly into — a filtered audit page.
func TestAuditStoreNormalisesOccurredAtToUTC(t *testing.T) {
	zone := time.FixedZone("UTC+9", 9*60*60)
	stamped := time.Date(2026, 7, 23, 19, 0, 0, 0, zone) // 10:00 UTC

	for name, store := range map[string]AuditStore{
		"memory": NewMemoryAuditStore(),
		"sqlite": newSQLiteAuditStore(t),
	} {
		t.Run(name, func(t *testing.T) {
			if err := store.Append(t.Context(), model.AuditEntry{
				OccurredAt: stamped, Action: "key.create", Actor: "Ops (key-1)", ActorID: "key-1", Outcome: model.AuditOK,
			}); err != nil {
				t.Fatalf("append: %v", err)
			}

			result, err := store.List(t.Context(), model.AuditQuery{})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(result.Data) != 1 {
				t.Fatalf("got %d entries, want 1", len(result.Data))
			}
			got := result.Data[0].OccurredAt
			if got.Location() != time.UTC {
				t.Errorf("occurred_at is in %s, want UTC", got.Location())
			}
			if !got.Equal(stamped) {
				t.Errorf("occurred_at = %s, want the same instant as %s", got, stamped)
			}

			// The row is 10:00 UTC. A range starting at 09:00 UTC must include it
			// and one starting at 11:00 UTC must not — which is exactly what a
			// stored +09:00 rendering would get backwards.
			for _, tc := range []struct {
				name  string
				since time.Time
				want  int
			}{
				{name: "before", since: time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC), want: 1},
				{name: "after", since: time.Date(2026, 7, 23, 11, 0, 0, 0, time.UTC), want: 0},
			} {
				result, err := store.List(t.Context(), model.AuditQuery{Since: &tc.since})
				if err != nil {
					t.Fatalf("list since %s: %v", tc.name, err)
				}
				if result.Total != tc.want {
					t.Errorf("since %s: total = %d, want %d", tc.name, result.Total, tc.want)
				}
			}
		})
	}
}

// TestMemoryAuditStoreIsBounded proves the default backend cannot grow without
// limit. If this fails, a long-lived gateway with no database configured
// accumulates audit entries for the life of the process and frees none of them.
func TestMemoryAuditStoreIsBounded(t *testing.T) {
	store := NewMemoryAuditStore()
	store.max = 3 // keeps the test from writing a thousand rows to prove one rule

	for i := range 10 {
		if err := store.Append(t.Context(), model.AuditEntry{
			OccurredAt: time.Date(2026, 7, 23, 10, i, 0, 0, time.UTC),
			Action:     "key.create",
			Actor:      "Ops (key-1)",
			ActorID:    "key-1",
			TargetID:   string(rune('a' + i)),
			Outcome:    model.AuditOK,
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	if got := len(store.entries); got != store.max {
		t.Fatalf("holding %d entries, want at most %d", got, store.max)
	}

	result, err := store.List(t.Context(), model.AuditQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// The newest survive and the oldest are dropped: a trail that discarded the
	// most recent action would be worse than no trail.
	if result.Data[0].TargetID != "j" {
		t.Errorf("newest retained entry is %q, want %q", result.Data[0].TargetID, "j")
	}
	if result.Total != store.max {
		t.Errorf("total = %d, want %d", result.Total, store.max)
	}
}

// ptr returns a pointer to v, for the optional query fields.
func ptr[T any](v T) *T { return &v }
