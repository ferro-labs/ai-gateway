package fixture

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Handler serves the whole v0.1.0 surface for the world State describes. The
// key store is stateful for the handler's lifetime: create/rotate/revoke/delete
// mutate it and later GETs reflect the change.
func Handler(s State) http.Handler {
	if s.AcceptKey == "" {
		s.AcceptKey = defaultKey
	}
	ks := newKeyStore()
	tr := newTracer()
	mux := http.NewServeMux()
	registerHealth(mux, s)
	registerKeys(mux, ks)
	registerChat(mux, s, tr)
	registerLogs(mux, s, tr)
	registerAdmin(mux, s)
	registerModels(mux)

	// Anything unrouted answers in the documented envelope rather than the
	// mux's plain-text 404, so the CLI's decoder is exercised even here.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, kindNotFound, "no route for "+r.Method+" "+r.URL.Path)
	})

	return withRequestID(withAuth(s, mux))
}

// registerHealth wires the liveness and readiness surface. /livez, /health and
// /readyz are unauthenticated on the real router and so are they here;
// /admin/health is the bearer-guarded probe that also reports component state.
func registerHealth(mux *http.ServeMux, s State) {
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{fieldStatus: statusOK})
	})

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		// Verified against a live gateway: no_providers <=> providers is EMPTY,
		// and 503 <=> no_providers. Nothing else makes /health non-200 — a
		// half-open circuit with two live providers still answers 200 "ok".
		//
		// Degraded and provider-less are therefore independent axes, and an
		// earlier version of this handler conflated them: it served 503
		// "no_providers" alongside two populated providers, so ferro reported
		// "providers=2" next to the warning "health: no_providers" — a pairing
		// live traffic can never show. Degraded now means what it means on the
		// real server: a 200 whose circuit is half-open.
		if s.NoProviders {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				fieldStatus:    statusNoProviders,
				fieldProviders: []map[string]any{},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			fieldStatus: statusOK,
			fieldProviders: []map[string]any{
				{fieldName: providerAnthropic, fieldStatus: statusAvailable, fieldCircuit: circuitOf(s), fieldModels: 412},
				{fieldName: providerOpenAI, fieldStatus: statusAvailable, fieldCircuit: circuitClosed, fieldModels: 1104},
			},
		})
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		// Every not-ready path on the real server goes through one writer that
		// emits ONLY {status, reason} — no providers, no targets, no
		// mcp_servers. Serving the arrays on a 503 taught callers to read
		// counts that will not be there, which is how ferro status came to
		// report "0/0 targets" for a gateway whose targets were merely dead.
		if s.NoProviders {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				fieldStatus: statusNotReady,
				"reason":    "no routable targets",
			})
			return
		}
		// A gateway with some routable targets is still ready — only "none
		// routable" is 503 — so the degraded world reports 200 with one target
		// marked unroutable.
		out := map[string]any{
			fieldStatus: statusReady,
			fieldProviders: []map[string]any{
				{fieldName: providerAnthropic, fieldCircuit: circuitOf(s)},
				{fieldName: providerOpenAI, fieldCircuit: circuitClosed},
			},
			"targets": []map[string]any{
				{fieldName: providerAnthropic, "routable": !s.Degraded},
				{fieldName: providerOpenAI, "routable": true},
			},
		}
		if !s.NoMCP {
			out["mcp_servers"] = []map[string]any{
				{fieldName: "filesystem", fieldReady: true, "required": false},
			}
		}
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("GET /admin/health", func(w http.ResponseWriter, _ *http.Request) {
		anthropic := map[string]any{fieldName: providerAnthropic, fieldStatus: statusAvailable, fieldModels: 412}
		status := statusHealthy
		if s.NoProviders {
			status = statusNoProviders // the default state of a credential-free gateway
		}
		if s.Degraded {
			status = statusDegraded
			anthropic[fieldMessage] = "circuit half_open after 3 consecutive failures"
		}
		// Component names and vocabulary verified against a live gateway:
		// Title Case with spaces (not snake_case), status healthy/disabled/
		// unavailable (not "ok"), and five entries -- API and Audit log were
		// both missing here. A missing log store reports "disabled", not an
		// absent row.
		logStore := statusHealthy
		if s.NoLogStore {
			logStore = statusDisabled
		}
		components := []map[string]any{
			{fieldName: "API", fieldStatus: statusHealthy},
			{fieldName: "Key store", fieldStatus: statusHealthy},
			{fieldName: "Config store", fieldStatus: statusHealthy},
			{fieldName: "Request logs", fieldStatus: logStore},
			{fieldName: "Audit log", fieldStatus: statusHealthy},
		}
		out := map[string]any{
			// Always 200: this is the auth/scope probe.
			fieldStatus: status,
			fieldProviders: []map[string]any{
				anthropic,
				{fieldName: providerOpenAI, fieldStatus: statusAvailable, fieldModels: 1104},
			},
			"components": components,
			fieldScopes:  []string{scopeAdmin},
		}
		if !s.NoMCP {
			out["mcp_servers"] = []map[string]any{
				{fieldName: "filesystem", fieldReady: true, "required": false},
			}
		}
		writeJSON(w, http.StatusOK, out)
	})

}

