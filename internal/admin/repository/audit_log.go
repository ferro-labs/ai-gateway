package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ferro-labs/ai-gateway/internal/admin/model"
	"github.com/ferro-labs/ai-gateway/internal/migrations"
	"github.com/ferro-labs/ai-gateway/internal/redact"
	"github.com/ferro-labs/ai-gateway/internal/sqldb"
)

// Page bounds for AuditStore.List, matching the request log's: a caller that
// omits Limit gets a page, and one that asks for everything gets a page too.
// An unbounded audit query is the one query most likely to be pointed at the
// largest table in the deployment.
const (
	defaultAuditListLimit = 50
	maxAuditListLimit     = 200
)

// maxMemoryAuditEntries bounds MemoryAuditStore.
//
// MemoryAuditStore is the default backend, not a test double, so an unbounded
// slice here is a process that grows for as long as it runs and frees nothing.
// Past this many entries the oldest is dropped: the trail becomes the most
// recent N actions rather than all of them, which is the price of keeping it in
// memory. Deployments that need the full history configure a SQL store.
const maxMemoryAuditEntries = 1000

// AuditStore records administrative actions.
//
// It is append-only from the application: there is no Update and no Delete,
// because a record the audited party can edit answers nothing. (Retention is a
// separate concern and deliberately not solved here.)
//
// Append returns an error, but callers are expected to log it and continue
// rather than fail the action they were auditing. The default backend is
// MemoryAuditStore and the SQL one is a database that can be down, so failing
// the request would make key management depend on an audit write — trading a
// missing row for a broken admin API, which is the worse of the two failures.
// The store does not swallow the error itself: which actions may proceed
// unrecorded is a policy the call site owns, not the storage layer.
type AuditStore interface {
	Append(ctx context.Context, entry model.AuditEntry) error
	List(ctx context.Context, query model.AuditQuery) (model.AuditResult, error)
	// Ping reports whether the store is reachable. Readiness probes call it, so
	// it must be cheap and return quickly.
	Ping(ctx context.Context) error
	Close() error
}

// auditRedactor is built once. DefaultPolicies compiles ten regexes, and
// rebuilding them per Append would put that cost on the admin write path.
var auditRedactor = redact.DefaultRedactor()

// prepareAuditEntry applies the two rules every backend owes a stored entry, in
// one place so the implementations cannot drift apart on either.
//
// Redaction is not a formality. Detail is composed by the caller out of
// whatever the mutation touched — an error string, a provider response, a
// config fragment — and any of those can quote a bearer token or an AWS key.
// Written verbatim, that secret lands in the one table built to be read by
// whoever is investigating an incident, which turns the audit trail itself into
// the leak.
//
// The timestamp is normalised to UTC, not merely defaulted. The SQLite driver
// stores a time.Time as Go's own rendering of it, zone offset and all, and the
// Since filter is a string comparison over that text; the comparison is
// chronologically correct only while every row carries the same offset, so an
// entry stamped in a local zone would sort into the wrong window.
func prepareAuditEntry(entry model.AuditEntry) model.AuditEntry {
	if entry.OccurredAt.IsZero() {
		entry.OccurredAt = time.Now()
	}
	entry.OccurredAt = entry.OccurredAt.UTC()
	entry.Detail = auditRedactor.Redact(entry.Detail)
	return entry
}

// clampAuditQuery applies the page bounds and rejects a negative offset.
func clampAuditQuery(query model.AuditQuery) model.AuditQuery {
	if query.Limit <= 0 {
		query.Limit = defaultAuditListLimit
	}
	if query.Limit > maxAuditListLimit {
		query.Limit = maxAuditListLimit
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	return query
}

// MemoryAuditStore keeps the audit trail in memory. It is what a gateway with
// no database configured uses, which is the default deployment, so it is a real
// implementation rather than a stub: bounded, concurrency-safe, and filtering
// and paging exactly as the SQL store does.
//
// Entries do not survive a restart. That is a real limitation of running
// without a database, not a bug — the alternative on this backend is losing
// them to unbounded growth instead.
type MemoryAuditStore struct {
	mu  sync.Mutex
	max int
	// entries is in insertion order, oldest first. List reverses it.
	entries []model.AuditEntry
}

// NewMemoryAuditStore creates an empty in-memory audit store.
func NewMemoryAuditStore() *MemoryAuditStore {
	return &MemoryAuditStore{max: maxMemoryAuditEntries}
}

// Append records an entry, dropping the oldest once the bound is reached.
func (s *MemoryAuditStore) Append(_ context.Context, entry model.AuditEntry) error {
	entry = prepareAuditEntry(entry)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = append(s.entries, entry)
	if len(s.entries) > s.max {
		// Shift in place rather than reallocating. An admin mutation is a rare
		// event, so this copy is never on a hot path; a ring buffer would buy
		// nothing here except an index to get wrong.
		s.entries = append(s.entries[:0], s.entries[len(s.entries)-s.max:]...)
	}
	return nil
}

// List returns the matching page, newest first, with the exact match count.
func (s *MemoryAuditStore) List(_ context.Context, query model.AuditQuery) (model.AuditResult, error) {
	query = clampAuditQuery(query)

	s.mu.Lock()
	defer s.mu.Unlock()

	matched := make([]model.AuditEntry, 0, len(s.entries))
	for i := len(s.entries) - 1; i >= 0; i-- {
		if auditEntryMatches(s.entries[i], query) {
			matched = append(matched, s.entries[i])
		}
	}
	// Stable, so entries sharing a timestamp keep the reverse-insertion order
	// the loop produced — the same tiebreak the SQL store's "occurred_at DESC,
	// id DESC" gives. Without a deterministic tiebreak, paging through entries
	// written in the same clock tick can show one row twice and skip another.
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].OccurredAt.After(matched[j].OccurredAt)
	})

	total := len(matched)
	if query.Offset >= total {
		return model.AuditResult{Data: []model.AuditEntry{}, Total: total}, nil
	}
	end := min(query.Offset+query.Limit, total)

	// Copied out: AuditEntry holds no reference types, so this hands the caller
	// a page it cannot use to reach back into stored state.
	page := make([]model.AuditEntry, end-query.Offset)
	copy(page, matched[query.Offset:end])
	return model.AuditResult{Data: page, Total: total}, nil
}

// Ping always succeeds: an in-memory store is reachable whenever the process
// is, matching KeyStore.Ping.
func (s *MemoryAuditStore) Ping(_ context.Context) error { return nil }

// Close releases nothing; it exists so callers can shut every AuditStore down
// the same way regardless of which one they were given.
func (s *MemoryAuditStore) Close() error { return nil }

// auditEntryMatches applies the same predicates the SQL WHERE clause does, so
// the two backends answer one query identically.
func auditEntryMatches(entry model.AuditEntry, query model.AuditQuery) bool {
	if query.Action != "" && entry.Action != query.Action {
		return false
	}
	if query.ActorID != "" && entry.ActorID != query.ActorID {
		return false
	}
	if query.Outcome != "" && entry.Outcome != query.Outcome {
		return false
	}
	// Inclusive, matching the SQL "occurred_at >= ?".
	if query.Since != nil && entry.OccurredAt.Before(query.Since.UTC()) {
		return false
	}
	return true
}

// SQLAuditStore persists the audit trail in SQLite or Postgres. Both dialects
// carry the same logical schema; only the id and timestamp types differ.
type SQLAuditStore struct {
	db      *sql.DB
	dialect sqldb.Dialect
}

// NewSQLAuditStore runs the audit migrations and returns a ready store.
func NewSQLAuditStore(ctx context.Context, db *sql.DB, dialect sqldb.Dialect) (*SQLAuditStore, error) {
	// No primary table is passed, so the runner never adopts an existing
	// audit_log as an already-applied baseline. Nothing predates this migration,
	// and adoption would skip the step that builds the indexes.
	if err := migrations.RunNamed(ctx, db, dialect, auditLedger, "", auditStoreSteps(dialect)); err != nil {
		return nil, fmt.Errorf("migrate %s audit log schema: %w", dialect, err)
	}
	return &SQLAuditStore{db: db, dialect: dialect}, nil
}