// registerLogs wires the request-log surface. Both routes answer 501 when no
// store is configured, which is how the CLI feature-detects it.
func registerLogs(mux *http.ServeMux, s State, tr *tracer) {
	mux.HandleFunc("GET /admin/logs", func(w http.ResponseWriter, r *http.Request) {
		if s.NoLogStore {
			writeErr(w, http.StatusNotImplemented, kindServer, "no request log store configured")
			return
		}
		q := r.URL.Query()
		since, ok := parseSince(w, q)
		if !ok {
			return
		}
		rows := filterLogs(logRows(tr), q, since)
		total := len(rows)
		rows, ok = page(w, rows, q)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			fieldData: rows,
			fieldSummary: map[string]any{
				fieldTotalEntries:  total,
				"returned_entries": len(rows),
			},
			"filters": map[string]any{
				fieldLimit:    q.Get(fieldLimit),
				fieldOffset:   q.Get(fieldOffset),
				fieldSince:    q.Get(fieldSince),
				fieldModel:    q.Get(fieldModel),
				fieldProvider: q.Get(fieldProvider),
				fieldStage:    q.Get(fieldStage),
				fieldAPIKeyID: q.Get(fieldAPIKeyID),
			},
		})
	})

	// GET /admin/logs/stats aggregates the same seeded rows /admin/logs lists,
	// filtered the way the real handler's Stats query filters them
	// (internal/admin/handlers/logs.go's logsStats + requestlog.SQLWriter.Stats):
	// stage, model and provider are exact-match-or-absent and since keeps rows
	// at or after the cursor. There is deliberately no default-to-terminal-
	// stages narrowing here (unlike /admin/logs) and no api_key_id filter — the
	// real endpoint applies neither, and internal/api.LogStats never sends one.
	// An earlier version of this handler ignored every parameter and served one
	// fixed aggregate regardless of the query, so a CLI bug that dropped a
	// filter before it reached the wire was invisible against this fixture.
	mux.HandleFunc("GET /admin/logs/stats", func(w http.ResponseWriter, r *http.Request) {
		if s.NoLogStore {
			writeErr(w, http.StatusNotImplemented, kindServer, "no request log store configured")
			return
		}
		q := r.URL.Query()
		since, ok := parseSince(w, q)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, logStatsResponse(filterStats(logRows(tr), q, since)))
	})

}

// registerAdmin wires the remaining admin control-plane reads: sessions, the
// audit trail, the plugin surfaces and the provider inventory.
func registerAdmin(mux *http.ServeMux, s State) {
	mux.HandleFunc("GET /admin/sessions", func(w http.ResponseWriter, _ *http.Request) {
		if s.NoSessions {
			writeErr(w, http.StatusNotImplemented, kindServer, "dashboard sessions are disabled")
			return
		}
		now := time.Now().UTC()
		writeJSON(w, http.StatusOK, map[string]any{
			fieldData: []map[string]any{
				{
					"id": "sess_01", "credential_id": keyIDOps, "subject": actorOps,
					fieldScopes: []string{scopeAdmin}, "created_at": ts(now.Add(-3 * time.Hour)),
					"last_seen_at": ts(now.Add(-4 * time.Minute)), "expires_at": ts(now.Add(21 * time.Hour)),
				},
				{
					"id": "sess_02", "credential_id": keyIDCI, "subject": "ci-pipeline",
					fieldScopes: []string{scopeReadOnly}, "created_at": ts(now.Add(-40 * time.Minute)),
					"last_seen_at": nil, "expires_at": ts(now.Add(23 * time.Hour)),
				},
			},
		})
	})

	mux.HandleFunc("GET /admin/audit", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if out := q.Get(fieldOutcome); out != "" &&
			out != outcomeOK && out != outcomeDenied && out != outcomeError {
			writeErr(w, http.StatusBadRequest, kindInvalidRequest,
				// Worded exactly as internal/admin/handlers/audit_read.go does.
				"invalid outcome: must be one of ok, denied, error")
			return
		}
		// Shared with /admin/logs and /admin/logs/stats: the real handler's
		// since is the same parseSince on every route that accepts it
		// (internal/admin/handlers/queryparams.go), so all three reject a
		// malformed value with the identical 400 message.
		since, ok := parseSince(w, q)
		if !ok {
			return
		}
		rows := filterAudit(auditRows(), q, since)
		total := len(rows)
		rows, ok = page(w, rows, q)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			fieldData: rows,
			fieldSummary: map[string]any{
				fieldTotalEntries:  total,
				"returned_entries": len(rows),
			},
		})
	})

	mux.HandleFunc("GET /admin/plugins", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{ // bare array
			{fieldName: "request-logger", fieldType: typeLogging, fieldEnabled: true},
			{fieldName: "budget", fieldType: typeGuardrail, fieldEnabled: true},
			{fieldName: "rate-limit", fieldType: typeRateLimit, fieldEnabled: true},
			{fieldName: "word-filter", fieldType: typeGuardrail, fieldEnabled: false},
		})
	})

	mux.HandleFunc("GET /admin/plugins/catalog", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			fieldData: []map[string]any{
				{
					fieldName: "request-logger", fieldType: typeLogging,
					fieldSummary:  "Records one row per request stage",
					fieldSettings: []string{"persist", "log_bodies"}, fieldFailsOpen: true,
				},
				{
					fieldName: "budget", fieldType: typeGuardrail,
					fieldSummary:  "Refuses requests once a spend ceiling is reached",
					fieldSettings: []string{"limit_usd", "window", "store_id"}, fieldFailsOpen: false,
				},
				{
					fieldName: "rate-limit", fieldType: typeRateLimit,
					fieldSummary:  "Per-credential request rate ceiling",
					fieldSettings: []string{"requests_per_second", "burst"}, fieldFailsOpen: false,
				},
				{
					fieldName: "word-filter", fieldType: typeGuardrail,
					fieldSummary:  "Rejects prompts containing blocked words",
					fieldSettings: []string{"blocked_words"}, fieldFailsOpen: false,
				},
			},
		})
	})

	mux.HandleFunc("GET /admin/providers", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{ // bare array
			{fieldName: providerAnthropic, fieldModels: modelsOwnedBy(providerAnthropic)},
			{fieldName: providerOpenAI, fieldModels: modelsOwnedBy(providerOpenAI)},
		})
	})
}

// registerModels wires the /v1 discovery surface; the /v1 chat surface is
// registerChat's (stream.go).
func registerModels(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			fieldObject: "list",
			fieldData:   modelCatalog(),
		})
	})
}