// NewSQLiteAuditStore opens or creates the SQLite audit database.
//
// Its own file, like the session store's: the audit trail and the credentials
// it describes have different lifetimes, and a key file an operator copies
// between machines has no business carrying the history of who touched it.
func NewSQLiteAuditStore(ctx context.Context, dsn string) (*SQLAuditStore, error) {
	db, err := sqldb.Open(ctx, sqldb.SQLite, dsn, "ferrogw-audit.db")
	if err != nil {
		return nil, err
	}
	store, err := NewSQLAuditStore(ctx, db, sqldb.SQLite)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// NewPostgresAuditStore connects to the PostgreSQL audit database.
//
// It may share a DSN with the key, config, and session stores: each carries its
// own migration ledger, so their version sequences cannot collide.
func NewPostgresAuditStore(ctx context.Context, dsn string) (*SQLAuditStore, error) {
	db, err := sqldb.Open(ctx, sqldb.Postgres, dsn, "")
	if err != nil {
		return nil, err
	}
	store, err := NewSQLAuditStore(ctx, db, sqldb.Postgres)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Append writes one entry. See AuditStore for why a caller logs the error and
// carries on rather than failing the action it was recording.
func (s *SQLAuditStore) Append(ctx context.Context, entry model.AuditEntry) error {
	entry = prepareAuditEntry(entry)

	query := sqldb.Bind(s.dialect,
		`INSERT INTO audit_log(occurred_at, action, actor, actor_id, target_id, outcome, detail, source_ip, trace_id)
	VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`)

	// The optional columns are written as the empty string rather than NULL.
	// Absent and empty are the same fact for every one of them — an action with
	// no source IP and one with a blank source IP are one thing — and the
	// nullable columns still read back correctly because the scan below handles
	// NULL either way.
	// #nosec G701 -- query is a fixed literal routed through sqldb.Bind; every value is a bound parameter.
	_, err := s.db.ExecContext(ctx, query,
		entry.OccurredAt,
		entry.Action,
		entry.Actor,
		entry.ActorID,
		entry.TargetID,
		string(entry.Outcome),
		entry.Detail,
		entry.SourceIP,
		entry.TraceID,
	)
	if err != nil {
		return fmt.Errorf("append audit entry: %w", err)
	}
	return nil
}

// List returns the matching page, newest first, with the exact match count.
func (s *SQLAuditStore) List(ctx context.Context, query model.AuditQuery) (model.AuditResult, error) {
	query = clampAuditQuery(query)

	whereClauses := make([]string, 0, 4)
	args := make([]any, 0, 4)

	if query.Action != "" {
		whereClauses = append(whereClauses, "action = ?")
		args = append(args, query.Action)
	}
	if query.ActorID != "" {
		whereClauses = append(whereClauses, "actor_id = ?")
		args = append(args, query.ActorID)
	}
	if query.Outcome != "" {
		whereClauses = append(whereClauses, "outcome = ?")
		args = append(args, string(query.Outcome))
	}
	if query.Since != nil {
		whereClauses = append(whereClauses, "occurred_at >= ?")
		args = append(args, query.Since.UTC())
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Total is counted rather than derived from the page, so a caller paging
	// through knows how many rows it is paging through.
	// #nosec G202 G701 -- whereSQL is built only from fixed predicates; every value is a bound placeholder.
	countQuery := sqldb.Bind(s.dialect, "SELECT COUNT(*) FROM audit_log"+whereSQL)

	var total int
	// #nosec G701 -- countQuery is assembled from fixed predicates and bound placeholders.
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return model.AuditResult{}, fmt.Errorf("count audit entries: %w", err)
	}

	// The id tiebreak keeps paging stable across entries written in the same
	// clock tick; occurred_at alone leaves their order to the planner, which can
	// repeat one row on one page and drop another.
	// #nosec G202 -- whereSQL is built only from fixed predicates and bound placeholders.
	listQuery := sqldb.Bind(s.dialect,
		"SELECT occurred_at, action, actor, actor_id, target_id, outcome, detail, source_ip, trace_id FROM audit_log"+
			whereSQL+" ORDER BY occurred_at DESC, id DESC LIMIT ? OFFSET ?")

	listArgs := make([]any, 0, len(args)+2)
	listArgs = append(listArgs, args...)
	listArgs = append(listArgs, query.Limit, query.Offset)

	// #nosec G701 -- listQuery is assembled from fixed predicates and bound placeholders.
	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return model.AuditResult{}, fmt.Errorf("list audit entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	entries := make([]model.AuditEntry, 0, query.Limit)
	for rows.Next() {
		var (
			entry    model.AuditEntry
			actorID  sql.NullString
			targetID sql.NullString
			outcome  string
			detail   sql.NullString
			sourceIP sql.NullString
			traceID  sql.NullString
		)
		if err := rows.Scan(&entry.OccurredAt, &entry.Action, &entry.Actor, &actorID, &targetID,
			&outcome, &detail, &sourceIP, &traceID); err != nil {
			return model.AuditResult{}, fmt.Errorf("scan audit entry: %w", err)
		}
		entry.OccurredAt = entry.OccurredAt.UTC()
		entry.Outcome = model.AuditOutcome(outcome)
		entry.ActorID = actorID.String
		entry.TargetID = targetID.String
		entry.Detail = detail.String
		entry.SourceIP = sourceIP.String
		entry.TraceID = traceID.String
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return model.AuditResult{}, fmt.Errorf("iterate audit entries: %w", err)
	}

	return model.AuditResult{Data: entries, Total: total}, nil
}

// Ping verifies the backing database is reachable.
func (s *SQLAuditStore) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("audit store ping: store not initialized")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("audit store ping: %w", err)
	}
	return nil
}

// Close closes the underlying SQL connection, matching the other stores' Close
// so bootstrap shutdown can close every store the same way.
func (s *SQLAuditStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Compile-time assertions, matching the guards every provider package uses.
var (
	_ AuditStore = (*MemoryAuditStore)(nil)
	_ AuditStore = (*SQLAuditStore)(nil)
)