// withAuth enforces the bearer on /admin/ AND /v1/, matching the real router.
//
// /health, /livez and /readyz are unauthenticated there and so are they here.
// Everything else is not: the gateway puts /v1/* behind the same middleware as
// /admin/* (only ALLOW_UNAUTHENTICATED_PROXY, a dev-only switch, lifts it). A
// fake that let /v1/models and /v1/chat/completions through unauthenticated
// would let the CLI's model discovery and playground be built without sending
// a credential, and the omission would surface as a 401 on first contact with
// a real gateway — precisely the drift this package must not introduce.
func withAuth(s State, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		guarded := strings.HasPrefix(r.URL.Path, "/admin/") ||
			strings.HasPrefix(r.URL.Path, "/v1/")
		if s.RequireAuth && guarded &&
			r.Header.Get("Authorization") != "Bearer "+s.AcceptKey {
			writeErr(w, http.StatusUnauthorized, kindAuthentication,
				"missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withRequestID stamps X-Request-ID on every response, as the gateway does. It
// is the id a log row carries as trace_id.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", newTraceID())
		next.ServeHTTP(w, r)
	})
}

// tracer remembers the last chat stream so /admin/logs can answer with a row
// whose trace_id equals that stream's X-Request-ID.
type tracer struct {
	mu    sync.RWMutex
	id    string
	model string
}

func newTracer() *tracer {
	return &tracer{id: newTraceID(), model: modelSonnet}
}

func (t *tracer) set(id, model string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.id, t.model = id, model
}

func (t *tracer) last() (id, model string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.id, t.model
}

// logRow is a /admin/logs entry, tags exactly as the gateway serves them.
// duration_ms, ttft_ms and cost_usd are nullable — the CLI renders "-" for a
// null, never 0 — and there is deliberately no cache/cached flag.
type logRow struct {
	TraceID          string   `json:"trace_id"`
	Stage            string   `json:"stage"`
	Model            string   `json:"model"`
	APIKeyID         string   `json:"api_key_id"`
	Provider         string   `json:"provider"`
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	TotalTokens      int      `json:"total_tokens"`
	ErrorMessage     string   `json:"error_message"`
	CreatedAt        string   `json:"created_at"`
	DurationMS       *float64 `json:"duration_ms"`
	TTFTMS           *float64 `json:"ttft_ms"`
	CostUSD          *float64 `json:"cost_usd"`
}

// logRows is the seeded request log, newest first. The first row attributes the
// most recent chat stream — same trace_id as its X-Request-ID — which is what
// lets the playground resolve provider and cost against the fake.
//
// The rows are in descending created_at order because that is the only order a
// real log is read in, and the tail's since-cursor advances on it: a seed that
// put an older row last-but-one would validate paging and cursor behavior
// against an order the gateway cannot produce.
func logRows(tr *tracer) []logRow {
	id, model := tr.last()
	now := time.Now().UTC()
	return []logRow{
		{
			TraceID: id, Stage: stageAfterRequest, Model: model, APIKeyID: keyIDOps,
			Provider: providerAnthropic, PromptTokens: 24, CompletionTokens: 7, TotalTokens: 31,
			CreatedAt:  ts(now.Add(-2 * time.Second)),
			DurationMS: fp(1284.5), TTFTMS: fp(312), CostUSD: fp(0.0031),
		},
		{
			// Non-terminal stage: visible only with stage=all, so the default
			// terminal-stage filter is observably doing something. It is the
			// row above's own earlier stage — same request, one second before
			// it completed — so it sorts here rather than at the end.
			TraceID: id, Stage: stageBeforeRequest, Model: model, APIKeyID: keyIDOps,
			Provider: providerAnthropic, CreatedAt: ts(now.Add(-3 * time.Second)),
		},
		{
			// Nullable measurements: the CLI must render "-", never 0.
			TraceID: "8f2c1d4ab9e34f7c9a0d5e6b71c28d3f", Stage: stageAfterRequest,
			Model: modelGPT4oMini, APIKeyID: keyIDCI, Provider: providerOpenAI,
			PromptTokens: 812, CompletionTokens: 44, TotalTokens: 856,
			CreatedAt: ts(now.Add(-51 * time.Second)),
		},
		{
			TraceID: "b31a77c0e5d24a1e8c93f0a6d2471be5", Stage: stageOnError,
			Model: modelOpus, APIKeyID: keyIDOps, Provider: providerAnthropic,
			ErrorMessage: "upstream_unavailable: circuit open",
			CreatedAt:    ts(now.Add(-4 * time.Minute)),
			DurationMS:   fp(30001.2),
		},
		{
			// Credential-less request: api_key_id is empty (?api_key_id=none).
			TraceID: "c07e5f9138b64d2fa1c6e7092b4d8a11", Stage: stageAfterRequest,
			Model: modelGPT4o, Provider: providerOpenAI,
			PromptTokens: 128, CompletionTokens: 512, TotalTokens: 640,
			CreatedAt:  ts(now.Add(-9 * time.Minute)),
			DurationMS: fp(4102), TTFTMS: fp(180), CostUSD: fp(0.0184),
		},
	}
}

// parseSince reads the optional since query parameter as RFC3339, writing the
// real handler's exact 400 (internal/admin/handlers/queryparams.go's
// parseSince) on anything that fails to parse and reporting ok=false so the
// caller returns. Reading it through url.Values.Get keeps a present-but-empty
// value read as absent — the same present-but-empty rule page applies to
// limit and offset — so `?since=` is not a second, silent way to ask for
// "unfiltered". Shared by /admin/logs, /admin/logs/stats and /admin/audit,
// the three routes that accept it.
func parseSince(w http.ResponseWriter, q url.Values) (time.Time, bool) {
	raw := q.Get(fieldSince)
	if raw == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, kindInvalidRequest, "invalid since: must be RFC3339 format")
		return time.Time{}, false
	}
	return t, true
}

// filterLogs applies the exact-match filters the endpoint documents. A fake
// that ignored them would answer rows the query excluded — a lie downstream
// tests would then enshrine. since is pre-parsed by parseSince rather than
// reparsed here, so a malformed value 400s before any row is ever filtered.
func filterLogs(rows []logRow, q map[string][]string, since time.Time) []logRow {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	stage, model, provider, keyID := get(fieldStage), get(fieldModel), get(fieldProvider), get(fieldAPIKeyID)

	// `since` is what a live tail advances on: Follower re-sends its cursor
	// every poll and expects to be given only what is newer. Echoing the
	// parameter without applying it made every poll return the whole page, so
	// the tail depended entirely on client-side dedupe to look correct.
	out := make([]logRow, 0, len(rows))
	for _, row := range rows {
		switch {
		case stage == "" && row.Stage != stageAfterRequest && row.Stage != stageOnError:
			continue // default filter: terminal stages only, one row per request
		case stage != "" && stage != "all" && row.Stage != stage:
			continue
		case model != "" && row.Model != model:
			continue
		case provider != "" && row.Provider != provider:
			continue
		case keyID == "none" && row.APIKeyID != "":
			continue
		case keyID != "" && keyID != "none" && row.APIKeyID != keyID:
			continue
		case !since.IsZero() && olderThan(row.CreatedAt, since):
			continue
		}
		out = append(out, row)
	}
	return out
}

// filterStats applies the filters GET /admin/logs/stats actually documents:
// stage, model and provider match exactly or are absent; since keeps rows at
// or after the cursor. There is no default-to-terminal-stages narrowing (the
// real query's WHERE clause never adds one — internal/requestlog/store.go's
// SQLWriter.Stats) and no api_key_id filter (the real handler never reads
// one), so this is deliberately not filterLogs with a parameter dropped.
func filterStats(rows []logRow, q map[string][]string, since time.Time) []logRow {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	stage, model, provider := get(fieldStage), get(fieldModel), get(fieldProvider)

	out := make([]logRow, 0, len(rows))
	for _, row := range rows {
		switch {
		case stage != "" && row.Stage != stage:
			continue
		case model != "" && row.Model != model:
			continue
		case provider != "" && row.Provider != provider:
			continue
		case !since.IsZero() && olderThan(row.CreatedAt, since):
			continue
		}
		out = append(out, row)
	}
	return out
}

// dimensionStat is one group's contribution to by_stage/by_provider/by_model —
// the same five fields the real handler's encodeDimension serves per group
// (internal/admin/handlers/logs.go) — accumulated here from the filtered rows
// instead of the store's SQL aggregation.
type dimensionStat struct {
	Count    int
	Errors   int
	Tokens   int
	CostUSD  float64
	Unpriced int
}

func (d *dimensionStat) add(row logRow) {
	d.Count++
	d.Tokens += row.TotalTokens
	if row.ErrorMessage != "" {
		d.Errors++
	}
	if row.CostUSD != nil {
		d.CostUSD += *row.CostUSD
	} else {
		d.Unpriced++
	}
}

func encodeDimensions(groups map[string]*dimensionStat) map[string]any {
	out := make(map[string]any, len(groups))
	for name, stat := range groups {
		out[name] = map[string]any{
			fieldCount: stat.Count, "errors": stat.Errors, "tokens": stat.Tokens,
			"cost_usd": stat.CostUSD, "unpriced": stat.Unpriced,
		}
	}
	return out
}

// logStatsResponse aggregates exactly the rows filterStats left in, the way
// the real handler's Stats does: an empty result answers zeroed counts and
// null percentiles rather than 200-with-yesterday's-numbers, so a filter that
// matches nothing reads differently from one that was silently dropped.
func logStatsResponse(rows []logRow) map[string]any {
	byStage := map[string]*dimensionStat{}
	byProvider := map[string]*dimensionStat{}
	byModel := map[string]*dimensionStat{}
	errorCounts := map[string]int{}
	errorEntries, totalTokens, promptTokens, completionTokens, unpriced := 0, 0, 0, 0, 0
	cost := 0.0
	var latencies, ttfts []float64

	group := func(m map[string]*dimensionStat, key string, row logRow) {
		if m[key] == nil {
			m[key] = &dimensionStat{}
		}
		m[key].add(row)
	}

	for _, row := range rows {
		if row.ErrorMessage != "" {
			errorEntries++
			errorCounts[row.ErrorMessage]++
		}
		totalTokens += row.TotalTokens
		promptTokens += row.PromptTokens
		completionTokens += row.CompletionTokens
		if row.CostUSD != nil {
			cost += *row.CostUSD
		} else {
			unpriced++
		}
		group(byStage, row.Stage, row)
		group(byProvider, row.Provider, row)
		group(byModel, row.Model, row)
		if row.DurationMS != nil {
			latencies = append(latencies, *row.DurationMS)
		}
		if row.TTFTMS != nil {
			ttfts = append(ttfts, *row.TTFTMS)
		}
	}

	return map[string]any{
		fieldSummary: map[string]any{
			fieldTotalEntries:   len(rows),
			"error_entries":     errorEntries,
			"total_tokens":      totalTokens,
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"cost_usd":          cost,
			"unpriced_requests": unpriced,
		},
		"latency_ms":  percentilesOf(latencies),
		"ttft_ms":     percentilesOf(ttfts),
		"by_stage":    encodeDimensions(byStage),
		"by_provider": encodeDimensions(byProvider),
		"by_model":    encodeDimensions(byModel),
		"top_errors":  topErrors(errorCounts),
		// The API defines series's inner shape (internal/admin/handlers/logs.go's
		// encodeSeries), but no CLI surface reads it (internal/api.LogStats
		// intentionally decodes no series field), so the fixture keeps the
		// cheaper empty object rather than modeling bucket math nothing consumes.
		"series": map[string]any{},
	}
}

// topErrors mirrors the real handler's encodeErrorGroups shape
// ({message, count}), ranked highest count first with message as the
// tiebreaker so JSON array order — unlike a map's — is deterministic.
func topErrors(counts map[string]int) []map[string]any {
	type group struct {
		message string
		count   int
	}
	groups := make([]group, 0, len(counts))
	for msg, n := range counts {
		groups = append(groups, group{msg, n})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].count != groups[j].count {
			return groups[i].count > groups[j].count
		}
		return groups[i].message < groups[j].message
	})
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		out = append(out, map[string]any{fieldMessage: g.message, fieldCount: g.count})
	}
	return out
}

// percentilesOf mirrors the real handler's encodePercentiles: nil when
// nothing was measured — never a zero-filled block, which would read as a
// real, implausibly fast measurement — otherwise nearest-rank p50/p95/p99
// plus max/mean/count.
func percentilesOf(vals []float64) any {
	if len(vals) == 0 {
		return nil
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	rank := func(p float64) float64 {
		idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		return sorted[idx]
	}
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	return map[string]any{
		"p50": rank(50), "p95": rank(95), "p99": rank(99),
		"max": sorted[len(sorted)-1], "mean": sum / float64(len(sorted)), fieldCount: len(sorted),
	}
}

// page applies offset then limit (limit is capped at 200, as documented).
// Shared by /admin/logs and /admin/audit, whose rows differ only in type.
//
// A limit that fails strconv.Atoi or is <= 0 writes the same 400 the real
// handler's parseLimit returns (internal/admin/handlers/queryparams.go) and
// reports ok=false, so the caller must return without serving its normal 200
// body.
//
// Both bounds mirror internal/admin/handlers/queryparams.go exactly, including
// the case that is easy to get wrong: the parameter PRESENT BUT EMPTY.
// parseLimit and parseOffset both read through url.Values.Get and return their
// default on "", so `?limit=` is the same request as no limit at all — the
// convention the gateway applies to model, provider, stage and api_key_id too.
// Reading it through the same Get is what keeps that from drifting; matching on
// len(q[key]) instead would 400 a request the real gateway serves, and the
// resulting "bug" would look like it lived in the CLI.
func page[T any](w http.ResponseWriter, rows []T, q url.Values) ([]T, bool) {
	limit := 100
	if raw := q.Get(fieldLimit); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, kindInvalidRequest, "invalid limit: must be a positive integer")
			return nil, false
		}
		limit = n
	}
	limit = min(limit, 200)

	offset := 0
	if raw := q.Get(fieldOffset); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, kindInvalidRequest, "invalid offset: must be a non-negative integer")
			return nil, false
		}
		offset = n
	}
	offset = min(offset, len(rows))

	rows = rows[offset:]
	if limit < len(rows) {
		rows = rows[:limit]
	}
	return rows, true
}

func auditRows() []map[string]any {
	now := time.Now().UTC()
	return []map[string]any{
		{
			fieldOccurredAt: ts(now.Add(-3 * time.Minute)), fieldAction: "key.create",
			fieldActor: actorOps, fieldActorID: keyIDOps, "target_id": "key_05",
			fieldOutcome: outcomeOK, fieldDetail: "scopes=read_only", fieldSourceIP: sourceIPLocal,
			"trace_id": "3a1f7c02d94b41e6b0f5a8c7d2e91b64",
		},
		{
			fieldOccurredAt: ts(now.Add(-2 * time.Hour)), fieldAction: "session.create",
			fieldActor: "unknown", fieldOutcome: outcomeDenied, fieldDetail: "invalid credential",
			fieldSourceIP: "10.0.4.19",
		},
		{
			fieldOccurredAt: ts(now.AddDate(0, 0, -12)), fieldAction: "key.revoke",
			fieldActor: actorOps, fieldActorID: keyIDOps, "target_id": "key_03",
			fieldOutcome: outcomeOK, fieldSourceIP: sourceIPLocal,
		},
		{
			fieldOccurredAt: ts(now.AddDate(0, 0, -20)), fieldAction: "logs.purge",
			fieldActor: actorOps, fieldActorID: keyIDOps, fieldOutcome: outcomeError,
			fieldDetail: "log store unavailable", fieldSourceIP: sourceIPLocal,
		},
	}
}

func filterAudit(rows []map[string]any, q map[string][]string, since time.Time) []map[string]any {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		occurred, _ := time.Parse(time.RFC3339Nano, stringValue(row[fieldOccurredAt]))
		if action := get("action"); action != "" && stringValue(row["action"]) != action {
			continue
		}
		if actorID := get(fieldActorID); actorID != "" && stringValue(row[fieldActorID]) != actorID {
			continue
		}
		if outcome := get(fieldOutcome); outcome != "" && stringValue(row[fieldOutcome]) != outcome {
			continue
		}
		if !since.IsZero() && occurred.Before(since) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

// modelCatalog is the /v1/models payload; /admin/providers serves the same rows
// grouped by owner.
func modelCatalog() []map[string]any {
	return []map[string]any{
		{
			"id": modelSonnet, fieldObject: objectModel, fieldCreated: 1748736000,
			fieldOwnedBy: providerAnthropic, fieldMode: modalityChat, fieldContextWindow: 200000,
			fieldMaxOutputTokens: 64000, fieldStatus: statusStable, fieldDeprecated: false,
			fieldCapabilities: []string{modalityChat, capabilityStreaming, capabilityTools, capabilityVision},
		},
		{
			"id": modelOpus, fieldObject: objectModel, fieldCreated: 1751328000,
			fieldOwnedBy: providerAnthropic, fieldMode: modalityChat, fieldContextWindow: 200000,
			fieldMaxOutputTokens: 32000, fieldStatus: statusStable, fieldDeprecated: false,
			fieldCapabilities: []string{modalityChat, capabilityStreaming, capabilityTools, capabilityVision},
		},
		{
			"id": modelGPT4o, fieldObject: objectModel, fieldCreated: 1715558400,
			fieldOwnedBy: providerOpenAI, fieldMode: modalityChat, fieldContextWindow: 128000,
			fieldMaxOutputTokens: 16384, fieldStatus: statusStable, fieldDeprecated: false,
			fieldCapabilities: []string{modalityChat, capabilityStreaming, capabilityTools, capabilityVision},
		},
		{
			"id": modelGPT4oMini, fieldObject: objectModel, fieldCreated: 1721260800,
			fieldOwnedBy: providerOpenAI, fieldMode: modalityChat, fieldContextWindow: 128000,
			fieldMaxOutputTokens: 16384, fieldStatus: statusStable, fieldDeprecated: false,
			fieldCapabilities: []string{modalityChat, capabilityStreaming, capabilityTools},
		},
		{
			"id": modelEmbedding3Small, fieldObject: objectModel, fieldCreated: 1705363200,
			fieldOwnedBy: providerOpenAI, fieldMode: modalityEmbedding, fieldContextWindow: 8191,
			fieldStatus: statusStable, fieldDeprecated: false,
			fieldCapabilities: []string{modalityEmbedding},
		},
	}
}

func modelsOwnedBy(owner string) []map[string]any {
	out := []map[string]any{}
	for _, m := range modelCatalog() {
		if m[fieldOwnedBy] == owner {
			out = append(out, m)
		}
	}
	return out
}

func circuitOf(s State) string {
	if s.Degraded {
		return circuitHalfOpen
	}
	return circuitClosed
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr emits the gateway's error envelope, so the CLI's decodeAPIError is
// exercised by the fixture rather than mocked around.
func writeErr(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, map[string]any{
		fieldError: map[string]any{fieldMessage: msg, fieldType: kind, "code": kind},
	})
}

// ts renders a wire timestamp: RFC3339, UTC.
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func sp(s string) *string   { return &s }
func fp(f float64) *float64 { return &f }

// newTraceID mints a 32-hex id shaped like the gateway's (its X-Request-ID is
// the OTel trace id).
func newTraceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// randToken mints a random base32 token for a key secret.
func randToken() string { return rand.Text() }

// olderThan reports whether an RFC3339 created_at precedes the cursor. A row
// whose timestamp will not parse is kept: dropping rows on a formatting
// problem would make a tail silently lossy, which is worse than showing one
// row twice.
func olderThan(createdAt string, since time.Time) bool {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return false
	}
	return t.Before(since)
}
